# zoom_room

Query Zoom Rooms visible to the account. A Zoom Rooms entitlement is required.

```sql
select id, name, status, location_id from zoom_room;
```

See [connection and scope requirements](../../README.md) and the [column definitions](../../zoom/plugin.go).
