# InfluxDB Support

Trickster provides support for accelerating InfluxDB queries that return time series data normally visualized on a dashboard. Acceleration works by using the Time Series Delta Proxy Cache to minimize the number and time range of queries to the upstream InfluxDB server.

## Scope of Support

Trickster is tested with the built-in [InfluxDB DataSource Plugin for Grafana](https://grafana.com/grafana/plugins/influxdb) v5.0.0.

Trickster uses InfluxDB-provided packages to parse and normalize queries for caching and acceleration. If you find query or response structures that are not yet supported, or providing inconsistent or unexpected results, we'd love for you to report those so we can further improve our InfluxDB support.

Trickster supports integrations with InfluxDB 1.x, 2.x, and 3.x.

## InfluxDB 3.x Support

Trickster supports InfluxDB 3.x via both the native v3 API endpoints and the v1/v2 compatibility endpoints.

### Supported v3 Endpoints

- `GET/POST /api/v3/query_sql` — SQL queries with delta-proxy caching
- `GET/POST /api/v3/query_influxql` — InfluxQL queries via the native v3 API
- `POST /api/v3/write_lp` — line protocol writes (proxied, not cached)

### SQL Query Caching

SQL queries using `date_bin()` for time binning are parsed for time range extraction and step detection, enabling delta-proxy caching. Trickster extracts time ranges from `WHERE` clauses and intervals from `date_bin(INTERVAL '...', time)` in `SELECT`.

Example query that Trickster will accelerate:

```sql
SELECT date_bin(INTERVAL '1 hour', time) AS time, avg(temperature)
FROM weather
WHERE time >= '2024-01-01 00:00:00' AND time < '2024-01-02 00:00:00'
GROUP BY 1
```

Non-SELECT queries and queries without parseable time ranges are proxied without caching.

### Response Formats

Trickster supports the following v3 response formats, controlled by the `format` query parameter:

- `json` (default) — JSON array of objects
- `jsonl` — JSON Lines (one JSON object per line)
- `csv` — standard CSV with header row

The `parquet` and `pretty` formats are not supported for caching and will be proxied through.

### v1/v2 Compatibility

InfluxDB 3.x ships with v1 and v2 compatibility endpoints. Trickster's existing InfluxQL support works against these endpoints with no additional configuration — just point Trickster at the v3 instance and query via `/query`.

### Flight SQL (gRPC)

InfluxDB 3.x exposes SQL via Apache Arrow Flight SQL on gRPC in addition to HTTP. Grafana's InfluxDB datasource in SQL mode, the Python/Rust/Java SDKs, and ADBC all default to Flight SQL, so HTTP-only caching misses a significant fraction of real-world query traffic.

Trickster can expose a Flight SQL server that proxies to an upstream Flight SQL endpoint and caches the Arrow IPC byte stream. Enable it per-backend:

```yaml
backends:
  influx3:
    provider: influxdb
    origin_url: 'http://influxdb3:8181/'
    flight_port: 8485                           # listen on gRPC :8485
    flight_upstream_address: 'influxdb3:8181'   # optional, defaults to origin_url host
```

The `authorization` and `database` headers are forwarded from the client through to the upstream. Queries are cached keyed by the full SQL statement; re-running the same query returns the cached Arrow response.

Flight SQL listeners participate in graceful shutdown: on SIGTERM the daemon calls `GracefulStop` on each registered gRPC server with a 5-second drain before forcing closure. Config reload (SIGHUP) replaces the listener in-place — the old server is drained and the new one binds the same port.

#### Metadata RPCs

ADBC clients (Grafana's SQL datasource, the Python `adbc_driver_flightsql`, etc.) probe the following RPCs on connect to populate schema browsers and query editors. Trickster proxies these to the upstream and caches the Arrow IPC response:

- `GetFlightInfoTables` / `DoGetTables`
- `GetFlightInfoCatalogs` / `DoGetCatalogs`
- `GetFlightInfoSchemas` / `DoGetDBSchemas`
- `GetFlightInfoTableTypes` / `DoGetTableTypes`
- `GetFlightInfoSqlInfo` / `DoGetSqlInfo`

#### Prepared Statements

Trickster proxies the full prepared statement lifecycle: `CreatePreparedStatement`, `DoPutPreparedStatementQuery` (parameter binding), `DoGetPreparedStatement`, `ClosePreparedStatement`. Cache keys incorporate the bound parameter hash so two clients running the same statement with different values don't alias.

Note: InfluxDB 3 Core 3.10 reports a parameter schema at prepare time but does not resolve bound values during query planning (upstream limitation). Parameterless prepared statements work end-to-end; parameterized ones require a newer Core or Enterprise build.

## Flux Language Support

Trickster supports the Flux Query Language for general/basic usage with InfluxDB 1.x and 2.x. Flux is not supported in InfluxDB 3.x.

The delta-proxy cache accepts `now()` as a `range()` bound and handles queries with `aggregateWindow(every: ...)` -- the common Grafana shape. Multi-table Flux CSV responses (one table per series in the result set) are also read correctly.

Trickster does not support advanced union-style queries (e.g., with multiple `from` clauses). In this rare use case, these responses will currently provide invalid data, however, a subsequent beta will proxy unsupported requests.
