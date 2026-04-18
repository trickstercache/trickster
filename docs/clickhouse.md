# ClickHouse Support

Trickster will accelerate ClickHouse queries that return time series data normally visualized on a dashboard. Acceleration works by using the Time Series Delta Proxy Cache to minimize the number and time range of queries to the upstream ClickHouse server.

## Scope of Support

Trickster is tested with the [ClickHouse DataSource Plugin for Grafana](https://grafana.com/grafana/plugins/vertamedia-clickhouse-datasource) v1.9.3 by Vertamedia, and supports acceleration of queries constructed by this plugin using the plugin's built-in `$timeSeries` macro. Trickster also supports the official [Grafana ClickHouse plugin](https://grafana.com/grafana/plugins/grafana-clickhouse-datasource/) (v4+), including `toDateTime64()` in WHERE clauses. Trickster also supports several other query formats that return "time series like" data.

Trickster also supports the ClickHouse Go SDK (`clickhouse-go/v2`). Queries made through the SDK's HTTP protocol — including those using `clickhouse.OpenDB` — are proxied and cached through Trickster.

### Native Binary Protocol Support

Trickster supports the ClickHouse native binary protocol (port 9000) in two configurations:

**Flow 1 — Native Protocol Listener:** Trickster can accept native protocol connections directly via a protocol listener. Clients connect using the native binary protocol, and Trickster proxies queries through its caching engine. Configure a protocol listener in the `frontend` section:

```yaml
frontend:
  protocol_listeners:
    - name: clickhouse-native
      protocol: clickhouse-native
      listen_port: 9000
      backend: click1
```

**Flow 2 — Native Upstream:** Trickster can speak native protocol to ClickHouse upstream while accepting HTTP from clients. Set `protocol: native` on the backend:

```yaml
backends:
  click1:
    provider: clickhouse
    origin_url: 'http://127.0.0.1:9000'
    protocol: native
```

The native protocol implementation supports SELECT queries, ping/pong, handshake with addendum, and LZ4 block compression. INSERT operations over the native protocol are proxied as SQL but inline data blocks are not yet supported.

Supported native protocol data types: all integer types (8–256 bit), Float32/64, String, FixedString(N), DateTime, DateTime64, Date, Date32, UUID, IPv4, IPv6, Enum8/16, Bool, Nullable(T), Array(T), Map(K,V), Tuple(T1,T2,...), LowCardinality(T), and Decimal.

**Native Format in DPC:** When a client sends `default_format=Native` (as the official Grafana ClickHouse plugin does), Trickster's DPC automatically requests Native format from ClickHouse, deserializes it into the internal DataSet representation for caching and delta merging, and returns Native binary to the client. This is detected via the `X-ClickHouse-Format` response header — no configuration needed.

Because ClickHouse does not ship an official Go query parser, Trickster includes its own custom SQL parser and lexer to deconstruct incoming ClickHouse queries, determine if they are cacheable and, if so, what elements are factored into the cache key derivation. Trickster also determines the requested time range and step based on the provided absolute values, in order to normalize the query before hashing the cache key.

If you find query or response structures that are not yet supported, or providing inconsistent or unexpected results, we'd love for you to report those. We also always welcome any contributions around this functionality.

To constitute a cacheable query, the first column expression in the main or any subquery must be in one of two specific forms in order to determine the timestamp column and step:

#### Grafana Plugin Format 
```sql
SELECT intDiv(toUInt32(time_col, 60) * 60) [* 1000] [as] [alias]
```
This is the approach used by the Grafana plugin.  The time_col and/or alias is used to determine the requested time range from the WHERE or PREWHERE clause of the query.  The argument to the ClickHouse intDiv function is the step value in seconds, since the toUInt32 function on a datetime column returns the Unix epoch seconds.

#### ClickHouse Time Grouping Functions
```sql
SELECT toStartOf[Period](time_col) [as] [alias]
```
Supported `toStartOf` functions:
```
toStartOfNanosecond     toStartOfMicrosecond    toStartOfMillisecond
toStartOfSecond         toStartOfMinute         toStartOfFiveMinute
toStartOfTenMinutes     toStartOfFifteenMinutes toStartOfHour
toStartOfDay            toStartOfWeek           toStartOfMonth
toStartOfQuarter        toStartOfYear           toMonday
```

#### Custom Interval Grouping
```sql
SELECT toStartOfInterval(time_col, INTERVAL N unit) [as] [alias]
```
Supported units: `second`, `minute`, `hour`, `day`, `week`, `month`, `quarter`, `year`, `millisecond`, `microsecond`, `nanosecond`.

#### SQL-Standard date_trunc
```sql
SELECT date_trunc('unit', time_col) [as] [alias]
```
Supported units: `second`, `minute`, `hour`, `day`, `week`, `month`, `quarter`, `year`, `millisecond`.

#### timeSlot
```sql
SELECT timeSlot(time_col) [as] [alias]
```
Rounds to 30-minute boundaries.

The time_col and/or alias is used to determine the request time range from the WHERE or PREWHERE clause, and the step is derived from the function name or interval.

#### Determining the requested time range

Once the time column (or alias) and step are derived, Trickster parses each WHERE or PREWHERE clause to find comparison operations 
that mark the requested time range.  To be cacheable, the WHERE clause must contain either a `[timecol|alias] BETWEEN` phrase or 
a `[time_col|alias] >[=]` phrase.  The BETWEEN or >= arguments must be a parsable ClickHouse string date in the form `2006-01-02 15:04:05`, 
a ten digit integer representing epoch seconds, or the `now()` / `now64()` ClickHouse functions with optional subtraction.

If a `>` phrase is used, a similar `<` phrase can be used to specify the end of the time period.  If none is found, Trickster will still cache results up to
the current time, but future queries must also have no end time phrase, or Trickster will be unable to find the correct cache key.

Examples of cacheable time range WHERE clauses:
```sql
WHERE t >= '2020-10-15 00:00:00' AND t <= '2020-10-16 12:00:00'
WHERE t >= '2020-10-15 12:00:00' AND t < now() - 60 * 60
WHERE datetime BETWEEN 1574686300 AND 1574689900
WHERE datetime >= toDateTime64(1589904000, 3) AND datetime <= toDateTime64(1589997600, 3)
```

These values can be wrapped in `toDateTime()` or `toDateTime64()` (including with a precision argument). All string times are assumed to be UTC.

Queries using `toStartOfMonth`, `toStartOfQuarter`, or `toStartOfYear` (which return Date rather than DateTime) automatically wrap time range boundaries in `toDate()` during interpolation.

### Non-Time-Series Queries

Queries that are not cacheable as time series — such as `LIMIT`-based queries, `SELECT 1` health checks, or SDK handshake requests — are transparently proxied to the upstream ClickHouse server. These requests are cached using the Object Proxy Cache (OPC) with per-query cache keys derived from the `query` and `database` URL parameters, ensuring that different SQL statements receive distinct cache entries.

When a query falls back from DPC to OPC, Trickster logs a warning at the `warn` level to help identify queries that aren't getting time series acceleration. This can be suppressed per-backend:

```yaml
backends:
  click1:
    provider: clickhouse
    dpc_fallback_warning: false
```

### Health and Ping Endpoint

Trickster exposes a `/ping` endpoint that returns a health check response, matching the endpoint provided by ClickHouse itself. This enables compatibility with clients and SDKs that probe `/ping` during connection initialization.

### Normalization and "Fast Forwarding"

Trickster will always normalize the calculated time range to fit the step size, so small variations in the time range will still result in actual queries for
the entire time "bucket".  In addition, Trickster will not cache the results for the portion of the query that is still active -- i.e., within the current bucket
or within the configured backfill tolerance setting (whichever is greater) 
