# Supported Providers

Trickster currently supports the following Providers:

### <img src="./images/logos/trickster-logo.svg" width=24 /> Generic HTTP Reverse Proxy Cache

Trickster operates as a fully-featured and highly-customizable reverse proxy cache, designed to accelerate and scale upstream endpoints like API services and other simple http services. Specify `'reverseproxycache'` or just `'rpc'` as the Provider when configuring Trickster.

### <img src="./images/logos/trickster-logo.svg" width=24 /> Static File Server

Trickster can serve the contents of a local directory itself, with no upstream origin, for hosting websites and other static content. Specify `'static'` as the Provider when configuring Trickster. See the [Static File Server Document](./static.md) for more information.

---

## Time Series Databases

### <img src="./images/external/prom_logo_60.png" width=24 /> Prometheus

Trickster fully supports the [Prometheus HTTP API (v1)](https://prometheus.io/docs/prometheus/latest/querying/api/), including Prometheus 3.x features like native histograms and UTF-8 metric names. Specify `'prometheus'` as the Provider when configuring Trickster. See the [Prometheus Support Document](./prometheus.md) for more information.

### <img src="./images/external/clickhouse_logo.png" width=24 /> ClickHouse

Trickster supports accelerating ClickHouse time series over both HTTP and the ClickHouse native binary protocol (port 9000), and is tested against the Vertamedia and official Grafana ClickHouse (v4+) datasource plugins. Specify `'clickhouse'` as the Provider when configuring Trickster.

See the [ClickHouse Support Document](./clickhouse.md) for more information.

### <img src="./images/external/influx_logo_60.png" width=24 /> InfluxDB

Trickster supports InfluxDB 1.x, 2.x, and 3.x. Specify `'influxdb'` as the Provider when configuring Trickster.

See the [InfluxDB Support Document](./influxdb.md) for more information.

### <img src="./images/external/druid-logo.svg" width=24 /> Apache Druid

Trickster accelerates fixed-width native JSON `timeseries`, `groupBy`, and
`topN` queries plus eligible `TIME_FLOOR` SQL queries, with safe Object Proxy
Cache fallback for other read-query shapes. Specify `'druid'` as the Provider
when configuring Trickster.

See the [Apache Druid Support Document](./druid.md) for more information.

### <img src="./images/external/graphite-logo.svg" width=24 /> Graphite

Trickster accelerates Graphite's render API, including graphite-web, go-carbon
and other Graphite-protocol backends. Specify `'graphite'` as the Provider when
configuring Trickster.

See the [Graphite Support Document](./graphite.md) for more information.

### <img src="./images/external/timescaledb_logo.svg" width=24 /> PostgreSQL and TimescaleDB

Trickster accepts native PostgreSQL wire-protocol connections and accelerates
time-bucketed queries against PostgreSQL and TimescaleDB, including the
statements Grafana's built-in PostgreSQL data source sends. Specify `postgres`
(or its alias `timescaledb`) as the provider and expose it through a listener
with `protocol: postgres`.

See the [PostgreSQL and TimescaleDB Provider Guide](./postgres.md) for the
supported clients, SQL, authentication, TLS, caching, routing, and operations
contract.

### <img src="./images/external/greptime-logo.svg" width=24 /> GreptimeDB

Trickster accelerates eligible GreptimeDB SQL queries over HTTP, PostgreSQL
and MySQL, plus its Prometheus-compatible range API. Specify `greptimedb` as
the provider and map native listeners explicitly. See the
[GreptimeDB Provider Guide](./greptimedb.md) for configuration, cache eligibility,
Grafana macros, authentication and upstream compatibility limits.

### <img src="./images/external/mysql_logo_60.png" width=24 /> MySQL

Trickster supports protocol-aware acceleration for supported MySQL
servers and Grafana's built-in MySQL data source. Specify `mysql` as the direct
terminal provider and expose it through a listener with `protocol: mysql`.

See the [MySQL Provider Guide](./mysql.md) for the supported server, client,
SQL, authentication, TLS, caching, routing, and operations contract.
