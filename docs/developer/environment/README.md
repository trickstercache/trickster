# Setting up a Local Developer Environment

## Prerequisites

* Docker is installed and running, and the `docker` command is available.
* Golang 1.27 is installed
* Make and other tools are installed

## Running

A Docker Compose file is available that starts up and seeds the containers needed
for your developer environment. This includes TSDBs like Prometheus, and Dashboard
apps like Grafana.

From the root of the repo, run `make developer-start` to start the environment.

Next, you must run Trickster from your local repo, by running `make serve-dev`.
This runs `cmd/trickster/main.go` with a config file from the developer environment.

You can combine these make actions `make developer-start serve-dev` if you want.

Once you have the Docker Compose running, and Trickster running locally, visit
the Grafana Dashboard at <http://127.0.0.1:3000/d/uAJ8w1wZz/trickster-status>.

The data in this dashboard is polled by Prometheus from your local Trickster
dev instance. So the longer you keep this dashboard up and refreshing, the more
you can test out Trickster acceleration features. You can change the Data Source
selector to go between various Trickster configs, or bypass Trickster altogether
for verification purposes.

You can stop the developer environment by running `make developer-stop`. To
delete the developer environment, run `make developer-delete` which will destroy
all data including named volumes.

## Graphite

The environment runs a Graphite origin (`graphiteapp/graphite-statsd`: carbon-cache
+ graphite-web on Whisper storage) that is the test bed for Trickster's Graphite
backend provider. Everything about it is deliberate: the point of the service is
to exercise Whisper archive transitions, retention clamping, and config-vs-disk
disagreement, so the setup is documented here in some detail.

| What | Where |
|---|---|
| graphite-web (render / metrics APIs) | <http://127.0.0.1:8081> |
| carbon plaintext ingest | `127.0.0.1:2003` |
| schema / aggregation config | `docker-compose-data/graphite-config/storage-{schemas,aggregation}.conf` |
| data generator | `docker-compose-data/graphite-config/generator.py` |
| Grafana datasources | `graphite-direct` (uid `ds_graphite_direct`) → origin; `graphite-trickster` (uid `ds_graphite_trickster`) → `http://host.docker.internal:8480/graphite1` |
| Trickster backend | `graphite1` in `trickster-config/trickster.yaml` (provider `graphite`, dedicated `graphite_fs` cache; the sizing it needs comes from the provider's own defaults, which this environment deliberately does not override); health at <http://127.0.0.1:8481/trickster/health/graphite1> |
| Grafana dashboard | <http://127.0.0.1:3000/d/trk_graphite/graphite> |
| Whisper files | docker volume `graphite-data`, under `/opt/graphite/storage/whisper/` |

### Ladders

`storage-schemas.conf` puts each generated namespace on its own ladder:

| Namespace | Ladder on disk | Aggregation (from `storage-aggregation.conf`) |
|---|---|---|
| `dev.fast.*` | `10s:6h, 60s:7d, 10m:5y` | `.count` sum (xff 0), `.p99` max (xff 0.1), `.percent` average (xff 0.3) |
| `dev.medium.*` | `60s:2d, 5m:30d, 1h:2y` | `.count`/`.dollars` sum, `.depth` last (xff 0.5) |
| `dev.coarse.*` | `5m:90d` (one rung) | `.bytes` last, others average |
| `dev.drift.*` | `30s:12h, 5m:14d, 1h:1y` — **but the config says `60s:2d, 5m:30d, 1h:2y`** | average |

Every ladder with more than one rung has a transition inside the seeded window,
and the coarse ladder's short `maxRetention` makes the retention clamp easy to
observe (`from=-120d` returns exactly 90 days).

### The schema-drift case

`dev.drift.*` reproduces the central failure mode of
`trickster-data/todos/graphite-resolution-prediction.md` §3: the Whisper files on
disk were created under one ladder, and `storage-schemas.conf` declares a
different one for the same pattern. Carbon never rewrites an existing file, so
once created the files keep their ladder regardless of what the config says.
Anything that predicts the step from the static config alone is wrong here —
the observed step at a one-hour range is 30s, not 60s — which is exactly what a
probe-based resolver has to detect.

It is set up by the generator rather than by editing the config after the fact,
so it survives `make developer-recreate`: `generator.py` creates `dev.drift.*`
files with `DRIFT_RETENTIONS` instead of the `storage-schemas.conf` match. The
seed step asserts the drift is observable (`observed step == disk != config`)
and fails if it is not.

### Seeding and the generator sidecar

Graphite will not accept future timestamps, so the usual "seed once and the
dashboards stay interesting as time passes" approach used for ClickHouse does
not work. Instead every value is a pure function of time,

    value = f(metric, floor(t / step))

and `generator.py` uses that one function two ways:

* **`graphite_seed`** (one-shot, also run by `make developer-seed-data`)
  backfills `[now - 6w, now]` by writing the Whisper files directly — far
  faster than replaying through carbon — then validates through graphite-web:
  the observed step at several `from` ages must match the intended ladder, the
  drift must be observable, the retention clamp must hold, and `dev.*` must
  expand to all four namespaces. A failure fails the seed; a silently mis-seeded
  ladder would invalidate every later measurement.
* **`graphite_generator`** (always on) catches up any gap since it last ran the
  same way, then emits `f(now)` for every metric to carbon's port 2003 at each
  step boundary, forever.

Coarser rungs are backfilled with the aggregate of the fine-step values in each
bucket (using the file's own aggregation method), which is what Whisper's
write-time propagation produces for streamed points, so there is no seam
between the backfilled and streamed regions of a file. A container that has
been up for months has the same data shape as one started this morning, and the
same query at the same wall-clock time yields the same values on every machine.

`make developer-seed-data` recreates the files from scratch (`GRAPHITE_SEED_FORCE=1`),
which is what to run after editing `storage-schemas.conf`, `storage-aggregation.conf`,
or the series table in `generator.py`; a plain `make developer-start` only fills
in whatever is missing. The generator's state lives in
`/opt/graphite/storage/trickster-generator-state.json` on the `graphite-data` volume.

### The dashboard

<http://127.0.0.1:3000/d/trk_graphite/graphite> has one row per ladder plus an
"Edge cases" row whose panels exist to pin down the Trickster provider's
behavior before it is written: an archive-boundary query, a beyond-`maxRetention`
query, the drift namespace, functions that will not be on the delta-cache
allowlist, and a multi-target render whose targets resolve to different steps.
The `Datasource` selector switches between `graphite-direct` (origin) and
`graphite-trickster` (through the local `make serve-dev` Trickster). Both
selections must render every data and edge-case panel identically — that
equivalence is the standing correctness check for every phase of the provider.

The final row, **Trickster — graphite1 provider**, is pinned to the
`1: Prom | Direct | GET` datasource (the Prometheus that scrapes your local
Trickster) and to the `graphite1` backend / `graphite_fs` cache; it does not
follow the selector. Each panel title names the phase of
`trickster-data/todos/graphite-backend-implementation.md` that makes it
non-empty:

| Panel | Lights up in |
|---|---|
| Frontend requests by path & status; frontend latency p50/p99 | **live now** (Phase 2, proxy-only) |
| DPC/OPC outcomes, cache hit ratio, returned points, origin latency, cache operations / storage / evictions | Phase 7 (Delta Proxy Cache integration) |
| Probe rate + distinct ladders, registry entries by layer, resolution confidence breakdown, fallback reasons, step mispredictions | Phase 9 (the `trickster_graphite_*` metric families; the behaviors they observe land in Phases 5–8) |

The dashboard is final as of Stage B. **An empty panel before its phase is
expected and is that phase's acceptance criterion — do not "fix" it by editing
the dashboard.** The metric names and label values those panels query are
frozen in todo item 3.4 and must be implemented unchanged in Phase 9.

## Included backends

The Compose file brings up Prometheus, InfluxDB 2.x, InfluxDB 3.x, ClickHouse,
Apache Druid, MySQL, TimescaleDB, and Graphite alongside Grafana. Trickster's
dev config registers a matching backend for each,
so Grafana can query the upstream directly or via Trickster for a side-by-side
comparison.

## Seed data

ClickHouse, MySQL, TimescaleDB, and Druid are all loaded with the same
synthetic `trips` dataset: about 1.9 million cab rides in the fictional city of Emberwick over a
12-week window, with the same 45-column schema, label cardinality, and
daily/weekly usage curve as a real ride dataset. Nothing is downloaded: the
`seed_data_generate` service runs `hack/seedgen` with `go run` on the
`golang:1.27-alpine` image, with networking disabled, and writes
`docker-compose-data/seed-data/trips_1.gz`, `trips_2.gz`, and
`seed-window.env`. Every value is a pure function of the row number, so the
uncompressed output is byte-identical on every machine and every run; the
generator verifies its output against a SHA-256 pinned in `hack/seedgen/main.go`
and fails if it drifts. `make seed-verify` runs that check without writing
files, and CI runs it with `GOPROXY=off`.

The timestamps in the files are fixed (12 weeks starting 2024-01-01). Each
database loader shifts them so the midpoint of the window lands on the seed
instant, giving six weeks of past data and six weeks of future data so live
dashboards keep showing fresh points as time passes.

To change the names, boroughs, cab colours, or shares, edit
`hack/seedgen/theme.go`; to change the daily or weekly curve, edit
`hack/seedgen/calendar.go`. After any intentional change, run
`cd hack/seedgen && go run . -verify-only` to print the new hash, pin it in
`main.go`, and run `go test ./...` there. `SEED_PROFILE=small` (or
`-profile small`) produces a tenth of the rows for quick experiments and is
what the CI integration job uses; pass `-force` to regenerate over a cached
output. `make seed-generate` (with optional `SEED_PROFILE=small` and
`SEED_FORCE=1`) runs the generator natively into the same directory without
a container. See `hack/seedgen/README.md`.

`make developer-seed-data` runs `hack/developer-seed-data.sh`, which
regenerates the seed window and then runs every seeder concurrently. Set
`SEED_TARGET` to a space- or comma-separated subset of `clickhouse`, `mysql`,
`timescaledb`, `druid`, and `graphite` to scope the run, for example
`SEED_TARGET=timescaledb make developer-seed-data`. The seed instant is
recomputed on every run, so a scoped re-seed shifts only the selected
databases; the others keep their previous shift, and dashboards that compare
backends will disagree until a full run re-syncs them.

## InfluxDB Details

For InfluxDB 3.x, Trickster also exposes an Apache Arrow Flight SQL (gRPC)
proxy on `:8485` (the `influx3-flight` listener with `protocol: flight-sql`,
mapped from the influx3 backend's `listener_names`). The
`Trickster InfluxDB 3` Grafana dashboard exercises both the HTTP InfluxQL
endpoint and Flight SQL via the InfluxDB datasource in SQL mode, with direct
and Trickster-cached targets on the same panel. An equivalent
`ClickHouse (Grafana Plugin)` dashboard is included for ClickHouse. The
`verify-influxdb3.sh` script runs a quick end-to-end check of the v3 HTTP
and Flight SQL paths through Trickster.

## ClickHouse Details

The environment runs ClickHouse on direct HTTP port `8123` and Native port
`9000`. Trickster's single `click1` backend uses the HTTP origin and is bound to
both its HTTP listener (`8480`, route `/click1/`) and ClickHouse Native listener
(`8487`). This demonstrates two ingress protocols sharing one backend and cache.

The ClickHouse users are `default` with an empty password and `testauth` with
password `trickster`. For a direct Native smoke test through Trickster:

```sh
clickhouse client --host 127.0.0.1 --port 8487 \
  --query "SELECT count() FROM trips"
```

Grafana provisions `clickhouse-grafana-trickster` for HTTP and
`clickhouse-grafana-trickster-native` for Native connections through Trickster.
Both appear in the Data Source selector on the official-plugin dashboard at
<http://127.0.0.1:3000/d/clickhouse-grafana-plugin/clickhouse-grafana-plugin>.
The Vertamedia-plugin dashboard is at
<http://127.0.0.1:3000/d/aekapw5xl2epsc/clickhouse>.

The backend can instead use ClickHouse's Native origin by changing
`origin_url` to `http://127.0.0.1:9000` and setting `protocol: native`. HTTP and
Native clients can use either origin transport. See the
[ClickHouse Support Guide](../../clickhouse.md) for TLS configuration,
delta-cacheable SQL, supported formats and types, and Native limitations.

## Apache Druid Details

The environment runs Apache Druid's single-node nano configuration on direct
port `8888`. Trickster registers the `druid1` backend and exposes it at
`http://127.0.0.1:8480/druid1`. Grafana provisions `druid-direct` and
`druid-trickster` datasources for origin-vs-cache comparison on the dashboard at
<http://127.0.0.1:3000/d/trickster-druid/apache-druid>.

Run `make developer-seed-data` to load the shared synthetic `trips` data
through Druid's native batch-ingestion API. The Druid seeder
(`hack/druidseed`, run with `go run` by the `druid_seed` service) uses the same
generated files and timestamp shift as the ClickHouse, MySQL, and TimescaleDB
seeders, then verifies the row count and shifted minimum and maximum timestamps through
Druid SQL. Before loading, it marks any segments from the previous moving seed
window unused so repeated runs do not accumulate stale rows. It runs in
parallel with the other database seeders after the shared generation step.

The published Druid image contains the nano service scripts but not the Perl
runtime used by its bundled supervisor. `druid-config/start-nano.sh` launches
the same ZooKeeper, Coordinator/Overlord, Broker, Router, Historical, and Middle
Manager processes with Bash inside the one development container.

## MySQL Details

The developer environment includes a pinned MySQL 8.4 (LTS) container seeded
with the same auto-phased synthetic `trips` dataset used by ClickHouse,
TimescaleDB, and Druid. All four seeders read the shared generated files in
`docker-compose-data/seed-data`, so the data is generated once regardless of
which seeder runs first (see [Seed data](#seed-data)).

For the supported production configuration, security, SQL, caching, routing,
and operations contract, see the [MySQL Provider Guide](../../mysql.md).

* Port `3306`: direct MySQL access
* Port `8486`: Trickster's protocol-aware MySQL listener
* Database: `trickster`
* Development-only credentials (provisioned by
  `docker-compose-data/mysql-config/init/01-users.sql`):
  * `root` / `trickster-dev-root` — administration
  * `seeder` / `trickster-dev-seed` — schema creation and seeding
  * `trickster` / `trickster-dev-upstream` — Trickster's upstream connection
    (read-only)
  * `grafana_ro` / `trickster-dev-grafana` — Grafana direct access (read-only)

The generation step records the dataset's pickup/dropoff bounds and derives
one seconds-level shift that places the pickup midpoint at the seed instant.
MySQL, TimescaleDB, and ClickHouse apply that exact shift to every pickup and
dropoff datetime and regenerate the related date columns; Druid applies it to the
primary `__time` timestamp. This preserves trip durations and partition/date
relationships in the relational copies while placing approximately half of the
pickup distribution before and half after the seed instant. To re-seed (for
example, after the data ages out of range), run `make developer-seed-data`,
which first runs the `seed_data_generate` service and then reloads ClickHouse,
MySQL, TimescaleDB, and Druid in parallel. A Trickster started before the re-seed still
holds the previous timeseries in its memory cache, so restart `make serve-dev`
afterwards (or compare against a `-direct` datasource) to see the new data.

The provisioned Grafana MySQL dashboard is at
<http://127.0.0.1:3000/d/trickster-mysql/mysql>. Its Data Source variable can
switch between `mysql-direct` and `mysql-trickster`. The default
relative time range shows the past half of the seeded data; use an absolute
future range to inspect the future half. Its performance panels are fixed to
the `mysql1` backend and `fs1` cache, keeping their metrics separate from other
backends and caches. The dashboard also shows SQL classifications and rewrite
failures, OPC/DPC outcomes, request latency and returned elements, and cache
operations, storage, and evictions.
The listener's `connections_limit` controls accepted downstream connections.
The initial protocol implementation maintains one upstream connection per
downstream connection so transaction, database, and session state cannot leak
between clients. MySQL negotiates inbound TLS in-band on port `8486`; it does
not use a separate `tls_port`. Downstream users come from the backend's named
authenticator; MySQL native-password authentication currently requires those
authenticator entries to contain plaintext credentials rather than htpasswd
hashes.

Deterministic `SELECT` results are cached through the Object Proxy Cache. A
half-open, cadence-aligned time-series query that matches the supported Grafana
grouping forms uses the Delta Proxy Cache, so expanding a dashboard range only
fetches missing buckets from MySQL. MySQL uses the backend's common
`timeseries_ttl`, `timeseries_retention_factor`, `backfill_tolerance`,
`backfill_tolerance_points`, `max_object_size_bytes`, `cache_name`, and
`cache_key_prefix` settings. Queries in transactions, and connections that
have changed unsupported session state or executed mutations, bypass the
cache. Grafana's literal `SET time_zone` initialization is supported and the
selected time zone is included in the cache identity.

The provisioned MySQL dashboard uses Grafana's `$__timeFrom()` and
`$__timeTo()` macros as a half-open range and rounds its default time picker to
hour boundaries. This keeps the time-series panels cadence-aligned for DPC.
Unrounded and live-ending half-open ranges also use DPC. Trickster rounds the
lower bound up and the upper bound down to the query cadence, so only complete
chart buckets inside the requested range are cached. A range containing no
complete bucket normalizes to an empty range. Refreshes within the same cadence
window can therefore be full cache hits. The limited top-N table panel uses OPC
by design.

MySQL cache outcomes are exported as
`trickster_sql_query_cache_total{backend_name="mysql1",cache_mode,cache_status}`.
The backing cache exports operations and storage independently because one
cache can serve several backends: inspect `trickster_cache_operation_objects_total`,
`trickster_cache_operation_bytes_total`, `trickster_cache_usage_objects`,
`trickster_cache_usage_bytes`, and `trickster_cache_events_total` with
`cache_name="mem1"`. Evictions appear in `trickster_cache_events_total` with
`event="eviction"`.
Native MySQL outcomes are also mirrored into
`trickster_proxy_requests_total{backend_name="mysql1",provider="mysql"}` so
the standard Trickster cache-status dashboard panels include this backend.
The `mysql1` backend also uses Trickster's shared health-check scheduler to
open a bounded native connection with its configured origin credentials and
TLS policy and execute `COM_PING`. Its state appears in the management health
response with the same thresholds and lifecycle as HTTP backends; failures are
reported as sanitized authentication, TLS, timeout, refused-connection, or
server-error categories.

Troubleshooting readiness: the `mysql` service has a healthcheck based on
`mysqladmin ping`; the seeder and Grafana wait for it to report healthy. If
seeding fails, inspect logs with
`docker compose logs mysql mysql_seed` from `docs/developer/environment`, then
re-run `make developer-seed-data`. The seed is idempotent and always truncates
and reloads the `trips` table.

## TimescaleDB Details

The developer environment includes a pinned TimescaleDB container
(`timescale/timescaledb:2.30.1-pg18`: TimescaleDB 2.30.1 on PostgreSQL 18)
seeded with the same auto-phased synthetic `trips` dataset used by ClickHouse,
MySQL, and Druid (see [Seed data](#seed-data)). The image is the Timescale
License (TSL) build rather than the Apache-only `-oss` one, so
`time_bucket_gapfill`, continuous aggregates, and compression are available
for testing.

Trickster serves TimescaleDB through its PostgreSQL wire-protocol listener.
It relays everything a client sends, and answers eligible `SELECT` statements
from the backend's cache (`mem1` here): whole results through the Object Proxy
Cache, and time-bucketed range queries through the Delta Proxy Cache, which
fetches from the origin only the buckets it does not already hold. The direct
path is the reference that the proxied path is verified against.

* Port `5432`: direct PostgreSQL access
* Port `8488`: Trickster's PostgreSQL wire-protocol listener (`timescaledb1`,
  `protocol: postgres`)
* Database: `trickster`
* Server time zone: `UTC`, set explicitly. Grafana's PostgreSQL data source
  has no time zone option and sends none at connection startup, so its
  sessions inherit this server default.
* Development-only credentials (provisioned by
  `docker-compose-data/timescaledb-config/init/01-users.sql`):
  * `postgres` / `trickster-dev-root` — administration
  * `seeder` / `trickster-dev-seed` — schema creation and seeding
  * `trickster` / `trickster-dev-upstream` — Trickster's upstream connection
    (read-only)
  * `grafana_ro` / `trickster-dev-grafana` — Grafana direct access (read-only)

The read-only roles receive `SELECT` through the seeder's default privileges,
so dropping and re-creating the table on every seed never needs a re-grant.
Authentication is SCRAM-SHA-256, the PostgreSQL default, for every connection
that arrives over the network, including the published port `5432`. The
image's `pg_hba.conf` trusts the container's own loopback and unix socket, so
the smoke test below connects by service name to exercise the password:

```sh
docker compose exec -e PGPASSWORD=trickster-dev-grafana timescaledb \
  psql -h timescaledb -U grafana_ro -d trickster -c "SELECT count(*) FROM trips"
```

`trips` is a hypertable partitioned on `pickup_datetime` with the default
7-day chunks (13 for the 12-week dataset). It has the same columns as the
MySQL table, with `pickup_datetime` and `dropoff_datetime` stored as
`timestamptz`, plus TimescaleDB's default time index and an index on
`(cab_type, pickup_datetime)`. PostgreSQL's `COPY` cannot transform values
while loading the way MySQL's `LOAD DATA ... SET` does, so the seeder copies
each file into an `UNLOGGED` all-text staging table and then moves the rows
into the hypertable with an `INSERT ... SELECT` that applies the shift. It
then validates the same facts as the MySQL seeder (row count, shifted bounds,
centering on seed time, date/datetime agreement, indexes) along with the chunk
count and the read-only grants.

Grafana provisions the `timescaledb-direct` and `timescaledb-trickster` data
sources using its bundled PostgreSQL plugin with the TimescaleDB option
enabled, so `$__timeGroup` expands to `time_bucket(...)`. The dashboard is at
<http://127.0.0.1:3000/d/trickster-timescaledb/timescaledb>, and its Data
Source variable switches between the two; every panel must render identically
through both. It has the same
panels as the MySQL dashboard and, because both databases hold the same rows,
shows the same values over the same time range. Two panel queries differ from
the MySQL versions only in dialect: the card-use rate counts with
`FILTER (WHERE ...)`, and `round(avg(x), 2)` casts the average to `numeric`.
The Trickster Performance row shows the `timescaledb1` backend's request
duration, returned elements, SQL classifications
(`trickster_sql_query_analysis_total{backend_name="timescaledb1",dialect="postgres",cache_mode,reason}`),
cache outcomes
(`trickster_sql_query_cache_total{backend_name="timescaledb1",cache_mode,cache_status}`)
and rewrite failures. The dashboard's `time_bucket` panels report
`delta / delta_cacheable`: a refresh fetches only the buckets the cache does not
hold yet. The top-N table is cached as a whole object
(`object / unsupported_limit`), keyed on the statement's exact text, so it is a
hit only while its range is unchanged.

These bucket expressions use the delta cache:

| Bucket | Notes |
| --- | --- |
| `time_bucket(width, col)` | what `$__timeGroup` writes with the TimescaleDB option on; the grid starts on Monday 2000-01-03 UTC |
| `time_bucket(width, col, TIMESTAMPTZ '...')`, `time_bucket(width, col, '...'::interval)` | a typed origin or offset; an untyped third argument is the time zone overload and is not matched |
| `time_bucket_gapfill(width, col)` | only for a single series with both range bounds in `WHERE`, and without `locf` or `interpolate` |
| `floor(extract(epoch from col)/N)*N` | what `$__timeGroup` writes with the option off; also with `date_part('epoch', col)` |
| `floor((col)/N)*N` | `$__unixEpochGroup`, over a column of epoch seconds with epoch-second bounds |
| `date_bin(width, col, origin)` | PostgreSQL requires the origin |
| `date_trunc('unit', col)` | only while the session `TimeZone` is UTC, since PostgreSQL truncates in the session zone |

A width is a fixed-length interval such as `'300.000s'`, `'5 minutes'`,
`INTERVAL '1 hour'` or `'7 days'`; months and years never match. A range whose
bounds are not on the bucket grid, as Grafana's live ranges are, is rounded
inward, so the partial first and last buckets are left out of the answer. A
bound written without a zone (`'2026-09-17 00:00:00'`, `TIMESTAMP '...'`) is
read in the session zone by PostgreSQL, so it qualifies only while that zone
is UTC; Grafana's `Z`-suffixed bounds always do.

A statement that fits none of these is cached as a whole object with the
reason in the classification metric. So is one the SQL parser cannot write
back faithfully (`FROM ONLY`, a `json` cast, a `U&'...'` string), one that calls
a set-returning function, and a gapfill outside the form above. A statement
that calls a volatile function or reads the clock anywhere but in a time bound
(`random()`, `now()`, `current_date`, `pg_sleep()`, `nextval()`, ...) is never
cached (`none / nondeterministic`). Statements the parser rejects outright,
such as named-argument calls (`origin => ...`) or `GROUP BY ROLLUP`, are
cached as objects too.

Cached answers are the origin's own bytes: the row description and every row
are stored exactly as received, and only the bucket column is read, to place
rows on the time axis. That is safe because everything that changes how a
value is rendered (`TimeZone`, `DateStyle`, `extra_float_digits`, and the rest
of the session identity below) partitions the cache. The delta cache needs the
bucket to be the leading `ORDER BY` term (either direction) or no `ORDER BY` at
all; within a bucket rows keep the origin's order. It uses the backend's common
`timeseries_ttl`, `timeseries_retention_factor`, `backfill_tolerance`,
`backfill_tolerance_points`, sharding, `max_object_size_bytes`, `cache_name`
and `cache_key_prefix` settings, and an open-ended range is never considered
complete in its newest bucket.

The cache always fails open to a plain relay of the client's own statement: when
the origin rejects Trickster's rewritten sub-query
(`trickster_sql_query_rewrite_failures_total{reason="origin_rejected"}`), when
a result outgrows the backend's `postgres.max_result_rows` (100000) or
`postgres.max_result_size_bytes` (64 MiB) limits (`reason="result_size"`), or
when the bucket column cannot be read, for example under a non-ISO `DateStyle`
(the statement is then cached as an object instead). Such a statement skips
the cache until its marker expires, so it is not retried on every refresh. An
oversized result of the client's own statement is handed back to the relay
mid-stream rather than fetched twice. Errors from the origin are passed to the
client verbatim and never cached, and a cancel request reaches a statement
that is being fetched for the cache.

Only a simple-protocol `Query` holding one row-returning statement is
analyzed, and so cached. Others are relayed and counted with `cache_mode="none"` and the
reason they were passed over: `multi_statement`, `in_transaction`,
`pipelined`, `query_size` (larger than the listener's
`postgres.max_query_size_bytes`, 1 MiB by default), or `session_state`.
Extended-protocol statements are relayed and never analyzed. A relayed
statement counts as a `proxy-only` request. A driver's pool keepalive does not:
pgx, which Grafana uses, sends an empty `-- ping` query before each panel
query, and it is relayed to the origin but left out of the request metrics. Setting
`proxy_only: true` on the backend switches statement inspection and caching
off entirely.

`session_state` means the session did something whose effect on later results
Trickster cannot follow, and it stays that way until the client reconnects.
Trickster follows the settings the origin announces (`TimeZone`, `DateStyle`,
`IntervalStyle`, `client_encoding`, `search_path` on PostgreSQL 18, and
others), plus `role`, `extra_float_digits` and `bytea_output` from the
client's own `SET` statements; all of them, with the user and database, will
partition the cache. Any other `SET` (for example a custom `app.tenant` used
by row-level security), `set_config()`, `SELECT ... INTO`, DDL, `DO`, `CALL`,
and fast-path function calls end caching for the session. Transactions, DML,
`SHOW`, `EXPLAIN`, and harmless settings such as `statement_timeout` and
`application_name` do not. A session with `standard_conforming_strings` off is
also relayed uncached, because its string constants follow other rules than the
analyzer reads them by. One known gap: a user-defined function that changes
a session setting as a side effect is not detected.

The `timescaledb1` backend uses `provider: timescaledb`, an alias of
`postgres`: both names select the same engine, and metrics report
`provider="postgres"`. A `postgres` listener maps to exactly one backend (or one User
Router, below) and keeps one upstream connection per client connection, so session state never
crosses clients. The backend's authenticator decides how clients log in:

* With `authenticator_name` (as configured here, `timescaledb-grafana`),
  Trickster authenticates the client itself and then opens the origin session
  with the `origin_url` credentials. It offers SCRAM-SHA-256, plus
  SCRAM-SHA-256-PLUS when the listener has a certificate. A user whose stored
  credential is a crypt hash or a PostgreSQL `md5` verifier needs a cleartext
  password, which is refused without TLS unless the listener sets
  `postgres.allow_cleartext_without_tls` (or `postgres.allow_md5` for the
  legacy md5 method). Authenticator entries may be plaintext or PostgreSQL
  `SCRAM-SHA-256$...` verifiers copied from `pg_authid`.
* Without one, or with `observe_only: true`, the client's own authentication
  exchange is relayed to the origin untouched and Trickster holds no
  credentials. SCRAM-SHA-256-PLUS cannot succeed in this mode when both the
  listener and the origin use TLS, because the client binds to Trickster's
  certificate rather than the origin's.

The backend's `healthcheck` block makes Trickster log in to the origin every
5 seconds on a fresh connection, with the `origin_url` credentials, and wait
for `ReadyForQuery`. A `postgres` listener can also front a User Router ALB
whose targets are `postgres`/`timescaledb` backends: Trickster authenticates
the client against the router's authenticator, routes on the startup user, and
pins the session to that backend
(`trickster_pgwire_route_selections_total{router_name,backend_name,outcome}`).
The dev config has no router; `examples/conf/postgres-user-router.yaml` is a
complete one.

TLS is negotiated in-band on port `8488` (there is no separate `tls_port`),
and TLS to the origin is independent of it: set the backend's
`postgres.upstream_tls_mode` to `disable` (the default), `require`,
`verify-ca`, or `verify-full`. Cancel requests work through the listener:
clients receive a Trickster-issued cancellation key that is mapped to the
origin's, and a client that disconnects mid-query has its statement canceled
at the origin. For a smoke test through Trickster (with `make serve-dev`
running):

```sh
docker compose exec -e PGPASSWORD=trickster-dev-grafana timescaledb \
  psql -h host.docker.internal -p 8488 -U grafana_ro -d trickster \
  -c "SELECT current_user, count(*) FROM trips"
```

`current_user` reports `trickster`, the origin role, because the listener
terminates authentication.

Troubleshooting readiness: the `timescaledb` service has a healthcheck based on
`pg_isready` over TCP, which only succeeds once the image's one-time
initialization has finished; the seeder and Grafana wait for it to report
healthy. If seeding fails, inspect logs with
`docker compose logs timescaledb timescaledb_seed` from
`docs/developer/environment`, then re-run `make developer-seed-data`. The seed
is idempotent and always drops, re-creates, and reloads the `trips` table.
PostgreSQL 18 images keep their data under `/var/lib/postgresql/18/docker`, so
the `timescaledb-data` volume is mounted at `/var/lib/postgresql`; the init SQL
only runs when that volume is empty (`make developer-delete` resets it).
