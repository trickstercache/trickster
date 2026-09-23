# GreptimeDB Provider

Use `provider: greptimedb` for GreptimeDB's HTTP, PostgreSQL and MySQL query
surfaces. One backend may be mapped to all three listener protocols. The
provider reuses the SQL and Prometheus cache engines, with GreptimeDB-specific
parsing, session settings, result types and API paths.

## Surfaces

| Surface | Origin port | Behavior |
| --- | --- | --- |
| HTTP `/v1/sql` | 4000 | Eligible SELECT results use delta or object caching |
| PostgreSQL wire | 4003 | Eligible simple queries use delta or object caching; extended queries are relayed |
| MySQL wire | 4002 | Eligible text queries use delta or object caching |
| `/v1/prometheus/api/v1/query_range` | 4000 | Prometheus range caching and supported time-series merges |
| Prometheus instant and metadata APIs | 4000 | Object caching subject to HTTP cache policy |
| Ingest, TQL, `/v1/promql`, other HTTP APIs | 4000 | Proxied without SQL/PromQL delta rewriting |
| gRPC | 4001 | Not implemented by this provider |

Wire compatibility does not make GreptimeDB a PostgreSQL or MySQL server.
Use the `greptimedb` provider even on native listeners. For example, MySQL's
`SHOW COUNT(*) WARNINGS` is not a supported GreptimeDB statement, and its
PostgreSQL session settings have different semantics.

The developer environment pins an official nightly that includes
[GreptimeDB #9295](https://github.com/GreptimeTeam/greptimedb/pull/9295), the fix
for Grafana's comment-only PostgreSQL health query. See the
[developer environment](developer/environment/README.md#greptimedb-details)
for the exact image and reproducible checks. The tested Grafana plugin is
the bundled PostgreSQL datasource, not a GreptimeDB-specific plugin.

## Configuration

The [complete example](../examples/conf/greptimedb.yaml) exposes HTTP on 8480,
PostgreSQL on 8489 and MySQL on 8491. A minimal mixed backend is:

```yaml
listeners:
  greptime-pg:
    protocol: postgres
    port: 8489
  greptime-mysql:
    protocol: mysql
    port: 8491

authenticators:
  greptime-readers:
    provider: basic
    proxy_preserve: true
    users:
      grafana_ro: ${GRAFANA_RO_PASSWORD}

backends:
  greptime:
    provider: greptimedb
    origin_url: http://greptime.example:4000
    listener_names: [default, greptime-pg, greptime-mysql]
    authenticator_name: greptime-readers
    postgres:
      upstream_url: postgres://trickster_ro:REPLACE_ME@greptime.example:4003/public
    mysql:
      upstream_url: mysql://trickster_ro:REPLACE_ME@greptime.example:4002/public
```

HTTP uses `origin_url`; `postgres.upstream_url` and `mysql.upstream_url`
separately identify native endpoints and credentials. Never put the HTTP
port in either native URL. Without a native override, an HTTP origin's host
is reused with the engine's native default port, not its HTTP port or userinfo.
Explicit URLs are recommended. URL credentials do not expand environment
variables; authenticator user values do.

List only `default` for an HTTP-only backend, or just the chosen native
listener for a native-only backend. A mixed backend probes HTTP `/health`;
a native-only backend uses its native authenticated probe. Listener, origin,
credential or protocol changes restart and drain affected native listeners.

### Authentication And TLS

For HTTP, `proxy_preserve: true` forwards client credentials to GreptimeDB.
Authenticated object responses are not shared unless the origin explicitly
permits HTTP caching. SQL and PromQL cache identities retain their database,
request parameters, effective timezone and authentication partitioning.

For PostgreSQL, an authenticator terminates client authentication at
Trickster; the separate upstream URL supplies the origin role. Without an
authenticator, authentication is passed through to GreptimeDB. MySQL requires
a configured authenticator. Give every origin role only the permissions its
clients need; terminating authentication does not preserve separate upstream
roles for each client.

Native TLS and limits use the existing `postgres` and `mysql` option blocks;
see [PostgreSQL](postgres.md) and [MySQL](mysql.md). Support for an option in
Trickster is not a promise that a particular GreptimeDB deployment supports
the corresponding server feature. The loopback developer environment uses
plaintext and published test credentials and must not be exposed publicly.

## SQL Cache Classification

Delta caching requires one provably deterministic time-bucketed SELECT with
supported grouping, ordering and time bounds. Other supported reads can use
whole-result object caching. Writes, volatile queries and session-unsafe
statements bypass caches; a parser rejection never makes a write cacheable.

PostgreSQL supports fixed-width `date_bin` intervals or compact widths such
as `5m`, UTC fixed-width `date_trunc`, and epoch buckets of the form
`floor(extract(epoch FROM ts)/300)*300` or `date_part`. Calendar widths,
ambiguous time predicates, unknown timezone-dependent semantics, and
submicrosecond PostgreSQL text buckets do not get delta plans. Extended
protocol messages are always relayed.

HTTP SQL supports GET and form-encoded POST with the default `greptimedb_v1`
response format. A delta response retains typed schema, row ordering, NULLs
and exact integer values. Alternate formats, `limit`, unknown options and
unaligned bounds that would discard partial buckets fall back to the original
query. Rebuilt responses do not claim the origin's execution duration or
execution metrics.

MySQL supports `DATE_BIN('1m', ts, FROM_UNIXTIME(0))` and fixed-width
`DATE_TRUNC` buckets with verified UTC sessions, whole-second cadence and
aligned half-open bounds. Other bucket origins, subsecond cadence and partial
buckets use the original query instead. Timestamp results keep up to nine
fractional digits, text groups are compared case-sensitively, and NULL ordering
follows GreptimeDB. Unsupported or failed session changes conservatively
disable caching for that connection.

### Grafana Macros

Use the built-in PostgreSQL datasource with **TimescaleDB disabled** and a
minimum interval of one minute. Native epoch-second columns are integers,
not timestamps cast to integers (GreptimeDB timestamp casts can yield
nanoseconds).

| Macro family | GreptimeDB behavior |
| --- | --- |
| `$__time`, `$__timeEpoch` | Supported projection; not a bucket by itself |
| `$__timeFilter`, `$__timeFrom`, `$__timeTo` | Supported timestamp bounds |
| `$__timeGroup`, `$__timeGroupAlias` | Fixed epoch-floor buckets with TimescaleDB mode off |
| `$__unixEpochFilter`, `$__unixEpochFrom`, `$__unixEpochTo` | Numeric epoch-second bounds |
| `$__unixEpochGroup`, `$__unixEpochGroupAlias` | Fixed buckets over an epoch-second column |
| `$__unixEpochNanoFilter` | Supported SQL predicate; not an automatic delta plan |
| `$__interval` | Use a positive supported fixed duration |

The [compatibility corpus](../pkg/backends/greptimedb/testdata/compatibility/README.md)
records real Grafana expansions, including unaligned windows and conservative
fallbacks. Grafana's MySQL macros are not interchangeable: `UNIX_TIMESTAMP`
is unsupported in the tested GreptimeDB image. Use explicit GreptimeDB SQL
when connecting a MySQL client.

## Known Limits

- `RANGE ... ALIGN` remains whole-result caching; TQL is passthrough. There
  is no advanced RANGE/FILL dependency rewriting.
- PostgreSQL text timestamps carry microseconds even for a TIMESTAMP(9)
  column. MySQL and HTTP have separate precision contracts.
- Transaction commands accepted by GreptimeDB are compatibility stubs, not
  proof of transactional isolation. PostgreSQL cancellation is not a supported
  upstream capability in the tested image.
- `SET LOCAL` persists at session scope in GreptimeDB. Effective settings are
  probed and tracked; requested startup parameters alone are not proof of the
  resulting timezone. The development read-only account cannot change it.
- PostgreSQL session load balancing is unsupported. MySQL listeners use the
  existing native MySQL routing rules.
- PromQL `count_values` currently loses its grouping label upstream; merges
  reject that response rather than claim a correct aggregate.
- Ingestion, mutations, enterprise-only features and gRPC caching are outside
  this provider's accelerated contract.

Run the [acceptance suites](../integration/greptimedb/README.md) against an
isolated seeded environment. Inspect cache counters as well as results:
`trickster_sql_query_analysis_total`, `trickster_sql_query_cache_total`, and
`trickster_sql_query_rewrite_failures_total` distinguish actual cache hits,
fallbacks and failed rewrites. SQL dialect labels are `greptimedb`; transport
metrics retain the native protocol name.
