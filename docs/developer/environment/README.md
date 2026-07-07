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
The Kibana frontend is available at <http://127.0.0.1:5601> and is configured
to use Trickster's `es1` Elasticsearch backend at
`http://host.docker.internal:8480/es1`.

The data in this dashboard is polled by Prometheus from your local Trickster
dev instance. So the longer you keep this dashboard up and refreshing, the more
you can test out Trickster acceleration features. You can change the Data Source
selector to go between various Trickster configs, or bypass Trickster altogether
for verification purposes.

Elasticsearch is seeded on startup with the `trickster-dev-logs` index. The seed
data includes recent and older `@timestamp` values so developers can verify
Elasticsearch date histogram caching through Trickster.
After Kibana connects through Trickster, the Kibana seed container creates the
`Trickster Dev Logs` data view, saved search, and dashboard for that index. The
dashboard is available at
<http://127.0.0.1:5601/app/dashboards#/view/trickster-dev-logs-dashboard>.

You can stop the developer environment by running `make developer-stop`. To
delete the developer environment, run `make developer-delete` which will destroy
all data, including the MySQL data volume and any other named volumes.

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
The provisioned ClickHouse dashboard mirrors the same five taxi-data panels so
the database demonstrations can be compared directly.

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
