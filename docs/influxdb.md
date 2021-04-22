# InfluxDB Support

Trickster provides support for accelerating InfluxDB queries that return time series data normally visualized on a dashboard. Acceleration works by using the Time Series Delta Proxy Cache to minimize the number and time range of queries to the upstream InfluxDB server.

## Scope of Support

Trickster is tested with the built-in [InfluxDB DataSource Plugin for Grafana](https://grafana.com/grafana/plugins/influxdb) v5.0.0.

Trickster uses InfluxDB-provided packages to parse and normalize queries for caching and acceleration. If you find query or response structures that are not yet supported, or providing inconsistent or unexpected results, we'd love for you to report those so we can further improve our InfluxDB support.

Trickster supports integrations with InfluxDB 1.x and 2.0, however, the Flux language is not currently supported.
