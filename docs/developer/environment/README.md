# Setting up a Local Developer Environment

## Prerequisites

* Docker is installed and running, and the `docker` command is available.
* Golang 1.26 is installed
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

## ClickHouse

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

## MySQL

The developer environment includes a pinned MySQL 8.4 (LTS) container seeded
with the same auto-phased NYC taxi `trips` dataset used by ClickHouse. The
seeder shares the ClickHouse seeder's download cache
(`docker-compose-data/clickhouse-config/seeding/data`), so the source files are
only downloaded once regardless of which seeder runs first.

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

The shared fetch step scans the source files for their actual pickup/dropoff
bounds and derives one seconds-level shift that places the pickup midpoint at
the seed instant. MySQL and ClickHouse apply that exact shift to every pickup
and dropoff datetime and regenerate the related date columns. This preserves
trip durations and partition/date relationships while placing approximately
half of the pickup distribution before and half after the seed instant. To
re-seed (for example, after the data ages out of range), run
`make developer-seed-data`, which first populates the shared download cache
via the `seed_data_fetch` service and then truncates and reloads ClickHouse
and MySQL in parallel. The MySQL server and Grafana data-source sessions both
run in UTC. Each seeder fails the workflow unless row count, exact shifted
timestamp bounds, date consistency, and the database's expected query keys all
validate after loading.

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
