# MySQL Provider

Trickster can accept native MySQL client connections, proxy each authenticated
session to a MySQL origin, and cache eligible text-protocol query
results. This guide describes the initial supported contract. Behavior not
listed here is rejected or proxied without caching; cache eligibility always
fails closed.

## Compatibility

The supported matrix is:

- Oracle MySQL 8.4 LTS through 9.7 LTS;
- Grafana 11.0 and above using Grafana's built-in MySQL data source; and
- native clients that use `mysql_native_password` and the supported commands
  below.

MariaDB, Percona Server, Aurora MySQL, other MySQL-compatible products, older
Grafana releases, and third-party Grafana MySQL data sources are not part of
the initial compatibility claim, but should work without problems. File an
issue if you discover a compatibility issue.

The developer environment pins MySQL 8.4 and Grafana 13.

## Direct backend configuration

The listener accepts the MySQL wire protocol on one TCP port. MySQL negotiates
TLS in-band, so do not configure an HTTP-style `tls_port`.

```yaml
listeners:
  mysql-native:
    protocol: mysql
    address: ""
    port: 8486
    connections_limit: 200
    mysql:
      handshake_timeout: 10s
      read_timeout: 30s
      write_timeout: 30s
      idle_timeout: 5m
      max_packet_size_bytes: 16777215
      max_query_size_bytes: 1048576

authenticators:
  mysql-clients:
    provider: basic
    users:
      grafana_reader: ${GRAFANA_MYSQL_PASSWORD}

caches:
  mysql-cache:
    provider: memory
    memory:
      max_size_bytes: 536870912

backends:
  mysql-primary:
    provider: mysql
    listener_names: [mysql-native]
    authenticator_name: mysql-clients
    origin_url: mysql://trickster_ro:REDACTED@mysql.example:3306/analytics
    cache_name: mysql-cache
    timeout: 30s
    max_concurrent_conns: 200
    max_object_size_bytes: 8388608
    mysql:
      max_result_rows: 100000
      max_result_size_bytes: 67108864
    healthcheck:
      interval: 5s
      timeout: 3s
      failure_threshold: 3
      recovery_threshold: 2
```

Exactly one direct MySQL backend or one supported MySQL User Router ALB maps to
a MySQL listener. The origin URL must use the `mysql` scheme and include an
origin username. Percent-encode reserved username, password, and database
characters. Configuration stringification and the sanitized management
configuration redact an embedded origin password, but the source configuration
must still be protected as a secret.

The authenticator is the only downstream credential source. Trickster
terminates downstream authentication; it does not pass client credentials to
the origin or silently fall back to origin credentials. Native-password
verification requires the original password, so MySQL authenticator entries
must be plaintext values, including environment-expanded values or a protected
CSV users file. Htpasswd hashes are rejected for this listener. Do not expose
the raw configuration management endpoint outside a trusted administrative
network.

## TLS

### Downstream TLS

Add a server certificate and key to the listener-facing backend:

```yaml
backends:
  mysql-primary:
    # ...direct backend settings above...
    require_tls: true
    tls:
      full_chain_cert_path: /etc/trickster/tls/server.crt
      private_key_path: /etc/trickster/tls/server.key
```

Without a server pair, downstream TLS is disabled. With the pair, TLS is
optional unless `require_tls: true` rejects plaintext clients. TLS 1.2 is the
minimum. Downstream client certificates are not supported in the initial
release.

### Upstream TLS

The upstream modes are:

- no upstream TLS fields: plaintext;
- `insecure_skip_verify: true`: encryption without server verification;
- one `certificate_authority_paths` entry: CA and hostname verification; and
- a complete `client_cert_path`/`client_key_path` pair: mutual TLS, combined
  with verified or explicitly unverified server mode.

```yaml
backends:
  mysql-primary:
    # ...direct backend settings above...
    tls:
      certificate_authority_paths:
        - /etc/trickster/tls/origin-ca.crt
      client_cert_path: /etc/trickster/tls/origin-client.crt
      client_key_path: /etc/trickster/tls/origin-client.key
```

Certificate/key settings must be complete pairs. More than one upstream CA
path, `require_tls` without a downstream server pair, or an incomplete pair is
a configuration error. There is no CA-verification-without-hostname mode.

## Connections, limits, and lifecycle

Each authenticated downstream connection owns exactly one upstream connection.
There is no pooling or multiplexing, which prevents transaction, database, and
session state from leaking between clients. Size `connections_limit` and
`max_concurrent_conns` together and leave capacity for health probes. A useful
starting point is one admitted origin connection per downstream connection,
plus normal operational headroom.

Listener limits protect the downstream boundary:

- `connections_limit` limits concurrent client connections (`0` is unlimited);
- handshake, command-read, response-write, and idle timeouts bound each phase;
- `max_packet_size_bytes` is fixed at 16 MiB minus one; and
- `max_query_size_bytes` limits SQL text within that packet ceiling.

Backend limits protect the origin and cache:

- `timeout` bounds connect and query work;
- `max_concurrent_conns` bounds upstream admission;
- `mysql.max_result_rows` and `mysql.max_result_size_bytes` bound results; and
- `max_object_size_bytes` skips caching an otherwise valid oversized object.

Handshake, packet, query, read, write, and idle failures close the client
connection. Origin timeout or concurrency rejection closes the affected
upstream connection and returns a bounded MySQL error. A row/byte overflow
closes both sides because a partial result may already have been emitted.

On shutdown, Trickster stops accepting new sessions and drains existing ones
within the configured management drain window. A reload that changes MySQL
authentication, listener transport, certificates, origin transport, routing,
or relevant limits restarts and drains the listener; an established session is
never silently moved to new credentials, TLS policy, or a different origin.

## Protocol behavior

Supported commands are:

- `COM_QUERY` containing one text-protocol statement;
- `COM_INIT_DB`;
- `COM_PING`;
- `COM_QUIT`; and
- `COM_RESET_CONNECTION`.

Reset discards the upstream connection and tracked session state. The next
command opens a fresh origin connection.

The initial release rejects binary prepared statements; SQL `PREPARE`,
`EXECUTE`, and `DEALLOCATE`; server cursors; binary results; compression;
`LOCAL INFILE` and all `LOAD` statements; multi-statements; stored-procedure
`CALL`; `HELP`, `XA`, `HANDLER`, and `CACHE INDEX`; executable version comments;
unclassified text response shapes; `COM_CHANGE_USER`; replica registration; and
binlog commands. Connection attributes may be syntactically accepted but are
ignored for authentication, routing, cache identity, and query semantics.
Malformed, oversized, timed-out, or partially streamed commands close the
connection. A fully consumed unsupported command normally leaves it usable.

Successful DML and DDL are proxied and never cached. They do not invalidate
existing OPC or DPC entries, so another session may see cached data until TTL
expiry or eviction. Use short TTLs or purge affected cache data when immediate
read-after-write visibility across sessions is required.

## Cache classification

Trickster uses three outcomes:

- **Delta Proxy Cache (DPC):** a supported deterministic aggregate time-series
  `SELECT` with one unambiguous time output, literal positive cadence, safe
  result shape and ordering, and an inclusive-lower/exclusive-upper raw-time
  predicate;
- **Object Proxy Cache (OPC):** a deterministic single-result `SELECT` that is
  safe to cache as one object but is not proven reusable by time extent; and
- **proxy-only:** mutations, transactions, unsupported or unsafe session state,
  non-deterministic or rejected shapes, and any query whose safety cannot be
  established.

DPC supports the tested zero-phase integer/cast and
`FLOOR(UNIX_TIMESTAMP(column) / n) * n` bucket forms when `n` is the same
positive integer literal on both sides. Dynamic, mismatched, non-positive,
overflowing, ambiguous, or non-zero-phase forms fail closed. Qualified and
backtick-quoted identifiers, multiple numeric values, and string dimensions
are supported only in the shapes recorded by the compatibility corpus.

The authenticated downstream username, selected terminal backend, normalized
database, supported time-zone literal, and query identity participate in cache
keys. Cache entries are never shared across authenticated users merely because
their SQL is identical.

## Grafana macros and exact SQL shapes

Only patterns represented by the versioned compatibility corpus are published
as supported. The corpus covers `$__time`, `$__timeEpoch`, `$__timeFilter`,
`$__timeFrom`, `$__timeTo`, `$__timeGroup`, `$__timeGroupAlias`,
`$__unixEpochFilter`, `$__unixEpochFrom`, `$__unixEpochTo`,
`$__unixEpochGroup`, and `$__unixEpochGroupAlias`. Its documented minimum
`$__interval` is one minute.

Grafana's normal inclusive `$__timeFilter` expansion is OPC:

```sql
SELECT
  CAST(CAST(UNIX_TIMESTAMP(ts)/(300) AS SIGNED)*300 AS SIGNED) AS time,
  COUNT(*) AS samples
FROM telemetry
WHERE ts BETWEEN FROM_UNIXTIME(1785542400)
             AND FROM_UNIXTIME(1785628800)
GROUP BY time
ORDER BY time
```

Compose `$__timeFrom()` and `$__timeTo()` into a half-open predicate for DPC:

```sql
SELECT
  CAST(CAST(UNIX_TIMESTAMP(ts)/(60) AS SIGNED)*60 AS SIGNED) AS time,
  AVG(value) AS mean_value
FROM telemetry
WHERE site = 'denver'
  AND ts >= FROM_UNIXTIME(1785542400)
  AND ts < FROM_UNIXTIME(1785628800)
GROUP BY time
ORDER BY time
```

The epoch-second equivalent is:

```sql
SELECT
  CAST(CAST(epoch_seconds/(300) AS SIGNED)*300 AS SIGNED) AS time,
  SUM(requests) AS requests
FROM service_rollups
WHERE region = 'us-west'
  AND epoch_seconds >= 1785542400
  AND epoch_seconds < 1785628800
GROUP BY time
ORDER BY time
```

Trickster normalizes the lower bound up and the exclusive upper bound down to
the cadence and caches only complete buckets. A range with no complete bucket
normalizes to an empty range. Inclusive upper bounds and Grafana's strict-lower
`$__unixEpochFilter` expansion remain OPC because they do not prove the same
complete-bucket semantics. Native `DATETIME`/`TIMESTAMP`, epoch-second integer,
and the corpus's epoch-nanosecond adaptation are supported in their recorded
shapes.

## Session state

The only cache-safe tracked state is the selected default database and one
session-scoped literal `SET time_zone = '<value>'`. `USE` and `COM_INIT_DB`
update the database portion of cache identity.

Transactions and savepoints bypass cache while active. Other `SET` forms,
character set or collation changes, `sql_mode`, `lc_time_names`, user
variables, temporary objects, locks, session-local functions, mutations, and
unclassified state changes make that connection cache-unsafe. Reconnect or a
successful `COM_RESET_CONNECTION` is required to restore the configured
baseline.

## Protocol-aware User Router

A native MySQL User Router selects one direct terminal MySQL backend from the
verified downstream username:

```yaml
listeners:
  mysql-routed:
    protocol: mysql
    port: 8486
    connections_limit: 200

authenticators:
  tenant-clients:
    provider: basic
    users:
      tenant_a_reader: ${TENANT_A_CLIENT_PASSWORD}
      tenant_b_reader: ${TENANT_B_CLIENT_PASSWORD}

caches:
  tenant-a-cache:
    provider: memory
  tenant-b-cache:
    provider: filesystem

backends:
  tenant-a-mysql:
    provider: mysql
    authenticator_name: tenant-clients
    origin_url: mysql://tenant_a_ro:REDACTED@mysql-a.example:3306/analytics
    cache_name: tenant-a-cache
    healthcheck:
      interval: 5s
      timeout: 3s

  tenant-b-mysql:
    provider: mysql
    authenticator_name: tenant-clients
    origin_url: mysql://tenant_b_ro:REDACTED@mysql-b.example:3306/analytics
    cache_name: tenant-b-cache
    healthcheck:
      interval: 5s
      timeout: 3s

  mysql-by-tenant:
    provider: alb
    listener_names: [mysql-routed]
    authenticator_name: tenant-clients
    alb:
      mechanism: ur
      user_router:
        default_backend: tenant-a-mysql
        users:
          tenant_a_reader:
            to_backend: tenant-a-mysql
          tenant_b_reader:
            to_backend: tenant-b-mysql
```

The listener-facing router owns the one downstream authentication exchange,
admission, and TLS. Direct terminal MySQL backend entries must reference the
same named authenticator to satisfy backend validation, but do not perform a
second client exchange. The terminal owns its origin credentials, upstream
TLS, cache, health, limits, and query policy. Routing occurs once before
opening the upstream connection and remains sticky for the whole downstream
session. Transactions, database changes, and session state never trigger
rerouting.

Only direct MySQL terminal backends are supported. Nested ALBs, Rules, cycles,
mixed terminal providers, empty routes, and MySQL `to_user`/`to_credential`
remapping are configuration errors. A router requires an authenticator. An
unmapped user uses `default_backend` when configured; without one, Trickster
returns a bounded native MySQL no-route error. HTTP `no_route_status_code` is
not sent on the native connection. A selected unhealthy or unavailable
terminal fails as a MySQL availability error; the session is not silently
routed elsewhere.

The verified username and selected terminal remain in cache identity. Route
metrics use configured router/backend names and bounded outcomes, never the
username.

## Metrics, logs, and health

Important metrics include:

- `trickster_sql_query_analysis_total{backend_name,dialect,cache_mode,reason}`;
- `trickster_sql_query_rewrite_failures_total{backend_name,dialect,reason}`;
- `trickster_sql_query_cache_total{backend_name,dialect,cache_mode,cache_status}`;
- `trickster_proxy_request_duration_seconds` and
  `trickster_proxy_points_total` with `provider="mysql"`;
- `trickster_mysql_connections_total`, `trickster_mysql_active_connections`,
  `trickster_mysql_errors_total`, `trickster_mysql_route_selections_total`, and
  `trickster_mysql_command_duration_seconds`; and
- cache operation, usage, limit, and event metrics labeled by `cache_name`.

Example PromQL, replacing names to match the deployment:

```promql
sum by (cache_mode, reason) (rate(trickster_sql_query_analysis_total{backend_name="mysql1"}[5m]))
sum by (reason) (rate(trickster_sql_query_rewrite_failures_total{backend_name="mysql1"}[5m]))
sum by (cache_mode, cache_status) (rate(trickster_sql_query_cache_total{backend_name="mysql1"}[5m]))
histogram_quantile(0.95, sum by (le) (rate(trickster_proxy_request_duration_seconds_bucket{backend_name="mysql1",provider="mysql"}[5m])))
sum by (cache_status) (rate(trickster_proxy_points_total{backend_name="mysql1",provider="mysql"}[5m]))
sum by (operation, status) (rate(trickster_cache_operation_objects_total{cache_name="mysql-cache"}[5m]))
trickster_cache_usage_bytes{cache_name="mysql-cache"}
sum by (reason) (rate(trickster_cache_events_total{cache_name="mysql-cache",event="eviction"}[5m]))
```

Analysis logs contain bounded backend, cache mode, reason, and statement-type
fields. Rewrite and protocol failures use bounded categories; SQL text,
passwords, and usernames are not metric labels. Keep debug logging temporary
in production because classification logs can be high volume.

Native health checks open a bounded connection with the backend's configured
origin credentials and TLS policy, then issue `COM_PING`. They use the common
interval, timeout, failure, and recovery thresholds. HTTP health request fields
do not apply. Health output reports sanitized authentication, TLS, timeout,
refused-connection, and server-error categories. See [Health Checks](health.md)
for management endpoint details.

## Kubernetes deployment

The base examples expose the optional `mysql` Service/container port on 8486.
The safe routed example keeps the complete credential-bearing configuration in
[`deploy/kube/mysql-user-router-secret.yaml`](../deploy/kube/mysql-user-router-secret.yaml)
and replaces the base ConfigMap volume with
[`deploy/kube/mysql-deployment-patch.yaml`](../deploy/kube/mysql-deployment-patch.yaml).
Replace every placeholder and use an encrypted/external secret controller in
production. Do not put an origin DSN, downstream passwords, private keys, or
client keys in `deploy/kube/configmap.yaml`.

For direct TLS or mTLS, add the server, CA, and client certificate material to
a Kubernetes TLS or opaque Secret, mount it read-only, and reference the mount
paths from the backend `tls` block. Rotating that Secret requires the
configuration/certificate reload path described above; established sessions
are drained rather than changing transport identity in place.

## Operations and troubleshooting

### Kubernetes readiness

The native health scheduler does not delay listener startup or make the general
health endpoint return a non-200 status. `/trickster/ping` proves only that the
process is running, and a TCP readiness probe on port 8486 proves only that the
native listener is accepting connections. The companion manifest uses that TCP
probe. Treat the scheduled MySQL status published in `/trickster/health?json`
as the origin-readiness signal in an external controller or monitoring rule;
do not assume the endpoint's HTTP status reflects origin health. Allow at least
`interval * failure_threshold` or `interval * recovery_threshold` for a stable
transition. User Router terminals report separately, and a failing selected
terminal returns a native availability error rather than rerouting the session.

### Capacity planning

Start from peak concurrent dashboard/client sessions. Reserve approximately
one origin connection per admitted client, then add health and rollout
headroom. Bound result rows/bytes below the pod or process memory budget and
set `max_object_size_bytes` below both the result-byte limit and a practical
fraction of cache capacity. For an in-memory cache, budget retained objects,
index overhead, concurrent result encoding, and normal process memory. Use
finite query, command, and idle timeouts so abandoned clients cannot retain
connections indefinitely.

### Repeated misses or proxy-only outcomes

1. Group `trickster_sql_query_analysis_total` by `cache_mode` and `reason`.
2. Confirm the query is deterministic, single-result, and outside a
   transaction or unsafe session.
3. For DPC, inspect the expanded SQL: require a literal cadence and `>=` lower,
   `<` upper raw-time predicates.
4. Confirm the requested interval contains at least one complete cadence
   bucket and that cache TTL, backfill tolerance, and retention are suitable.
5. Check username, selected backend, database, and time zone; these intentionally
   isolate keys.
6. Inspect cache operation status and eviction metrics for admission failures
   or churn.

### Authentication and TLS

- Authentication failure: verify the named authenticator, plaintext/CSV
  credential source, exact username, supported native-password plugin, and
  filesystem permissions on a mounted users file.
- Downstream TLS failure: verify the server pair, `require_tls`, client TLS
  mode, hostname, and TLS 1.2+ support. Do not use `tls_port`.
- Upstream TLS failure: verify the single CA path, hostname in the origin URL,
  complete client pair when using mTLS, file permissions, and whether
  `insecure_skip_verify` was intentionally selected.

### Origin and cache failures

During an origin outage, health transitions and MySQL error metrics should
rise; existing safe cache hits may continue, while misses fail. Restore origin
health before increasing connection limits. A cache-provider failure should
surface in cache operation/event metrics and logs; query handling fails closed
to the provider's normal proxy behavior rather than treating an unknown cache
state as a hit.

Certificate rotation and relevant configuration reloads drain affected native
listeners. Monitor active connections, authentication/TLS errors, route
selection outcomes, health state, rewrite failures, cache status, and p95
latency until the old sessions have drained.

### Rollout and rollback

Canary the listener/backend configuration with a limited client population.
Compare against the pre-release baseline and roll back for a sustained increase
of 5 percentage points in proxy/origin/protocol errors, 10 percentage points in
proxy-only classifications, 1 percentage point in rewrite failures, or 20% in
p95 proxy latency. Roll back by restoring the previous configuration and
binary/image on the release branch, reloading or restarting Trickster, and
allowing the old MySQL listener to drain. Do not move an established session
between versions. Preserve metrics and sanitized logs for the incident record.

## Environment variables and reload behavior

The global `TRK_ORIGIN_URL` and `TRK_ORIGIN_TYPE` variables map only to the
default backend and are suitable only for a basic single-backend deployment.
There are no one-to-one `TRK_*` variables for MySQL listener limits, backend
limits, TLS, health, or User Router options; configure those in YAML.
Authenticator `users` values support `${VARIABLE}` expansion, and supported
users files and secret volume mounts are preferred for production. The
`origin_url` field does not expand `${VARIABLE}` in structured YAML; store a
configuration containing an embedded DSN as a protected secret, not a public
ConfigMap.

Validate before rollout:

```sh
trickster -validate-config -config /etc/trickster/trickster.yaml
```

SIGHUP and the management reload endpoint reload a changed file. Authentication,
TLS, transport, routing, and terminal runtime changes restart and drain the
affected MySQL listener. Cache policy changes apply to new work; existing cache
objects remain subject to their stored TTL and normal eviction.

## Known compatibility gaps

The initial release does not claim support for:

- MySQL-compatible server products outside Oracle MySQL 8.4-9.7;
- downstream plugins other than `mysql_native_password` or downstream mTLS;
- prepared/binary protocols, compression, local infile, multi-results, stored
  procedures, binlog/replication commands, or connection-attribute semantics;
- general SQL parsing beyond the Vitess-supported and compatibility-corpus
  shapes;
- window functions, grouping sets, rollups, ambiguous time axes or buckets,
  unsafe boolean predicates, non-deterministic outputs, dynamic/non-zero-phase
  buckets, or inclusive-range DPC;
- arbitrary session-state caching or cross-session mutation invalidation; and
- nested/mixed MySQL User Router topologies or origin credential remapping.

The executable SQL contract is
[`pkg/backends/mysql/testdata/compatibility/v1.json`](../pkg/backends/mysql/testdata/compatibility/v1.json). The maintainer-facing frozen release matrix is in
[`docs/developer/mysql-release-contract.md`](developer/mysql-release-contract.md).
