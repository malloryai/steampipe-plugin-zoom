# zoom_device

Query managed devices across the device type, enrollment and status partitions described in the [collection contract](../../README.md). Devices use `device_id`; Room devices use a separate table and identifier.

```sql
select device_id, device_name, vendor, model, platform_os,
       app_version, device_status, enrolled_in_zdm
from zoom_device;
```

This API does not provide a complete endpoint software inventory. See the [column definitions](../../zoom/plugin.go).
