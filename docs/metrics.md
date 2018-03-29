# Trickster Metrics

Trickster exposes a Prometheus /metrics endpoint with a customizable listener port number (default is 8082). For more information on customizing the metrics configuration, see [configuring.md](configuring.md).

The following metrics are available for polling:

* `trickster_requests_total` (Counter) - The total number of requests Trickster has handled.
  * labels:
    * `method` - 'query' or 'query_range'
    * `status` - 'hit', 'phit', (partial hit) 'kmiss', (key miss) 'rmiss' (range miss)


* `trickster_points_total` (Counter) - The total number of data points Trickster has handled.
  * labels:
    * `status` - 'hit', 'phit', (partial hit) 'kmiss', (key miss) 'rmiss' (range miss)


* `trickster_proxy_duration_ms` (Histogram) - Time required to proxy a given Prometheus query.
  * labels:
    * `method` - 'query' or 'query_range'
    * `status` - 'hit', 'phit', (partial hit) 'kmiss', (key miss) 'rmiss' (range miss)

In addition to these custom metrics, Trickster also exposes the standard Prometheus metrics that are part of the [client_golang](https://github.com/prometheus/client_golang) package, including memory and cpu utilization, etc.
