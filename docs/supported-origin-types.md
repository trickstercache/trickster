# Supported Origin Types

Trickster currently supports the following Origin Types:

<img src="./images/external/prom_logo_60.png" width=16 /> Prometheus

<img src="./images/external/influx_logo_60.png" width=16 /> InfluxDB


### Prometheus

Trickster fully supports the [Prometheus HTTP API (v1)](https://prometheus.io/docs/prometheus/latest/querying/api/). You can explicitly specify `prometheus` as the Origin Type when configuring Trickster, but that is the default when not provided since Trickster was originally developed as a Prometheus accelerator.

### InfluxDB

Trickster 1.0 Beta has experimental support for InfluxDB. Once Trickster 1.0 leaves beta and has a GA release, InfluxDB will be fully supported. Specify `influxdb` as the Origin Type when configuring Trickster.
