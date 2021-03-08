# Trickster Metrics

Trickster exposes a Prometheus /metrics endpoint with a customizable listener port number (default is 8481). For more information on customizing the metrics configuration, see [configuring.md](configuring.md).

---

The following metrics are available for polling with any Trickster configuration:

* `trickster_build_info` (Gauge) - This gauge is always 1 when Trickster is running
  * labels:
    * `goversion` - the version of go under which the running Trickster binary was built
    * `revision` - the commit ID on which the running Trickster binary was built
    * `version` - semantic version of the running Trickster binary

* `trickster_config_last_reload_successful` (Gauge) - The value is 1 when true (the last config reload was successful) or 0 when false

* `trickster_config_last_reload_success_time_seconds` (Gauge) - Epoch timestamp of the last successful configuration reload

* `trickster_frontend_requests_total` (Counter) - Count of front end requests handled by Trickster
  * labels:
    * `backend_name` - the name of the configured backend handling the proxy request
    * `provider` - the type of the configured backend handling the proxy request
    * `method` - the HTTP Method of the proxied request
    * `http_status` - The HTTP response code provided by the backend
    * `path` - the Path portion of the requested URL

* `trickster_frontend_requests_duration_seconds` (Histogram) - Histogram of front end request durations handled by Trickster
  * labels:
    * `backend_name` - the name of the configured backend handling the proxy request
    * `provider` - the type of the configured backend handling the proxy request
    * `method` - the HTTP Method of the proxied request
    * `http_status` - The HTTP response code provided by the backend
    * `path` - the Path portion of the requested URL

* `trickster_frontend_written_byte_total` (Counter) - Count of bytes written in front end requests handled by Trickster
  * labels:
    * `backend_name` - the name of the configured backend handling the proxy request
    * `provider` - the type of the configured backend handling the proxy request
    * `method` - the HTTP Method of the proxied request
    * `http_status` - The HTTP response code provided by the backend
    * `path` - the Path portion of the requested URL

* `trickster_proxy_requests_total` (Counter) - The total number of requests Trickster has handled.
  * labels:
    * `backend_name` - the name of the configured backend handling the proxy request
    * `provider` - the type of the configured backend handling the proxy request
    * `method` - the HTTP Method of the proxied request
    * `cache_status` - status codes are described [here](./caches.md#cache-status)
    * `http_status` - The HTTP response code provided by the backend
    * `path` - the Path portion of the requested URL

* `trickster_proxy_points_total` (Counter) - The total number of data points Trickster has handled.
  * labels:
    * `backend_name` - the name of the configured backend handling the proxy request
    * `provider` - the type of the configured backend handling the proxy request
    * `cache_status` - status codes are described [here](./caches.md#cache-status)
    * `path` - the Path portion of the requested URL

* `trickster_proxy_request_duration_seconds` (Histogram) - Time required to proxy a given Prometheus query.
  * labels:
    * `backend_name` - the name of the configured backend handling the proxy request
    * `provider` - the type of the configured backend handling the proxy request
    * `method` - the HTTP Method of the proxied request
    * `cache_status` - status codes are described [here](./caches.md#cache-status)
    * `http_status` - The HTTP response code provided by the backend
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
    * `provider` - the type of the configured cache performing the operation
    * `operation` - the name of the operation being performed (read, write, etc.)
    * `status` - the result of the operation being performed

* `trickster_cache_operation_bytes_total` (Counter) - The total number of bytes upon which the Trickster cache has operated.
  * labels:
    * `cache_name` - the name of the configured cache performing the operation$
    * `provider` - the type of the configured cache performing the operation
    * `operation` - the name of the operation being performed (read, write, etc.)
    * `status` - the result of the operation being performed

---

The following metrics are available only for Caches Types whose object lifecycle Trickster manages internally (Memory, Filesystem and bbolt):

* `trickster_cache_events_total` (Counter) - The total number of events that change the Trickster cache, such as retention policy evictions.
  * labels:
    * `cache_name` - the name of the configured cache experiencing the event$
    * `provider` - the type of the configured cache experiencing the event
    * `event` - the name of the event being performed
    * `reason` - the reason the event occurred

* `trickster_cache_usage_objects` (Gauge) - The current count of objects in the Trickster cache.
  * labels:
    * `cache_name` - the name of the configured cache$
    * `provider` - the type of the configured cache$

* `trickster_cache_usage_bytes` (Gauge) - The current count of bytes in the Trickster cache.
  * labels:
    * `cache_name` - the name of the configured cache$
    * `provider` - the type of the configured cache$

* `trickster_cache_max_usage_objects` (Gauge) - The maximum allowed size of the Trickster cache in objects.
  * labels:
    * `cache_name` - the name of the configured cache$
    * `provider` - the type of the configured cache

* `trickster_cache_max_usage_bytes` (Gauge) - The maximum allowed size of the Trickster cache in bytes.
  * labels:
    * `cache_name` - the name of the configured cache$
    * `provider` - the type of the configured cache

---

In addition to these custom metrics, Trickster also exposes the standard Prometheus metrics that are part of the [client_golang](https://github.com/prometheus/client_golang) metrics instrumentation package, including memory and cpu utilization, etc.
