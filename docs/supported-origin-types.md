# Supported Origin Types

Trickster currently supports the following Origin Types:

<img src="./images/external/prom_logo_60.png" width=16 /> Prometheus

<img src="./images/external/influx_logo_60.png" width=16 /> InfluxDB

<img src="./images/external/irondb_logo_60.png" width=16 /> Circonus IRONdb


### Prometheus

Trickster fully supports the [Prometheus HTTP API (v1)](https://prometheus.io/docs/prometheus/latest/querying/api/). You can explicitly specify `prometheus` as the Origin Type when configuring Trickster, but that is the default when not provided since Trickster was originally developed as a Prometheus accelerator.

### InfluxDB

Trickster 1.0 Beta has experimental support for InfluxDB. Once Trickster 1.0 leaves beta and has a GA release, InfluxDB will be fully supported. Specify `influxdb` as the Origin Type when configuring Trickster.

### Circonus IRONdb

Experimental support has been included for the Circonus IRONdb time-series database. If Grafana is used for visualizations, the Circonus IRONdb data source plug-in for Grafana can be configured to use Trickster as its data source. All IRONdb data retrieval operations, including CAQL queries, are supported.

When configuring an IRONdb origin, specify `irondb` as the origin type in the Trickster configuration. The `host` value can be set directly to the address and port of an IRONdb node, but it is recommended to use the Circonus API proxy service. When using the proxy service, set the `host` value to the adress and port of the proxy service, and set the `api_path` value to `'irondb'`.
