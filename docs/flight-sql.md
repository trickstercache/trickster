# Apache Arrow Flight SQL Listeners

Trickster can serve [Apache Arrow Flight SQL](https://arrow.apache.org/docs/format/FlightSql.html)
— the gRPC-based query protocol spoken by ADBC drivers, Grafana's SQL-mode
datasources, and the Arrow-native client SDKs — as a caching proxy in front of
a Flight SQL-capable origin. The protocol support is vendor-neutral; each
backend provider that adopts it supplies only its origin-specific wiring.
InfluxDB 3.x is the first supported provider (see
[InfluxDB Support](./influxdb.md) for its specifics and examples).

## Enabling a Flight SQL listener

Define a listener with the `flight-sql` protocol and map exactly one
compatible backend to it through the backend's `listener_names`:

```yaml
listeners:
  my-flight:
    protocol: flight-sql   # Apache Arrow Flight SQL over gRPC
    port: 8485
backends:
  influx3:
    provider: influxdb
    origin_url: 'http://influxdb3:8181/'
    listener_names: [ default, my-flight ]
```

Flight SQL listeners share Trickster's standard listener lifecycle:
connection limits (`connections_limit`), graceful drain on SIGTERM, and
config reload (SIGHUP) — a reload with an unchanged backend configuration
keeps serving on the existing socket, while a changed configuration drains
the old server (closing its upstream connection) and rebinds.

### TLS

The listener serves TLS when the mapped backend's `tls` block presents a
certificate and key, with in-place certificate rotation on config reload;
without one it serves plaintext gRPC. The upstream dial is TLS-capable via
provider options (for InfluxDB, `influxdb.flight_upstream_tls`).

## Caching model

Every cache entry is scoped to the backend name plus a per-tenant namespace
derived from the request metadata the provider declares (for InfluxDB:
`database`, `bucket-name`, and a hash of `authorization`), mirroring the
scope the upstream itself grants — tenants never share entries, and
credentials never appear in cache keys.

Statement queries are served through three tiers:

1. **Delta proxy cache** — statements the provider's SQL analyzer classifies
   as delta-cacheable are cached by time extent: repeat and overlapping
   queries fetch only missing sub-ranges from the origin, and responses are
   rebuilt into Arrow record batches conforming to the response's original
   schema (types, column order, nullability, and schema metadata are
   preserved). Responses whose schemas the delta model cannot represent fall
   automatically to the next tier.
2. **Object cache** — everything else cacheable is stored as the verbatim
   Arrow IPC byte stream and returned byte-identically, with a short,
   provider-configured lifetime. Metadata RPCs use this tier.
3. **Proxy** — nondeterministic and non-SELECT statements are never cached.

Serving-cost characteristics of the tiers are recorded in
[Flight SQL Cache Tier Benchmarks](./developer/flightsql-benchmarks.md).

## Supported RPC surface

- Statement execution (`GetFlightInfoStatement` / `DoGetStatement`).
- Result-set schema requests (`GetSchemaStatement` /
  `GetSchemaPreparedStatement`), which ADBC drivers probe on connect. When
  the origin itself does not implement its schema RPC (InfluxDB 3 Core among
  them), the schema is derived by executing the statement through the object
  cache, so the probe's cost folds into the execution the client performs
  next.
- Catalog metadata (`GetTables`, `GetCatalogs`, `GetDbSchemas`,
  `GetTableTypes`, `GetSqlInfo`), proxied and cached per tenant.
- The prepared-statement lifecycle (create, bind, execute, close). A
  parameterless prepared statement is served through the same three statement
  tiers — sharing cache entries with ad-hoc executions of the same text —
  while parameterized executions are cached whole, keyed by the bound
  parameter hash. Statements abandoned by disconnected clients are closed
  upstream after 15 minutes of inactivity.

Writes/updates/ingest, transactions, savepoints, query cancellation, and
session options return gRPC `Unimplemented`; clients requiring them should
connect to the origin directly.

## Metrics

Statement executions record cache outcomes to the native SQL metrics under
the `flightsql` dialect label: `trickster_sql_query_cache_total`
(`cache_mode` x `cache_status`), the standard proxy request/points/duration
metrics, `trickster_sql_query_rewrite_failures_total` for extent-rendering
failures, and cache-error events on the shared cache metrics. See
[Metrics](./metrics.md).

## Architecture (for provider implementers)

The protocol lives in vendor-neutral packages:

- `pkg/proxy/flightsql` — the gRPC protocol server, caching statement/
  metadata/prepared handling, and the upstream client. A provider supplies:
  the upstream address and TLS posture; `ForwardMetadataKeys` (which inbound
  gRPC metadata flows to the origin); a `KeyScoper` covering every forwarded
  key that affects results; and optionally a `DeltaConfig` carrying its SQL
  dialect analyzer to enable the delta tier.
- `pkg/proxy/engines/nativedelta` — the transport-agnostic delta-proxy-cache
  engine (shared with the MySQL native listener), driven by the frozen
  `pkg/parsing/sqlanalyzer` dialect contract.
- `pkg/timeseries/dataset/arrow` — dataset ⇄ Arrow record batch conversion
  used by the delta tier.

`pkg/backends/influxdb/native_listener.go` is the reference wiring: a
provider integrates by implementing the `native.Adapter` interface
(declaring `Protocol() == "flight-sql"` and its own `BackendProvider()`) and
constructing the flightsql server from its backend options.
