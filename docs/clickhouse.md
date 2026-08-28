# ClickHouse Support

Trickster will accelerate ClickHouse queries that return time series data normally visualized on a dashboard. Acceleration works by using the Time Series Delta Proxy Cache to minimize the number and time range of queries to the upstream ClickHouse server.

## Scope of Support


Trickster is tested with the 

Trickster is tested with the official [ClickHouse DataSource Plugin for Grafana](https://grafana.com/grafana/plugins/grafana-clickhouse-datasource/) v4.21.1 and supports acceleration of queries constructed by this plugin using its built-in time macros like `$__fromTime` and `$__toTime`. Delta caching with this an other plugins (e.g., Altinity/Vertamedia) depends on the supported query shapes below. Trickster also supports several other query formats that return "time series like" data.

Trickster also supports the ClickHouse Go SDK (`clickhouse-go/v2`) over its HTTP and Native protocols, including `clickhouse.OpenDB`.

### Native Binary Protocol Support

Inbound and upstream protocols are configured independently:

- A named listener with `protocol: clickhouse` accepts Native client connections.
- A ClickHouse backend with `protocol: native` uses the Native protocol for its origin. An empty protocol or `protocol: http` uses HTTP.

```yaml
listeners:
  default:
    protocol: http
    port: 8480
  clickhouse-native:
    protocol: clickhouse
    port: 8487

backends:
  click1:
    provider: clickhouse
    origin_url: http://clickhouse:8123
    listener_names: [default, clickhouse-native]
    cache_name: default
```

This exposes one HTTP route at `/click1/` and one Native listener on port 8487, backed by the same cache and HTTP ClickHouse origin. A Native listener must map to exactly one backend; ALB and user-router multiplexing are not supported.

To use a Native origin instead, set the backend protocol and point `origin_url` at ClickHouse's Native port:

```yaml
backends:
  click1:
    provider: clickhouse
    origin_url: http://clickhouse:9000
    protocol: native
```

All four HTTP/Native ingress and HTTP/Native origin combinations are supported. Native credentials, database, query settings, parameters, and query IDs are forwarded to the origin. Native identity pools are bounded to 64 credential/database combinations per backend.

#### TLS

Native listener TLS uses the backend's server certificate. Set `require_tls: true` and provide both certificate paths:

```yaml
listeners:
  clickhouse-native-tls:
    protocol: clickhouse
    port: 9441
    tls_watch_interval: 30s

backends:
  click1-tls:
    provider: clickhouse
    origin_url: http://clickhouse:8123
    listener_names: [clickhouse-native-tls]
    require_tls: true
    tls:
      full_chain_cert_path: /etc/trickster/tls/server.crt
      private_key_path: /etc/trickster/tls/server.key
```

Certificate changes are hot-swapped. Since `require_tls` applies to every binding of a backend, use a separate backend entry when plaintext and TLS listeners must coexist.

For a TLS Native origin, use an `https` origin URL with the Native protocol. The common backend TLS options configure origin verification and optional mutual TLS:

```yaml
backends:
  click1:
    provider: clickhouse
    origin_url: https://clickhouse:9440
    protocol: native
    tls:
      certificate_authority_paths: [/etc/trickster/tls/origin-ca.crt]
      client_cert_path: /etc/trickster/tls/client.crt
      client_key_path: /etc/trickster/tls/client.key
```

#### Native Limitations

The Native listener supports SELECT queries, ping/pong, revision 54460 framing, structured errors, and LZ4 compression. `INSERT`, external tables, and session-changing `USE` or `SET` statements are rejected; select a database and settings per request. `ClientCancel` is accepted as a no-op and does not asynchronously cancel an active upstream request.

Supported native protocol data types: all integer types (8–256 bit), Float32/64, String, FixedString(N), DateTime, DateTime64, Date, Date32, UUID, IPv4, IPv6, Enum8/16, Bool, Nullable(T), Array(T), Map(K,V), Tuple(T1,T2,...), LowCardinality(T), and Decimal.

Native wire clients receive Native blocks. HTTP clients using a Native origin may request JSON, Native, CSV, or TSV-family output; compound columns in CSV/TSV and unsupported formats return an error. The modeler recognizes Native origin responses through the `X-ClickHouse-Format` response header, and HTTP Native framing honors `client_protocol_version`.

Trickster parses incoming ClickHouse statements into a full abstract syntax tree using the [AfterShip ClickHouse SQL parser](https://github.com/AfterShip/clickhouse-sql-parser), then applies its own semantic analysis to determine whether a query is eligible for time series delta caching and, if so, its timestamp column, bucket cadence, time range, grouping tags, and cache identity. The cache key is derived from a canonical form of the query in which the requested time range is replaced with placeholders, so requests for different time ranges of the same logical series share one delta cache entry.

Trickster's analysis fails closed: a valid query whose shape cannot be proven safe for delta caching is never rewritten approximately. It is instead served through the Object Proxy Cache (OPC) or proxied directly, and the classification reason is exported through the `trickster_sql_query_analysis_total` metric.

If you find query or response structures that are not yet supported, or providing inconsistent or unexpected results, we'd love for you to report those. We also always welcome any contributions around this functionality.

## Delta-Cacheable Queries

To be eligible for the delta cache, a query must be a single `SELECT` statement containing a recognized time-bucketing expression in its select list, a supported time range in its `WHERE` or `PREWHERE` clause, and a `GROUP BY` clause that includes the time bucket. Each requirement is described below.

### Time-Bucketing Expressions

Exactly one select-list expression must match a supported bucket form:

#### Grafana Plugin Format

```sql
SELECT intDiv(toUInt32(time_col), 60) * 60 [* 1000] [AS alias]
```

This is the approach used by the Grafana plugin. The argument to the ClickHouse `intDiv` function is the step value in seconds, since the `toUInt32` function on a datetime column returns the Unix epoch seconds. An optional output multiplier of 1000, 1000000, or 1000000000 selects millisecond, microsecond, or nanosecond output timestamps.

#### ClickHouse Time Grouping Functions

```sql
SELECT toStartOfInterval(time_col, INTERVAL n unit) [AS alias]
```

with a positive constant `n` and a unit of `millisecond`, `second`, `minute`, `hour`, `day`, or `week`; or one of the fixed-period functions:

```text
toStartOfNanosecond
toStartOfMicrosecond
toStartOfMillisecond
toStartOfSecond
toStartOfMinute
toStartOfFiveMinute
toStartOfTenMinutes
toStartOfFifteenMinutes
timeSlot
toStartOfHour
toStartOfDay
toStartOfWeek
toMonday
```

`date_trunc('unit', time_col)` and `dateTrunc` support the same fixed units. `toStartOfWeek` uses ClickHouse's Sunday phase while `toMonday` and weekly `date_trunc` use Monday. Calendar-length month, quarter, and year buckets are served through the OPC.

The time column may be a plain, qualified (`table.col`), or quoted identifier, optionally wrapped in `toDateTime`, `toInt32`, or `toUInt32`. Integer constants defined in a scalar `WITH` clause may be used for the step value. Timezone-parameter variants of these functions (for example `toStartOfHour(time_col, 'America/Denver')`) are not eligible for delta caching and are served through the OPC. The current SQL parser does not accept `INTERVAL MICROSECOND` or `INTERVAL NANOSECOND`; use the corresponding fixed `toStartOf...` function.

### Determining the Requested Time Range

Time range predicates must appear in a top-level `AND` conjunction of the `WHERE` or `PREWHERE` clause. Predicates joined by `OR` or negated with `NOT` make the query ineligible for delta caching.

Two predicate targets are supported, with different rules:

- **The raw time column** (the column inside the bucket function): the lower bound must be inclusive (`>=`) and the upper bound exclusive (`<`), and both values must fall exactly on bucket boundaries. Other comparators — including `BETWEEN` — describe partial buckets whose aggregates cannot be safely cached, so those queries are served through the OPC.
- **The bucket alias** (the output of the bucket expression): `>`, `>=`, `<`, `<=`, and `BETWEEN` are all supported, because bucket outputs are discrete; Trickster normalizes each comparator to the first and last included bucket.

Bound values may be expressed as epoch integers, ClickHouse string dates in the form `2006-01-02 15:04:05` (or date-only, or RFC3339), `toDateTime(n)`, `toDateTime64(n, precision)`, or `toDate(n)` wrappers, `WITH`-clause constants, or `now()`/`now64()` with optional addition or subtraction of seconds. DateTime64 precision is retained. Floating epoch bounds and timezone-qualified conversions such as `toDateTime(n, 'America/Denver')` are not eligible.

If no upper bound is present, Trickster caches results up to the current time and inserts a safe upper bound into origin requests automatically.

Examples of delta-cacheable time range clauses (for a one-minute bucket cadence):

```sql
WHERE t >= '2020-10-15 00:00:00' AND t <= '2020-10-16 12:00:00'  -- bucket alias
WHERE t BETWEEN 1574686320 AND 1574689920                        -- bucket alias
WHERE time_col >= toDateTime(1574686320) AND time_col < toDateTime(1574689920)
WHERE t >= now() - 3600 AND t < now()                            -- bucket alias
```

Secondary date-range predicates whose values match the primary range — such as the `Date`-typed partition filters emitted by the Grafana plugin — are recognized and rewritten in step with the primary range.

### Grouping and Result Shape

The `GROUP BY` clause must include the time bucket (by alias or by its full expression), and every non-aggregate column in the select list must also be grouped. Grouped columns become the series tags in the cached time series. Queries using `GROUP BY ... WITH CUBE/ROLLUP`, grouping on expressions that are not selected, or leaving a selected dimension ungrouped are served through the OPC.

### Output Formats

Delta-cacheable queries may specify `FORMAT JSON`, `CSV`, `CSVWithNames`, `TabSeparated` (`TSV`), `TabSeparatedWithNames`, or `TabSeparatedWithNamesAndTypes`, or omit the `FORMAT` clause. Trickster requests `TSVWithNamesAndTypes` from the origin and re-marshals cached data into the client's requested format.

### Non-Time-Series Queries

Queries that are not cacheable as time series — such as `LIMIT`-based queries, queries with set operations (`UNION`, `EXCEPT`, `INTERSECT`), `SELECT 1` health checks, or SDK handshake requests — are transparently proxied to the upstream ClickHouse server. These requests are cached using the Object Proxy Cache (OPC) with per-query cache keys derived from the `query` and `database` URL parameters, ensuring that different SQL statements receive distinct cache entries.

### Health and Ping Endpoint

Trickster exposes a `/ping` endpoint that returns a health check response, matching the endpoint provided by ClickHouse itself. This enables compatibility with clients and SDKs that probe `/ping` during connection initialization.

### Normalization and "Fast Forwarding"

Trickster will always normalize the calculated time range to fit the step size, so small variations in the time range will still result in actual queries for the entire time "bucket". In addition, Trickster will not cache the results for the portion of the query that is still active -- i.e., within the current bucket or within the configured backfill tolerance setting (whichever is greater).

Per-query behavior can be adjusted with comment directives such as `trickster-backfill-tolerance`; see [Per-Query Instructions](./per-query-instructions.md).

## Observability

Query classification outcomes are exported through two low-cardinality metrics that never include query text:

- `trickster_sql_query_analysis_total` — labeled by backend, dialect, cache mode (`delta`, `object`, `none`), and a stable reason code such as `delta_cacheable`, `unsafe_predicate`, or `unsupported_bucket`.
- `trickster_sql_query_rewrite_failures_total` — counts failures to render an origin request from a cached query plan.

With debug logging enabled, classification decisions are also logged with the same structured reason codes.

## Max Query Range Limitation

Trickster supports enforcing a `max_query_range` limit on ClickHouse backends. For details on how to configure and use query range limits, see the [Query Range Limits](./query-range-limits.md) documentation.
