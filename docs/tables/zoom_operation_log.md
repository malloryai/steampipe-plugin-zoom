# zoom_operation_log

Query administrative operation logs for the preceding 30 days by default.

Replace the example timestamps with the UTC interval to collect.

```sql
select time, operator, action, category_type, operation_detail
from zoom_operation_log
where time >= TIMESTAMPTZ '2026-11-01 00:00:00+00'
  and time < TIMESTAMPTZ '2026-11-08 00:00:00+00'
order by time desc;
```

Optional `time >=` and `time <` predicates with explicit UTC timestamp literals define an exact half-open interval of at most 30 days. SQL function expressions may filter only the default snapshot without changing the API window. Provider history limits also apply. `LIMIT` does not reduce API requests.

See the [date-window and collection contract](../../README.md) and the [column definitions](../../zoom/plugin.go).
