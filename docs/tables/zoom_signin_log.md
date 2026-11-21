# zoom_signin_log

Query sign-in and sign-out activity for the preceding 30 days by default. These rows do not represent failed sign-in attempts.

```sql
select time, email, type, ip_address, client_type, version
from zoom_signin_log
where time >= current_timestamp - interval '1 day'
order by time desc;
```

Optional `time >=` and `time <` predicates define an exact half-open interval of at most 30 days. Provider history limits also apply. `LIMIT` does not reduce API requests.

See the [date-window and collection contract](../../README.md) and the [column definitions](../../zoom/plugin.go).
