# Trickster Metrics

Trickster exposes a Prometheus /metrics endpoint with a customizable listener port number (default is 8082). For more information on customizing the metrics configuration, see [configuring.md](configuring.md).

---

The following metrics are available for polling with any Trickster configuration:

* `trickster_frontend_requests_total` (Counter) - Count of front end requests handled by Trickster
  * labels:
    * `origin_name` - the name of the configured origin handling the proxy request$
    * `origin_type` - the type of the configured origin handling the proxy request
    * `method` - the HTTP Method of the proxied request
    * `http_status` - The HTTP response code provided by the origin
    * `path` - the Path portion of the requested URL

* `trickster_frontend_requests_duration_seconds` (Histogram) - Histogram of front end request durations handled by Trickster
  * labels:
    * `origin_name` - the name of the configured origin handling the proxy request$
    * `origin_type` - the type of the configured origin handling the proxy request
    * `method` - the HTTP Method of the proxied request
    * `http_status` - The HTTP response code provided by the origin
    * `path` - the Path portion of the requested URL

* `trickster_frontend_written_byte_total` (Counter) - Count of bytes written in front end requests handled by Trickster`
  * labels:
    * `origin_name` - the name of the configured origin handling the proxy request$
    * `origin_type` - the type of the configured origin handling the proxy request
    * `method` - the HTTP Method of the proxied request
    * `http_status` - The HTTP response code provided by the origin
    * `path` - the Path portion of the requested URL



* `trickster_proxy_requests_total` (Counter) - The total number of requests Trickster has handled.
  * labels:
    * `origin_name` - the name of the configured origin handling the proxy request$
    * `origin_type` - the type of the configured origin handling the proxy request
    * `method` - the HTTP Method of the proxied request
    * `cache_status` - 'hit', 'phit', (partial hit) 'kmiss', (key miss) 'rmiss' (range miss)
    * `http_status` - The HTTP response code provided by the origin
    * `path` - the Path portion of the requested URL

* `trickster_proxy_points_total` (Counter) - The total number of data points Trickster has handled.
  * labels:
    * `origin_name` - the name of the configured origin handling the proxy request$
    * `origin_type` - the type of the configured origin handling the proxy request
    * `cache_status` - 'hit', 'phit', (partial hit) 'kmiss', (key miss) 'rmiss' (range miss)
    * `path` - the Path portion of the requested URL

* `trickster_proxy_request_duration_seconds` (Histogram) - Time required to proxy a given Prometheus query.
  * labels:
    * `origin_name` - the name of the configured origin handling the proxy request$
    * `origin_type` - the type of the configured origin handling the proxy request
    * `method` - the HTTP Method of the proxied request
    * `cache_status` - 'hit', 'phit', (partial hit) 'kmiss', (key miss) 'rmiss' (range miss)
    * `http_status` - The HTTP response code provided by the origin
    * `path` - the Path portion of the requested URL

* `trickster_proxy_max_connections` (Gauge) - Trickster max number of allowed concurrent connections

* `trickster_proxy_active_connections` (Gauge) - Trickster number of concurrent connections

* `trickster_proxy_requested_connections_total` (Counter) - Trickster total number of connections requested by clients.

* `trickster_proxy_accepted_connections_total` (Counter) - Trickster total number of accepted client connections.

* `trickster_proxy_closed_connections_total` (Counter) - Trickster total number of administratively closed client connections.

* `trickster_proxy_failed_connections_total` (Counter) - Trickster total number of failed client connections.

* `trickster_cache_operation_objects_total` (Counter) - The total number of objects upon which the Trickster cache has operated.
  * labels:
    * `cache_name` - the name of the configured cache performing the operation$
    * `cache_type` - the type of the configured cache performing the operation
    * `operation` - the name of the operation being performed (read, write, etc.)
    * `status` - the result of the operation being performed


* `trickster_cache_operation_bytes_total` (Counter) - The total number of bytes upon which the Trickster cache has operated.
  * labels:
    * `cache_name` - the name of the configured cache performing the operation$
    * `cache_type` - the type of the configured cache performing the operation
    * `operation` - the name of the operation being performed (read, write, etc.)
    * `status` - the result of the operation being performed

---

The following metrics are available only for Caches Types whose object lifecycle Trickster manages internally (Memory, Filesystem and bbolt):

* `trickster_cache_events_total` (Counter) - The total number of events that change the Trickster cache, such as retention policy evictions.
  * labels:
    * `cache_name` - the name of the configured cache experiencing the event$
    * `cache_type` - the type of the configured cache experiencing the event
    * `event` - the name of the event being performed
    * `reason` - the reason the event occurred

* `trickster_cache_usage_objects` (Gauge) - The current count of objects in the Trickster cache.
  * labels:
    * `cache_name` - the name of the configured cache$
    * `cache_type` - the type of the configured cache$

* `trickster_cache_usage_bytes` (Gauge) - The current count of bytes in the Trickster cache.
  * labels:
    * `cache_name` - the name of the configured cache$
    * `cache_type` - the type of the configured cache$

* `trickster_cache_max_usage_objects` (Gauge) - The maximum allowed size of the Trickster cache in objects.
  * labels:
    * `cache_name` - the name of the configured cache$
    * `cache_type` - the type of the configured cache

* `trickster_cache_max_usage_bytes` (Gauge) - The maximum allowed size of the Trickster cache in bytes.
  * labels:
    * `cache_name` - the name of the configured cache$
    * `cache_type` - the type of the configured cache

---

In addition to these custom metrics, Trickster also exposes the standard Prometheus metrics that are part of the [client_golang](https://github.com/prometheus/client_golang) metrics instrumentation package, including memory and cpu utilization, etc.
