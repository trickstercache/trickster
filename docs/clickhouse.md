# ClickHouse Support

Trickster 1.0 provides experimental support for accelerating ClickHouse queries that return time series data normally visualized on a dashboard. Acceleration works by using the Time Series Delta Proxy Cache to minimize the number and time range of queries to the upstream ClickHouse server.

## Scope of Support

Trickster is tested with the [ClickHouse DataSource Plugin for Grafana](https://grafana.com/grafana/plugins/vertamedia-clickhouse-datasource) v1.9.3 by Vertamedia, and supports acceleration of queries constructed by this plugin using the plugin's built-in `$timeSeries` macro.

Because ClickHouse does not provide a golang-based query parser, Trickster uses pre-compiled Regular Expression pattern matches on the incoming ClickHouse query to deconstruct its components, determine if it is cacheable and, if so, what elements are factored into the cache key derivation. We also determine what parts of the query are template-able (e.g., `time BETWEEN $time1 AND $time2`) based on the provided absolute values, in order to normalize the query before hashing the cache key.

If you find query or response structures that are not yet supported, or providing inconsistent or unexpected results, we'd love for you to report those. We also always welcome any contributions around this functionality. The regular expression patterns we currently use will likely grow in complexity as support for more query patterns is added. Thus, we may need to find a more robust query parsing solution, and welcome any assistance with that as well.

Trickster currently supports the following query patterns (case-insensitive) in the JSON response format, which align with the output of the ClickHouse Data Source Plugin for Grafana:

```sql
SELECT (intDiv(toUInt32(time_col), 60) * 60) * 1000 AS t, countMerge(val_col) AS cnt, field1, field2
FROM exampledb.example_table WHERE time_col BETWEEN toDateTime(1574686300) AND toDateTime(1574689900)
	AND field1 > 0 AND field2 = 'some_value' GROUP BY t, field1, field2 ORDER BY t, field1, field2
FORMAT JSON
```

In this format, the first column must be the datapoint's timestamp, the second column must be the datapoint's value, and all additional fields define the datapoint's metric name. The time column must be in the format of `(intDiv(toUInt32($time_col), $period) * $period) * 1000`, and the value column must be numeric (integer or floating point). The where clause must include `time_col > toDateTime($epoch)` or `time_col BETWEEN toDateTime($epoch1) AND toDateTime($epoch2)`. Subqueries and other modifications are compatible so long as the key components of the time series, mentioned here, can be extracted.
