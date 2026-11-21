# zoom_group_member

Query group memberships. With no `group_id` predicate, the plugin reads all groups and all member pages. A denied group fails the query.

```sql
select group_id, id, email from zoom_group_member;
```

An optional `group_id =` predicate reads one group's members without listing groups.

See [connection and scope requirements](../../README.md) and the [column definitions](../../zoom/plugin.go).
