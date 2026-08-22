# MySQL Initial Release Contract

This document freezes the compatibility, safety, and rollout contract for the
initial MySQL backend release. Behavior not explicitly listed as supported is
rejected or proxied without caching. Cache eligibility always fails closed.

## Compatibility and writes

- The compatibility claim covers Oracle MySQL only, from MySQL 8.4 LTS through
  MySQL 9.7 LTS. MariaDB, Percona Server, Aurora MySQL, and other compatible
  products are outside the initial claim until they have their own corpus.
- Grafana 11.0 through 11.6, using the built-in MySQL data source shipped with
  each Grafana release, is the initial client compatibility range. The
  developer environment pins Grafana 11.6.0 and MySQL 8.4.
- Successful DML and DDL are proxied and never cached. They do not invalidate
  OPC or DPC entries. A session that performs a mutation becomes cache-unsafe,
  but entries held for other sessions can remain stale until their configured
  TTL or normal eviction. This eventual-consistency tradeoff is intentional for
  the initial release and must be disclosed to operators.

## Authentication and authorization

- Trickster terminates downstream authentication. Authentication pass-through
  and silent fallback to origin credentials are not supported.
- The only downstream plugin is `mysql_native_password`. An unsupported plugin
  fails the handshake with an access-denied or unsupported-authentication MySQL
  error; Trickster never downgrades or retries with another plugin.
- The named Trickster authenticator is the only downstream credential source.
  Plaintext entries, including environment-expanded values and supported users
  files, are accepted for this release because native-password challenge
  verification requires the original password. Hash-form entries are rejected.
  Sanitized configuration output must continue to redact these values.
- The verified downstream username is an authorization boundary and is present
  in every OPC and DPC key, together with the selected backend and safe session
  identity. It must never be normalized across users.

## Protocol capabilities

The supported command set is `COM_QUERY` for one text-protocol statement,
`COM_INIT_DB`, `COM_PING`, `COM_QUIT`, and `COM_RESET_CONNECTION`.
`COM_RESET_CONNECTION` discards the upstream connection and all tracked state;
the next command creates a fresh one.

- Binary prepared statements and SQL `PREPARE`, `EXECUTE`, and `DEALLOCATE` are
  rejected. Long data, statement reset, statement close, server-side cursors,
  and binary result sets therefore cannot establish usable state.
- Compression and `LOCAL INFILE` capabilities are not advertised. All `LOAD`
  statements are rejected before reaching the origin.
- Multi-statement requests are rejected at command dispatch. Stored procedure
  `CALL`, which can produce multiple results, is also rejected. Trickster emits
  one text-protocol result only. MySQL versioned executable comments (`/*! */`)
  are rejected because they can hide unsupported or state-changing commands.
- `HELP`, `XA`, `HANDLER`, and `CACHE INDEX` statements are rejected because
  their response or session shape varies within the statement family. Valid
  text statements whose response shape cannot be classified are likewise
  rejected instead of being guessed as an OK packet.
- `COM_CHANGE_USER` is rejected. Session reset is supported only through
  `COM_RESET_CONNECTION` or reconnect.
- Replica registration and binlog dump commands are rejected with
  `ER_NOT_SUPPORTED_YET`. Other unknown commands receive a protocol error.
- Connection attributes may be syntactically accepted by the protocol library,
  but Trickster ignores them. They cannot select credentials, authorization,
  routing, cache identity, or result semantics.

A rejected command does not change upstream or cache state. The connection can
remain usable when the full command packet has been consumed; malformed,
oversized, timed-out, or partially streamed commands close the connection.

## TLS

MySQL TLS is always an in-band upgrade on the primary MySQL listener port.
Configuring an HTTP-style `tls_port` for a MySQL listener is invalid.

- Downstream TLS is disabled without a server certificate/key pair, optional
  when the pair is configured, and required when the backend also sets
  `require_tls: true`. The minimum TLS version is 1.2. Downstream client
  certificates are not supported in the initial release.
- Upstream TLS is disabled when no upstream TLS control is set. Setting
  `insecure_skip_verify: true` requires encryption without server verification.
  One `certificate_authority_paths` entry enables CA and hostname verification.
  A client certificate/key pair adds mutual TLS to either verified or
  explicitly unverified server mode. CA verification without hostname
  verification is not a separate supported mode.
- Server certificate/key and client certificate/key settings must be complete
  pairs. `require_tls` without a downstream server pair and more than one
  upstream CA path are configuration errors.
- Changes to downstream TLS requirements, certificates, upstream TLS inputs,
  authenticators, or origin transport restart and drain the MySQL listener so
  established sessions are never silently moved to new transport state.

## Session state and cache safety

The closed cache-safe state list contains only:

- the default database selected by the handshake, `USE`, or `COM_INIT_DB`; and
- a single session-scoped `SET time_zone = '<literal>'` statement.

The normalized database and time-zone literal are included in every cache key.
No claim is made that either setting cannot affect result bytes.

Transactions and savepoints bypass the cache while active. All other `SET`
forms, character-set or collation changes, `sql_mode`, `lc_time_names`, user
variables, temporary objects, locks, ambiguous transaction state, session-local
functions, and unclassified state-changing statements make the connection
cache-unsafe. DML and DDL also make the issuing connection cache-unsafe. That
state is cleared only by a successful `COM_RESET_CONNECTION` or reconnect,
both of which discard the upstream connection and restore the configured
database baseline.

## Resource ownership and errors

Listener-owned limits constrain untrusted downstream clients: connection
count, handshake/read/write/idle timeouts, maximum packet size, and maximum
query size. Backend-owned limits constrain origin work and cache memory:
connect/query timeouts, maximum result rows and bytes, maximum cache-object
size, and upstream concurrency.

The supported transport model is exactly one upstream connection per
authenticated downstream session. There is no pooling or multiplexing.
Admission control belongs at the listener and must not add channels to the row
or query hot path.

An admission failure returns a connection-level unavailable error. Handshake
timeout, packet overflow, query overflow, read timeout, write timeout, and idle
timeout close the downstream connection. Connect or query timeout and origin
concurrency rejection return a MySQL server-unavailable/interrupted error and
close the upstream connection before the downstream session can be reused.
Result row/byte overflow closes both sides because a partial result may already
have been emitted. Cache-object overflow skips storage and leaves the connection
usable.

## Performance and rollout gates

The release candidate must meet all of these gates on the project benchmark
runner, with the machine and five-run median recorded beside the evidence:

- analyzer: at most 250 microseconds and 64 KiB allocated per representative
  Grafana query;
- extent renderer: at most 25 microseconds and 16 KiB allocated per render;
- local in-memory OPC hit: p95 overhead at most 1 millisecond over direct result
  encoding and no more than 1.5 times the encoded result size in transient
  allocation;
- proxy-only and cache-miss paths: p95 added latency at most 2 milliseconds or
  10 percent over direct origin latency, whichever is larger;
- retained cache result memory: encoded payload plus at most 25 percent and
  1 MiB of fixed overhead per object; and
- the stripped Trickster binary may grow by at most 35 MiB relative to the same
  commit without the Vitess dependency graph.

Merge evidence requires lint, focused unit and fuzz tests, race coverage for
the MySQL backend and listener lifecycle, MySQL 8.4 integration tests in plain
and TLS modes, the Grafana 11.0 and 11.6 compatibility corpus, and recorded
parser/renderer/cache/proxy benchmarks. Any missing gate blocks release.

Rollback is required for a sustained increase over the pre-release baseline of
either 5 percentage points in proxy/origin/protocol errors, 10 percentage
points in proxy-only analysis outcomes, 1 percentage point in rewrite
failures, or 20 percent in p95 proxy latency. Cache-status, analysis-reason,
rewrite-failure, protocol-error, and origin-error metrics are the evidence; no
username is a metric label.

## User Router ALB

Native-protocol User Router ALBs support direct MySQL terminal backends. A
MySQL listener maps to either one direct MySQL backend or one User Router ALB;
Rules, nested ALBs, mixed terminal providers, empty routes, cycles, and HTTP
`to_user` or `to_credential` remapping are rejected during validation.

The listener-facing User Router owns downstream admission, TLS, and terminated
authentication. After authentication, Trickster's protocol-neutral resolver
selects a configured or default backend and the MySQL adapter asserts that the
selected backend provides a native MySQL runtime. The terminal backend owns
the upstream address, origin credentials, upstream TLS, cache, limits, and
query policy. No-route and unavailable outcomes are returned as bounded MySQL
errors; HTTP `no_route_status_code` is never emitted on a MySQL connection.

Routing occurs once before the upstream connection is opened. The selected
backend remains fixed for the complete downstream session, including across
transactions, prepared statements, database changes, and session variables.
The verified username and terminal backend remain in cache identity. Metrics
identify configured router and terminal names plus a bounded route outcome;
usernames are never metric labels. Reloads atomically switch new sessions or
restart the listener when authentication, TLS, transport, route, or terminal
runtime configuration changes. Existing sessions are never rerouted.

## Release compliance and approval

`make check-third-party-licenses` regenerates the linked dependency notice tree
and explicitly verifies the Vitess license used by the native protocol/parser.
Release archives include that tree. Container images install the project
`LICENSE`, `NOTICE`, and third-party tree under `/licenses`; RPMs install the
same material under the platform license directory.
