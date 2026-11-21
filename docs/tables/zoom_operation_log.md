# zoom_operation_log

Query administrative operation logs for the preceding 30 days by default.

```sql
select time, operator, action, category_type, operation_detail
from zoom_operation_log
where time >= current_timestamp - interval '7 days'
order by time desc;
```

Optional `time >=` and `time <` predicates define an exact half-open interval of at most 30 days. Provider history limits also apply. `LIMIT` does not reduce API requests.

See the [date-window and collection contract](../../README.md) and the [column definitions](../../zoom/plugin.go).
