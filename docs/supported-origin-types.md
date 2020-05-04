# Supported Origin Types

Trickster currently supports the following Origin Types:

### <img src="./images/logos/trickster-logo.svg" width=16 /> Generic HTTP Reverse Proxy Cache

Trickster operates as a fully-featured and highly-customizable reverse proxy cache, designed to accellerate and scale upstream endpoints like API services and other simple http services. Specify `'reverseproxycache'` or just `'rpc'` as the Origin Type when configuring Trickster.

---

## Time Series Databases

### <img src="./images/external/prom_logo_60.png" width=16 /> Prometheus

Trickster fully supports the [Prometheus HTTP API (v1)](https://prometheus.io/docs/prometheus/latest/querying/api/). Specify `'prometheus'` as the Origin Type when configuring Trickster.

### <img src="./images/external/influx_logo_60.png" width=16 /> InfluxDB

Trickster 1.0 has support for InfluxDB. Specify `'influxdb'` as the Origin Type when configuring Trickster.

See the [InfluxDB Support Document](./influxdb.md) for more information.

### <img src="./images/external/clickhouse_logo.png" width=16 /> ClickHouse

Trickster 1.0 has support for ClickHouse. Specify `'clickhouse'` as the Origin Type when configuring Trickster.

See the [ClickHouse Support Document](./clickhouse.md) for more information.

### <img src="./images/external/irondb_logo_60.png" width=16 /> Circonus IRONdb

Support has been included for the Circonus IRONdb time-series database. If Grafana is used for visualizations, the Circonus IRONdb data source plug-in for Grafana can be configured to use Trickster as its data source. All IRONdb data retrieval operations, including CAQL queries, are supported.

When configuring an IRONdb origin, specify `'irondb'` as the origin type in the Trickster configuration. The `host` value can be set directly to the address and port of an IRONdb node, but it is recommended to use the Circonus API proxy service. When using the proxy service, set the `host` value to the address and port of the proxy service, and set the `api_path` value to `'irondb'`.
