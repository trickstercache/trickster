# Adding A PostgreSQL Wire Engine

PostgreSQL wire compatibility is a transport contract, not a SQL dialect or
session-state contract. Keep engine differences behind `pgwire.Engine` and
its optional interfaces. GreptimeDB in `pkg/backends/greptimedb` is the worked
example; `pkg/backends/postgres` remains the reference PostgreSQL engine.

## Start With Relay

Implement `Name`, `DefaultPort`, `Dialect`, `Defaults`, `TimeAxis` and
`TimeSemantics`. Return a nil `Analyzer` initially. Register the engine in
`pkg/backends/providers/registry` using the PostgreSQL native adapter. Prove
startup, authentication, simple/extended query forwarding, errors and connection
recovery against a real origin before enabling caching.

Do not infer cancellation, transactions, type OIDs, precision or successful
setting changes from a PostgreSQL-shaped response. Observe the raw protocol
and compare with a direct connection. Capture only the relevant fields, not
passwords or reusable authentication/cancellation material.

## Backend Model

Providers that also serve HTTP implement `pgwire.HTTPEngine.SupportsHTTP`.
Their `origin_url` remains HTTP; `postgres.upstream_url` independently selects
the native endpoint. HTTP userinfo is not a source of native credentials.
Listener validation sets runtime HTTP/native mappings and validates only the
configured protocols, including terminal user-router targets.

GreptimeDB also has a MySQL engine. Native registry lookup must therefore use
`GetForProvider(protocol, provider)` or enumerate `ForProvider(provider)`.
`GetByProvider` deliberately returns nil when a provider has several adapters;
never let map iteration choose which protocol to validate or run.

Health probes must match the selected surface. Reuse the shared scheduler;
do not create a provider-specific polling loop. Include endpoint, engine,
authentication, TLS and result-affecting settings in native restart identity.

## Session And Time Semantics

`TimeAxis` maps only verified OIDs to timestamp/date/numeric time axes.
`TimeSemantics` describes engine guarantees, such as GreptimeDB's naive UTC
timestamps and lossless float text. Assumed settings are for nonconfigurable
guarantees, not convenient substitutes for unknown role or server defaults.

Implement `SessionDefaultsEngine` when PostgreSQL's defaults query is not
valid upstream. GreptimeDB probes `SHOW TIMEZONE`, `SHOW DateStyle` and
`SHOW IntervalStyle`. Returned columns must match the declared names and
contain exactly one non-null row per result.

Implement `SessionSettingsEngine` for different tracked settings, aliases,
neutral settings or `SET LOCAL` semantics. GreptimeDB's `time_zone` aliases
`timezone`; its local setting persists. Requested startup settings partition
the identity but are not accepted as effective values before observation.
Failed statements must not publish successful changes. Unknown state bypasses
caching conservatively.

Implement `SessionAnalyzer.ForSession` if query eligibility depends on the
effective timezone. Keep it cheap and immutable. Test two sessions with
different settings, successful/failed SET, reset, startup overrides and
reconnects. GreptimeDB's pgwire microsecond text precision must not be reused
for its MySQL nanosecond text or HTTP timestamp representation.

## Analyzer And Renderer

Follow [SQL dialect adapters](sql-dialect-adapters.md). Keep parser AST types
out of the shared plan. Reuse Cockroach adapter bucket matchers, compact-duration
parsing and narrowly scoped post-render hooks when applicable. The exported
compact parser rejects signs, fractions, whitespace, zero components and
overflow; supply immutable dialect-specific fixed-duration units.

Use clause rewriters only when the complete clause semantics are proved.
Unsupported `RANGE`, `ALIGN` and TQL shapes need no speculative delta plan.
Do not silently round HTTP partial buckets away merely because a dashboard's
pgwire mode intentionally consumes complete buckets. Preserve all non-time
predicates and result ordering. Rendering must be immutable and reentrant.

## Tests And Corpus

Add an origin target to `integration/pgwire_conformance_test.go`; declare
unsupported capabilities explicitly and prove them with a direct probe.
Assertions must compare direct and proxied values and check counters so a
passthrough response cannot masquerade as a cache hit.

Put a versioned corpus in the backend's `testdata/compatibility` directory.
Use `pkg/testutil/sqlcompat.Run`, `CheckGrafanaMacros` and `Benchmark` with a
corpus path and a session-zone-aware analyzer. Each delta case records units,
cadence, phase, bounds and columns. The runner checks canonical identity and
renders and analyzes an exact cache extent. Capture actual Grafana
`executedQueryString` over an unaligned range; record plugin version, original
macros and unsupported cases instead of guessing the expansion.

Run PostgreSQL regressions as well as new engine tests. Measure the shared
pgwire gate/hit path and new analyzer/render/model paths, excluding fixture
construction from timed loops. Run race tests, fuzz parsing boundaries, and
retain failed live attempts separately from corrected runs.

## Developer Environment

Use `<engine>-direct` and `<engine>-trickster` datasource names and dashboard
regex `/^<engine>-/`. Share the generated seed files and time-window metadata;
do not download another independent dataset. Give the seeder a writer role
and Grafana/Trickster read-only origin roles.

Reserved ports are 8480 HTTP, 8485 Flight SQL, 8486 MySQL, 8487 ClickHouse,
8488 PostgreSQL/TimescaleDB, 8489 GreptimeDB pgwire and 8490 future QuestDB.
GreptimeDB MySQL uses 8491. Check host occupancy and bind remote acceptance
ports to loopback. Pin an upstream image that passes direct Grafana health
and queries before validating Trickster. Record nightly and stable builds
accurately; an upstream merge alone is not a released or tested binary.
