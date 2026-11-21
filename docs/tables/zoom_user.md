# zoom_user

Query users in the active, inactive and pending populations. An optional `status =` predicate selects one population. The plugin reads every page.

```sql
select id, email, first_name, last_name, status, type
from zoom_user
where status = 'inactive';
```

See [connection and scope requirements](../../README.md) and the [column definitions](../../zoom/plugin.go).
