# Supported Providers

Trickster currently supports the following Providers:

### <img src="./images/logos/trickster-logo.svg" width=16 /> Generic HTTP Reverse Proxy Cache

Trickster operates as a fully-featured and highly-customizable reverse proxy cache, designed to accelerate and scale upstream endpoints like API services and other simple http services. Specify `'reverseproxycache'` or just `'rpc'` as the Provider when configuring Trickster.

---

## Time Series Databases

### <img src="./images/external/prom_logo_60.png" width=16 /> Prometheus

Trickster fully supports the [Prometheus HTTP API (v1)](https://prometheus.io/docs/prometheus/latest/querying/api/). Specify `'prometheus'` as the Provider when configuring Trickster. Trickster supports [label injection](./prometheus.md) for Prometheus.

### <img src="./images/external/influx_logo_60.png" width=16 /> InfluxDB

Trickster supports for InfluxDB. Specify `'influxdb'` as the Provider when configuring Trickster.

See the [InfluxDB Support Document](./influxdb.md) for more information.

### <img src="./images/external/clickhouse_logo.png" width=16 /> ClickHouse

Trickster supports accelerating ClickHouse time series. Specify `'clickhouse'` as the Provider when configuring Trickster.

See the [ClickHouse Support Document](./clickhouse.md) for more information.
