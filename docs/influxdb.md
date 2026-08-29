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
- `GET/POST /api/v3/query_influxql` — InfluxQL queries with delta-proxy caching
- `POST /api/v3/write_lp` — line protocol writes (proxied, not cached)

All other v3 API paths (including the `/api/v3/configure/*` management endpoints, with any HTTP method) are proxied through untouched.

Query requests on both query endpoints may arrive as URL parameters (GET), an `application/json` document (`{"q": ..., "db": ..., "format": ..., "params": ...}`), a form-encoded body, or a raw statement body — all four shapes are parsed and cached equivalently, and the `db` and `params` values participate in the cache identity.

### InfluxQL over v3

`/api/v3/query_influxql` requests use the same delta-proxy caching as the v1 `/query` endpoint (queries with a `GROUP BY time(...)` interval and a time-bounded `WHERE` clause), but speak the v3 request/response shapes: the v3 request document above and the v3 tabular response formats, including the `iox::measurement` column, which Trickster treats as a series tag alongside any `GROUP BY` tags.

### SQL Query Caching

SQL queries using `date_bin()` or `date_trunc()` for time binning are parsed for time range extraction and step detection, enabling delta-proxy caching. Trickster extracts time ranges from `WHERE` clauses and intervals from `date_bin(INTERVAL '...', time)` or `date_trunc('unit', time)` in `SELECT`. Live, unaligned time ranges (the shape dashboard tools emit) are rounded inward to complete buckets. Grouped queries (`GROUP BY 1, host`) are cached per tag series.

Example query that Trickster will accelerate:

```sql
SELECT date_bin(INTERVAL '1 hour', time) AS time, avg(temperature)
FROM weather
WHERE time >= '2024-01-01 00:00:00' AND time < '2024-01-02 00:00:00'
GROUP BY 1
```

SELECT queries that cannot be delta-cached (no fixed-cadence time bucket, joins, subqueries, window functions, compound selects such as `UNION`, `LIMIT`, variable-length buckets like `'1 month'`, or unsafe time predicates) fall back to the object proxy cache, which caches the whole response briefly and passes results through unchanged. Non-SELECT statements and parameterized queries (a `params` field in the request) are proxied to the origin without delta caching.

Queries without an upper time bound run to the present; for these, Trickster's backfill tolerance is floored at one bucket so the still-filling final bucket is always refreshed from the origin rather than cached as complete.

### Response Formats

Trickster supports the following v3 response formats, controlled by the `format` query parameter (in the URL or the request document) or, when no `format` is given, the `Accept` header (`application/json`, `application/jsonl`, `text/csv`, ...):

- `json` (default) — JSON array of objects
- `jsonl` — JSON Lines (one JSON object per line)
- `csv` — standard CSV with header row

The `parquet` and `pretty` formats are not supported for caching and will be proxied through.

### v1/v2 Compatibility

InfluxDB 3.x ships with v1 and v2 compatibility endpoints. Trickster's existing InfluxQL support works against these endpoints with no additional configuration — just point Trickster at the v3 instance and query via `/query`.

### Flight SQL (gRPC)

InfluxDB 3.x exposes SQL via Apache Arrow Flight SQL on gRPC in addition to HTTP. Grafana's InfluxDB datasource in SQL mode, the Python/Rust/Java SDKs, and ADBC all default to Flight SQL, so HTTP-only caching misses a significant fraction of real-world query traffic.

Trickster can expose a Flight SQL server that proxies to an upstream Flight SQL endpoint and caches the Arrow IPC byte stream. Enable it by defining a listener with the `flight-sql` protocol and mapping exactly one InfluxDB backend to it (alongside its usual HTTP listener):

```yaml
listeners:
  influx3-flight:
    protocol: flight-sql   # Apache Arrow Flight SQL over gRPC
    port: 8485
backends:
  influx3:
    provider: influxdb
    origin_url: 'http://influxdb3:8181/'
    listener_names: [default, influx3-flight]
    influxdb:
      flight_upstream_address: 'influxdb3:8181'   # optional, defaults to origin_url host
```

The `authorization` and `database` headers are forwarded from the client through to the upstream, and every cache entry is keyed by the backend name, the `database`/`bucket-name` headers, and a hash of the `authorization` header — so tenants and databases never share cache entries. Requests that send no `authorization` header share one anonymous cache scope, mirroring the access the upstream would grant them.

Statement queries are served through a three-tier cache:

1. **Delta proxy cache** — queries the SQL analyzer classifies as delta-cacheable (the same `date_bin()`/`date_trunc()` shapes as the HTTP path) are cached by time extent: repeat and overlapping queries fetch only the missing sub-ranges from the upstream, and responses are rebuilt into Arrow record batches conforming to the response's original schema. Entries use the backend's `timeseries_ttl` and honor `backfill_tolerance`; still-filling buckets are always refetched. Responses whose Arrow schemas the delta model cannot represent (nested types, non-string dictionaries, ...) automatically fall to the next tier.
2. **Object cache** — everything else cacheable is stored as the verbatim Arrow IPC byte stream, returned byte-identically, with a lifetime of `influxdb.flight_cache_ttl` (default 60s). Metadata RPCs and prepared statements always use this tier.
3. **Proxy** — statements referencing nondeterministic functions (`now()`, `current_timestamp`, `random()`, ...) and non-SELECT statements are never cached.

#### Flight SQL TLS

The Flight listener serves TLS when its mapped backend's `tls` block presents a certificate and key (the same mechanism HTTP listeners use); certificate rotation applies on config reload without dropping the listener. Without a certificate the listener serves plaintext gRPC. The upstream dial uses TLS when `influxdb.flight_upstream_tls` is set, honoring the backend `tls` block's `insecure_skip_verify`.

#### Unsupported Flight SQL RPCs

Trickster proxies queries, the metadata RPCs below, and the prepared-statement lifecycle. Other Flight SQL RPCs — `GetSchema`, writes/updates/ingest (`ExecuteUpdate`, `DoPut` ingestion), transactions, savepoints, query cancellation, and session options — return gRPC `Unimplemented`; clients requiring them should connect to InfluxDB directly.

Flight SQL listeners share Trickster's standard listener lifecycle: connection limits (`connections_limit`), graceful drain on SIGTERM (active streams are drained until the configured drain timeout, then closed), and config reload (SIGHUP) — a reload with an unchanged backend configuration keeps serving on the existing socket, while a changed configuration drains the old server and rebinds.

#### Metadata RPCs

ADBC clients (Grafana's SQL datasource, the Python `adbc_driver_flightsql`, etc.) probe the following RPCs on connect to populate schema browsers and query editors. Trickster proxies these to the upstream and caches the Arrow IPC response:

- `GetFlightInfoTables` / `DoGetTables`
- `GetFlightInfoCatalogs` / `DoGetCatalogs`
- `GetFlightInfoSchemas` / `DoGetDBSchemas`
- `GetFlightInfoTableTypes` / `DoGetTableTypes`
- `GetFlightInfoSqlInfo` / `DoGetSqlInfo`

#### Prepared Statements

Trickster proxies the full prepared statement lifecycle: `CreatePreparedStatement`, `DoPutPreparedStatementQuery` (parameter binding), `DoGetPreparedStatement`, `ClosePreparedStatement`. Cache keys incorporate the bound parameter hash so two clients running the same statement with different values don't alias. Prepared statements abandoned by disconnected clients are closed upstream after 15 minutes of inactivity.

Note: InfluxDB 3 Core 3.10 reports a parameter schema at prepare time but does not resolve bound values during query planning (upstream limitation). Parameterless prepared statements work end-to-end; parameterized ones require a newer Core or Enterprise build.

## Flux Language Support

Trickster supports the Flux Query Language for general/basic usage with InfluxDB 1.x and 2.x. Flux is not supported in InfluxDB 3.x.

The delta-proxy cache accepts `now()` as a `range()` bound and handles queries with `aggregateWindow(every: ...)` -- the common Grafana shape. Multi-table Flux CSV responses (one table per series in the result set) are also read correctly.

Trickster does not support advanced union-style queries (e.g., with multiple `from` clauses). In this rare use case, these responses will currently provide invalid data, however, a subsequent beta will proxy unsupported requests.

Trickster currently does not properly handle schema changes within a response CSV body (e.g., multiple CSVs in the same document with their own #annotation and header rows). We will fully support this use case in a future beta.

## Max Query Range Limitation

Trickster supports enforcing a `max_query_range` limit on InfluxDB backends. For details on how to configure and use query range limits, see the [Query Range Limits](./query-range-limits.md) documentation.
