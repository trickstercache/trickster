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
all data.

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
| Trickster backend | `graphite1` in `trickster-config/trickster.yaml` (provider `graphite`, dedicated `graphite_fs` cache, `max_object_size_bytes` raised because native-resolution fetches of wide windows exceed the 512KB default); health at <http://127.0.0.1:8481/trickster/health/graphite1> |
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

