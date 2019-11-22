# ClickHouse Support

Trickster 1.0 provides experimental support for accelerating ClickHouse queries which return time series data that are normally visualized on a dashboard. It is not meant to accelerate or cache general ClickHouse queries that are not based on a repeatable time series. Acceleration works by using the Time Series Delta Proxy Cache to minimize the number and time range of queries to the upstream ClickHouse server.

## Scope of Support

Because ClickHouse does not provide a golang-based query parser, we currently use pre-compiled Regular Expression pattern matches on the incoming ClickHouse query to deconstruct its components, determine if it is cacheable and, if so, what elements are factored into the cache key derivation. We also determine what parts of the query are template-able (e.g., `time BETWEEN $time1 AND $time2`) based on the provided absolute values, in order to normalize the query before hashing the cache key.

If you find query or response structures that are not yet supported, or providing inconsistent or unexpected results, we'd love for you to report those. We also always welcome any contributions around this functionality. The regular expression patterns we currently use will likely grow in complexity as support for more query patterns is added. Thus, we may need to find a more robust query parsing solution, and welcome any assistance with that as well.

Trickster currently supports ClickHouse JSON results, and the following query patterns (case-insensitive):

**JSON Results with columns as follows: unix_ts, value, label1, label2...**

```sql
## Queries with WHERE clause in the format of:
## WHERE ts BETWEEN x AND y AND field1='filter'... GROUP by ts, labels ORDER BY ts, labels

# intDiv(toUInt32(time_col)), by period (60s), optional: as ms instead of secs ('* 1000')
SELECT (intDiv(toUInt32(time_col), 60) * 60) * 1000 AS t, countMerge(some_count) AS cnt, field1, field2
FROM exampledb.example_table WHERE time_col BETWEEN toDateTime($epoch1) AND toDateTime($epoch2)
	AND field1 > 0 AND field2 = 'some_value' GROUP BY t, field1, field2 ORDER BY t, field1, field2
FORMAT JSON
```
