# zoom_room_device

Query devices for every Zoom Room. Each row includes its `room_id`. A denied Room fails the query. An optional `room_id =` predicate reads one Room's devices without listing Rooms.

```sql
select room_id, id, device_type, status, app_version
from zoom_room_device;
```

See [connection and scope requirements](../../README.md) and the [column definitions](../../zoom/plugin.go).
