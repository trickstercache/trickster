# ClickHouse Support

Trickster will accelerate ClickHouse queries that return time series data normally visualized on a dashboard. Acceleration works by using the Time Series Delta Proxy Cache to minimize the number and time range of queries to the upstream ClickHouse server.

## Scope of Support

Trickster is tested with the [ClickHouse DataSource Plugin for Grafana](https://grafana.com/grafana/plugins/vertamedia-clickhouse-datasource) v1.9.3 by Vertamedia, and supports acceleration of queries constructed by this plugin using the plugin's built-in `$timeSeries` macro.  Trickster also supports several other query formats that return "time series like" data.

Trickster also supports the ClickHouse Go SDK (`clickhouse-go/v2`). Queries made through the SDK's HTTP protocol — including those using `clickhouse.OpenDB` — are proxied and cached through Trickster. The SDK's Native binary protocol is supported via transparent proxying (OPC).

Because ClickHouse does not ship an official Go query parser, Trickster includes its own custom SQL parser and lexer to deconstruct incoming ClickHouse queries, determine if they are cacheable and, if so, what elements are factored into the cache key derivation. Trickster also determines the requested time range and step based on the provided absolute values, in order to normalize the query before hashing the cache key.

If you find query or response structures that are not yet supported, or providing inconsistent or unexpected results, we'd love for you to report those. We also always welcome any contributions around this functionality.

To constitute a cacheable query, the first column expression in the main or any subquery must be in one of two specific forms in order to determine the timestamp column and step:

#### Grafana Plugin Format 
```sql
SELECT intDiv(toUInt32(time_col, 60) * 60) [* 1000] [as] [alias]
```
This is the approach used by the Grafana plugin.  The time_col and/or alias is used to determine the requested time range from the WHERE or PREWHERE clause of the query.  The argument to the ClickHouse intDiv function is the step value in seconds, since the toUInt32 function on a datetime column returns the Unix epoch seconds.

#### ClickHouse Time Grouping Function
```sql
SELECT toStartOf[Period](time_col) [as] [alias]
```
This is the approach that uses the following optimized ClickHouse functions to group timeseries queries:
```
toStartOfMinute
toStartOfFiveMinute
toStartOfTenMinutes
toStartOfFifteenMinutes
toStartOfHour
toDate
```
Again the time_col and/or alias is used to determine the request time range from the WHERE or PREWHERE clause, and the step is derived from the function name.

#### Determining the requested time range

Once the time column (or alias) and step are derived, Trickster parses each WHERE or PREWHERE clause to find comparison operations 
that mark the requested time range.  To be cacheable, the WHERE clause must contain either a `[timecol|alias] BETWEEN` phrase or 
a `[time_col|alias] >[=]` phrase.  The BETWEEN or >= arguments must be a parsable ClickHouse string date in the form `2006-01-02 15:04:05`, 
a ten digit integer representing epoch seconds, or the `now()` ClickHouse function with optional subtraction.

If a `>` phrase is used, a similar `<` phrase can be used to specify the end of the time period.  If none is found, Trickster will still cache results up to
the current time, but future queries must also have no end time phrase, or Trickster will be unable to find the correct cache key.

Examples of cacheable time range WHERE clauses:
```sql
WHERE t >= "2020-10-15 00:00:00" and t <= "2020-10-16 12:00:00"
WHERE t >= "2020-10-15 12:00:00" and t < now() - 60 * 60
WHERE datetime BETWEEN 1574686300 AND 1574689900
```

Note that these values can be wrapped in the ClickHouse toDateTime function, but ClickHouse will make that conversion implicitly and it is not required.   All string times are assumed to be UTC.

### Non-Time-Series Queries

Queries that are not cacheable as time series — such as `LIMIT`-based queries, `SELECT 1` health checks, or SDK handshake requests — are transparently proxied to the upstream ClickHouse server. These requests are cached using the Object Proxy Cache (OPC) with per-query cache keys derived from the `query` and `database` URL parameters, ensuring that different SQL statements receive distinct cache entries.

### Health and Ping Endpoint

Trickster exposes a `/ping` endpoint that returns a health check response, matching the endpoint provided by ClickHouse itself. This enables compatibility with clients and SDKs that probe `/ping` during connection initialization.

### Normalization and "Fast Forwarding"

Trickster will always normalize the calculated time range to fit the step size, so small variations in the time range will still result in actual queries for
the entire time "bucket".  In addition, Trickster will not cache the results for the portion of the query that is still active -- i.e., within the current bucket
or within the configured backfill tolerance setting (whichever is greater) 
