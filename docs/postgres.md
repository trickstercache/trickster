# PostgreSQL and TimescaleDB Provider

Trickster can accept native PostgreSQL client connections, relay each session
to a PostgreSQL or TimescaleDB origin, and cache the results of eligible
queries. Dashboards that bucket rows by time are served from Trickster's delta
cache: a refresh fetches only the buckets the cache does not hold yet.

This guide describes the supported contract. Anything not listed here is
relayed to the origin without caching; cache eligibility always fails closed.

## Compatibility

- PostgreSQL 14 and later, with or without the TimescaleDB extension.
- Grafana's built-in PostgreSQL data source (Grafana 11 and later), with its
  TimescaleDB option on or off.
- Any client that speaks protocol version 3.0 or 3.2 (`psql`, libpq, pgx,
  JDBC, ...). Every client is relayed; which statements are cached is
  described under [Cache classification](#cache-classification).

Other engines that speak the PostgreSQL wire protocol are not part of this
provider's compatibility claim. The developer environment pins PostgreSQL 18
with TimescaleDB 2.30 and Grafana 13.

Provider names: `postgres`, and its alias `timescaledb`. Both select the same
engine, and metrics report `provider="postgres"`.

## Direct backend configuration

A `postgres` listener accepts the connections and maps to exactly one backend
(or one [User Router](#protocol-aware-user-router)) through the backend's
`listener_names`.

```yaml
listeners:
  tsdb:
    protocol: postgres
    port: 5432

authenticators:
  tsdb-clients:
    provider: basic
    users:
      grafana_ro: ${GRAFANA_RO_PASSWORD}

backends:
  tsdb1:
    provider: timescaledb
    origin_url: postgres://trickster_ro:REPLACE_ME@tsdb.example:5432/metrics
    listener_names: [tsdb]
    authenticator_name: tsdb-clients
    cache_name: default
    healthcheck:
      interval: 5s
      timeout: 3s
```

A complete annotated example is in
[`examples/conf/example.full.yaml`](../examples/conf/example.full.yaml).

### Authentication modes

The backend's authenticator decides who authenticates the client.

**With `authenticator_name` (recommended).** Trickster authenticates the
client itself against the authenticator's users and then logs in to the origin
with the `origin_url` credentials. Clients are offered SCRAM-SHA-256, plus
SCRAM-SHA-256-PLUS (channel binding) when the listener has a certificate.
Authenticator entries may be plaintext passwords or PostgreSQL
`SCRAM-SHA-256$...` verifiers copied from `pg_authid`. An entry stored as a
PostgreSQL `md5` hash or a crypt hash needs the client's cleartext password,
which is refused on a non-TLS connection unless the listener sets
`postgres.allow_cleartext_without_tls` (or `postgres.allow_md5` for the legacy
md5 exchange). Every client shares the origin role named in `origin_url`, so
give that role the least privilege the dashboards need. Replication
connections are refused in this mode.

**Without one (or with `observe_only: true`).** The client's own
authentication exchange is relayed to the origin untouched and Trickster holds
no credentials. One caveat: SCRAM-SHA-256-PLUS cannot succeed when both the
listener and the origin use TLS, because the client binds to Trickster's
certificate and the origin expects its own. Clients that require channel
binding must use the authenticator mode.

In both modes the cache is partitioned by the client's user and database, so
one user's cached answers are never served to another.

## TLS

TLS is negotiated in-band on the listener port, as PostgreSQL does it; there
is no separate TLS port. Configure the backend's `tls.full_chain_cert_path`
and `tls.private_key_path` to let clients upgrade, and set `require_tls: true`
to refuse plaintext clients.

TLS toward the origin is independent of the listener. Set the backend's
`postgres.upstream_tls_mode`:

| Mode | Meaning |
| --- | --- |
| `disable` (default) | plaintext |
| `require` | TLS without certificate verification |
| `verify-ca` | the origin's chain must verify against `tls.certificate_authority_paths` |
| `verify-full` | as `verify-ca`, and the certificate must match the origin host name |

`tls.client_cert_path` and `tls.client_key_path` add a client certificate.

## Connections, limits, and lifecycle

A result that outgrows the result limits is streamed to the client through a
fixed buffer, however large its rows are; the limits bound what Trickster
holds in memory, not what a client may read.

Each client connection gets its own origin connection for its whole life, so
session state never crosses clients, and Trickster adds no pooling. Put a
pooler such as PgBouncer between Trickster and the origin if you need one.

| Setting | Where | Default | Effect |
| --- | --- | --- | --- |
| `max_concurrent_conns` | backend | unlimited | origin connections; further clients get `53300 too many clients` |
| `connections_limit` | listener | unlimited | accepted client connections |
| `postgres.handshake_timeout` | listener | 10s | startup and authentication |
| `postgres.read_timeout` / `write_timeout` | listener | 30s | one message in flight |
| `postgres.idle_timeout` | listener | 5m | wait for a client's next message |
| `postgres.max_query_size_bytes` | listener | 1 MiB | larger statements are relayed without being analyzed |
| `postgres.max_result_rows` | backend | 100000 | larger results are relayed without being cached |
| `postgres.max_result_size_bytes` | backend | 64 MiB | as above |
| `timeout` | backend | 1m | origin connect, and the wait for a cached statement's result |

Cancel requests work through the listener: clients receive a Trickster-issued
cancellation key that maps to the origin's, and a client that disconnects
mid-query has its statement canceled at the origin. A configuration reload
that changes a listener's identity (origin, credentials, TLS, routes, limits)
restarts that listener and drains its sessions; other reloads leave sessions
alone.

## Protocol behavior

Everything a client sends is relayed: the simple and extended query protocols,
`COPY`, `LISTEN`/`NOTIFY`, transactions, multi-statement queries, notices and
errors (with their SQLSTATE) all reach the client as the origin sent them.

Only one kind of message is ever answered from the cache: a simple-protocol
`Query` holding a single read statement, sent while the session is idle (no
open transaction, nothing in flight). That is what Grafana, `psql` and most
reporting tools send. **Statements sent with the extended protocol
(Parse/Bind/Execute, i.e. prepared or parameterized statements) are relayed
and never cached.** For a driver that defaults to it, select its simple
protocol mode if you want caching (pgx: `default_query_exec_mode=simple_protocol`;
JDBC: `preferQueryMode=simple`).

Cached answers are the origin's own bytes: the row description and rows are
stored and replayed exactly as received, in text format. Trickster reads only
the bucket column, to place rows on the time axis.

## Cache classification

Each analyzed statement is counted in
`trickster_sql_query_analysis_total{backend_name,dialect,cache_mode,reason}`.

| `cache_mode` | Meaning |
| --- | --- |
| `delta` | cached per time bucket; only missing buckets are fetched |
| `object` | the whole result is cached under the statement's exact text |
| `none` | relayed, never cached |

### Delta-cacheable statements

A statement uses the delta cache when it is a single-table `SELECT` that
groups by one recognized time bucket and bounds the bucketed column with
literal (or `now()`-relative) lower and upper limits joined by `AND`.

| Bucket | Notes |
| --- | --- |
| `time_bucket(width, col)` | TimescaleDB; what Grafana's `$__timeGroup` writes with the TimescaleDB option on |
| `time_bucket(width, col, TIMESTAMPTZ '...')` | a typed origin |
| `time_bucket(width, col, '...'::interval)` | a typed offset |
| `time_bucket_gapfill(width, col)` | a single series, both bounds in `WHERE`, no `locf` or `interpolate` |
| `floor(extract(epoch from col)/N)*N` | `$__timeGroup` with the TimescaleDB option off; also with `date_part('epoch', col)` |
| `floor((col)/N)*N` | `$__unixEpochGroup`, over a column of epoch seconds with epoch-second bounds |
| `date_bin(width, col, origin)` | PostgreSQL 14+ |
| `date_trunc('unit', col)` | second to week, and only while the session `TimeZone` is UTC |

A width is a fixed-length interval: `'300.000s'`, `'5 minutes'`, `'1h30m'`,
`INTERVAL '1 hour'`, `'7 days'`. Month and year widths never match.
`FILTER`, ordered-set aggregates, TimescaleDB hyperfunctions such as `first`
and `last`, extra `GROUP BY` columns, and `ORDER BY` on the bucket (either
direction) are all fine.

Three behaviors are worth knowing:

- **Live ranges lose their partial edge buckets.** Bounds that are not on the
  bucket grid, as Grafana's `now`-relative ranges never are, are rounded
  inward, so the partial first and last buckets are left out of the answer
  rather than cached as if they were complete. `col <= X` keeps X's bucket
  only when X is the last instant of it (`...:59.999999`).
- **Zone-less literals need a UTC session.** PostgreSQL reads
  `'2026-09-17 00:00:00'` and `TIMESTAMP '...'` in the session `TimeZone`, so
  such bounds (and origins) qualify only while that zone is UTC. Grafana's
  `Z`-suffixed bounds always qualify.
- **Ordering within a bucket is the origin's.** Buckets are merged in time
  order and each keeps the row order the origin returned. `ORDER BY` must lead
  with the bucket or be absent; any other ordering makes the statement an
  object.

### Statements cached as objects

Everything else that is a deterministic read: statements with no bucket, a
`LIMIT`, a join, a subquery or CTE, `DISTINCT`, `HAVING`, window functions,
set-returning functions, a gapfill outside the form above, month or year
buckets, the time zone form of `time_bucket`, unsafe time predicates (`OR`,
`NOT`, `<>`, a strict `>` lower bound on the raw column), and statements the
SQL parser cannot write back faithfully (`FROM ONLY`, `json` casts,
`U&'...'` strings) or cannot parse at all (named arguments such as
`origin => ...`, `GROUP BY ROLLUP`). The `reason` label says which.

An object is keyed on the statement's exact text. A dashboard panel whose
range ends at `now` is therefore a new object on every refresh, and is served
from cache only while its range is unchanged. Objects live for
`timeseries_ttl`, so a repeated `SELECT count(*) FROM t` is answered from
cache for that long; lower it, or set `proxy_only: true`, if that is too
stale for your clients.

### Statements never cached

- Anything that is not a `SELECT`, and a `SELECT` that writes or locks
  (`WITH x AS (DELETE ... RETURNING *) SELECT ...`, `FOR UPDATE`,
  `SELECT ... INTO`).
- A statement that calls a volatile or side-effecting function, or reads the
  clock anywhere but in a time bound: `random()`, `now()`, `current_date`,
  `nextval()`, `pg_sleep()`, `pg_notify()`, advisory locks and similar
  (`reason="nondeterministic"`).
- Multi-statement queries, statements inside a transaction, pipelined
  statements, and statements larger than `postgres.max_query_size_bytes`.
- Everything in a session whose state Trickster can no longer vouch for; see
  [Session state](#session-state).

Trickster recognizes volatile functions by name. **A user-defined volatile
function, or one that changes a session setting as a side effect, is not
detectable.** Do not route such statements through a caching backend; use a
`proxy_only` backend for them.

### Continuous aggregates

Querying a continuous aggregate works like querying a table. To stay on the
delta cache, re-bucket the aggregate's bucket column and group by it:

```sql
SELECT time_bucket('15 minutes', bucket) AS "time", cab_type AS metric, sum(trips) AS trips
FROM trips_15m
WHERE bucket >= $__timeFrom() AND bucket < $__timeTo()
GROUP BY 1, 2 ORDER BY 1
```

A bare `SELECT bucket, trips FROM trips_15m WHERE ...` has no bucket function
and no `GROUP BY`, so it is cached as an object. Real-time aggregates change
their newest buckets as data arrives; `backfill_tolerance` controls how much
of the recent past Trickster re-fetches on each request.

## Grafana macros and exact SQL shapes

These are the statements Grafana 13's PostgreSQL data source sends, and how
they are classified.

| Macros | Sent as | Mode |
| --- | --- | --- |
| `$__timeGroup(col,'5m')`, TimescaleDB on | `time_bucket('300.000s',col) AS "time"` | delta |
| `$__timeGroup(col,'5m')`, TimescaleDB off | `floor(extract(epoch from col)/300)*300 AS "time"` | delta |
| `$__timeGroupAlias(col,$__interval)` | as above, with the panel's interval | delta |
| `$__timeFilter(col)` | `col BETWEEN '...Z' AND '...Z'` | delta with a bucket |
| `$__timeFrom()`, `$__timeTo()` | `'...Z'` | delta with a bucket |
| `$__unixEpochGroup(col,'10m')` | `floor((col)/600)*600` | delta |
| `$__unixEpochFilter(col)` | `col >= N AND col <= M` | delta with a bucket |
| `$__time(col)`, `$__timeEpoch(col)` | a projection, no bucket | object |
| `$__unixEpochNanoFilter(col)` | nanosecond integers | object |

The minimum supported `$__interval` is 1 minute. Prefer
`col >= $__timeFrom() AND col < $__timeTo()` over `$__timeFilter(col)`: both
are delta-cacheable, but the half-open form states exactly the range that is
cached. Grafana's pgx driver sends an empty `-- ping` query before each panel
query; it is relayed and left out of the request metrics.

The executable form of this contract is
[`pkg/backends/postgres/testdata/compatibility/v1.json`](../pkg/backends/postgres/testdata/compatibility/v1.json).

## Session state

Cached answers depend on session settings: `TimeZone` changes how every
`timestamptz` is rendered, `search_path` changes which table a name means.
Trickster therefore partitions the cache by a session identity: the user, the
database, every setting the origin announces (`TimeZone`, `DateStyle`,
`IntervalStyle`, `client_encoding`, `search_path` on PostgreSQL 18, ...), and
`role`, `extra_float_digits` and `bytea_output` from the client's own `SET`
statements. Two sessions share cached answers only when all of it matches.

A session that does something Trickster cannot follow stops being cached
until it reconnects (`reason="session_state"`): any other `SET` (for example
a custom `app.tenant` used by row-level security), `set_config()`,
`SELECT ... INTO`, DDL, `PREPARE`/`EXECUTE`, `DO`, `CALL`, and fast-path
function calls. A session with `standard_conforming_strings` off is relayed
uncached as well. Transactions, DML, `SHOW`, `EXPLAIN`, and harmless settings
such as `statement_timeout` and `application_name` do not end caching.

PostgreSQL never announces `extra_float_digits` or `bytea_output`, and a role
or database default can change them. With an authenticator, Trickster reads
the session's effective values once at origin login, makes them part of the
session identity, and returns to them on `RESET`. When the client's own login
is relayed, Trickster cannot ask, so it only knows a value the client set
itself.

Results whose bucket column cannot be read safely are cached as objects
instead: under a non-ISO `DateStyle`, and for a `float8` epoch bucket
(`$__unixEpochGroup`, or the `date_part('epoch', ...)` form) whenever
`extra_float_digits` is negative or unknown, since PostgreSQL may then render
`1788998400` as `2e+09`. In relay-authentication mode such a panel needs the
client to `SET extra_float_digits` (0 or higher) to use the delta cache.

## Failure behavior

The cache always fails open to a plain relay of the client's own statement:
when the origin rejects a statement Trickster rewrote for a sub-range
(`trickster_sql_query_rewrite_failures_total{reason="origin_rejected"}`), or
when a result outgrows the result limits (`reason="result_size"`). Such a
statement skips the cache until a marker expires, so it is not retried on
every refresh. Origin errors reach the client verbatim and are never cached.

## Protocol-aware User Router

A `postgres` listener can front a User Router ALB whose targets are
`postgres`/`timescaledb` backends. Trickster authenticates the client against
the router's authenticator, picks the backend mapped to the startup user (or
`default_backend`), and logs in to that backend's origin with its `origin_url`
credentials. The session stays on that backend until it ends.

- The router requires an authenticator. Targets need origin credentials and
  no authenticator or listener of their own.
- A user with no route, or whose backend is failing its health check, is
  refused with SQLSTATE `28000`. Sessions never fail over to another backend.
- `to_user` and `to_credential` are not supported.
- Each target keeps its own cache, limits and metrics.

A complete example is in
[`examples/conf/postgres-user-router.yaml`](../examples/conf/postgres-user-router.yaml).

## Metrics, logs, and health

| Metric | Labels |
| --- | --- |
| `trickster_sql_query_analysis_total` | `backend_name`, `dialect`, `cache_mode`, `reason` |
| `trickster_sql_query_cache_total` | `backend_name`, `dialect`, `cache_mode`, `cache_status` |
| `trickster_sql_query_rewrite_failures_total` | `backend_name`, `dialect`, `reason` |
| `trickster_proxy_requests_total`, `..._duration_seconds`, `trickster_proxy_points_total` | the common request labels, `method="QUERY"` |
| `trickster_pgwire_connections_total`, `trickster_pgwire_active_connections` | `backend_name` (and `event`) |
| `trickster_pgwire_errors_total` | `backend_name`, `class` |
| `trickster_pgwire_route_selections_total` | `router_name`, `backend_name`, `outcome` |

Query text, user names in labels, passwords and origin addresses are never
logged or exported. The health check logs in to the origin on a fresh
connection with the `origin_url` credentials and waits for `ReadyForQuery`;
see [Health Checks](./health.md#native-postgresql-health-checks).

## Operations and troubleshooting

**A panel is never a hit.** Look up its statement's `reason` in the analysis
metric. `unsupported_limit`, `unsupported_bucket` and `unsupported_format`
mean it is an object, and an object whose range ends at `now` changes every
refresh. `session_state` means the session did something unmodeled; find the
`SET` the client sends at connect time. `pipelined` and `in_transaction`
point at a driver that wraps reads in transactions or uses the extended
protocol.

**The first and last points are missing.** Expected on unaligned ranges; see
[Delta-cacheable statements](#delta-cacheable-statements).

**`origin_rejected` rewrite failures.** Trickster re-spells the statement
when it fetches a sub-range, and the origin refused the result. The client is
unaffected, since its own statement is relayed, but the statement is not
cached. Please file an issue with the statement.

**Stale data after a bulk rewrite.** Cached buckets older than the backfill
window are not re-fetched until they expire (`timeseries_ttl`). After
rewriting history, restart Trickster when using the memory cache, or clear
the backend's keys from a persistent one.

### Rollout and rollback

Canary the listener with a limited client population and compare with the
pre-release baseline. Roll back on a sustained increase of 5 percentage points
in `trickster_pgwire_errors_total` or proxy-error outcomes, 1 percentage point
in rewrite failures, or 20% in p95 request duration. Roll back by restoring
the previous configuration and binary, reloading or restarting Trickster, and
letting the listener drain; sessions are never moved between versions.
Setting `proxy_only: true` on the backend is the quickest mitigation: it
keeps the listener up and turns off all statement inspection and caching.

## Environment variables and reload behavior

There are no `TRK_*` variables for postgres listener or backend options;
configure them in YAML. Authenticator `users` values expand `${VARIABLE}`;
`origin_url` does not, so keep a configuration with an embedded password in a
secret store. Validate before rollout:

```sh
trickster -validate-config -config /etc/trickster/trickster.yaml
```

## Known compatibility gaps

- Extended-protocol (prepared, parameterized, binary-format) statements are
  relayed but never cached.
- Month, quarter and year buckets, the time zone overload of `time_bucket`,
  integer-time `time_bucket(bigint, col)`, and explicit `start`/`finish`
  arguments to `time_bucket_gapfill` are objects, not deltas.
- `date_trunc` and zone-less time literals are delta-cacheable only in UTC
  sessions.
- User-defined volatile functions and functions that change session settings
  cannot be detected.
- Downstream client-certificate authentication, GSSAPI encryption, and
  channel binding in relay-authentication mode are not supported.
- No cross-session invalidation: a write through one session does not evict
  what another session cached.
