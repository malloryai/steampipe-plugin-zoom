# Zoom plugin for Steampipe

Query Zoom account inventory and security logs with SQL. This Mallory fork uses the [Steampipe plugin SDK](https://github.com/turbot/steampipe-plugin-sdk) and retains the [upstream Apache-2.0 license](LICENSE).

## Connection

Create and activate a [Zoom Server-to-Server OAuth app](https://developers.zoom.us/docs/internal-apps/s2s-oauth/). Set all three credentials in the connection:

```hcl
connection "zoom" {
  plugin = "zoom"
  account_id = "your-account-id"
  client_id = "your-client-id"
  client_secret = "your-client-secret"
}
```

The plugin accepts only these explicit credentials. Tokens renew automatically. It does not read environment credentials or support JWT apps. API requests use the standard Zoom service domains.

Grant the read scope for each table you query:

| Table | Read scope |
| --- | --- |
| `zoom_user` | `user:read:list_users:admin` |
| `zoom_group` | `group:read:list_groups:admin` |
| `zoom_group_member` | `group:read:list_groups:admin`, `group:read:list_members:admin` |
| `zoom_device` | `device:read:list_zdm_devices:admin` |
| `zoom_room` | `zoom_rooms:read:list_rooms:admin` |
| `zoom_room_device` | `zoom_rooms:read:list_rooms:admin`, `zoom_rooms:read:list_devices:admin` |
| `zoom_signin_log` | `report:read:user_activities:admin` |
| `zoom_operation_log` | `report:read:operation_logs:admin` |

Reports require Pro or higher. Room tables require the Zoom Rooms entitlement. Missing access fails the query; it does not produce an empty successful result. See the [Users API](https://developers.zoom.us/docs/api/users/), [Meetings API](https://developers.zoom.us/docs/api/meetings/) and [Rooms API](https://developers.zoom.us/docs/api/rooms/).

## Queries

```sql
select id, email, status from zoom_user where status = 'inactive';
select group_id, id, email from zoom_group_member;
select device_id, device_name, app_version, device_status from zoom_device;
select room_id, id, status, app_version from zoom_room_device;
select time, email, type, ip_address from zoom_signin_log
where time >= current_timestamp - interval '1 day'
order by time desc;
```

Users include active, inactive and pending populations. An optional `status =` predicate narrows that scan. Group membership and Room device queries enumerate parents unless `group_id =` or `room_id =` is supplied.

Room scans include both values of `unassigned_rooms`. Duplicate Room IDs are read once. Room-device scans use the same complete parent list.

Managed device scans enumerate device types `-1, 5, 7, 8, 9`, both ZDM enrollment values and statuses `-1, 0, 1`. Type `-1` covers Room device types. Managed devices and Room devices remain separate tables. These APIs do not provide a complete endpoint software inventory.

Log queries default to the preceding 30 days, ending at one fixed UTC time per query. Use `time >=` and `time <` for a half-open interval of at most 30 days. Other time comparison operators return an error. An upper bound without a lower bound selects the 30 days before that upper bound. A lower bound without an upper bound ends at the query start time. Provider date requests contain at most seven calendar days and do not cross a month boundary. Rows are filtered to the exact requested interval. Sign-in history is subject to Zoom's six-month API limit.

Every query completes all pages and selected partitions before it returns rows. `LIMIT` does not reduce API calls. The plugin stores selected fields in a private temporary file during the scan and removes the file when the query ends. Cancellation, invalid responses and denied child resources fail the scan. Rate-limit retries are bounded and respect `Retry-After`; waits longer than the retry budget fail the query.

## Development

```sh
go test -race ./...
go vet ./...
go build ./...
```

The plugin uses the SDK major version supported by the Steampunk embedded FDW. See [the table definitions](zoom/plugin.go) for the current column schema.
