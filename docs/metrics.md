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

* `trickster_proxy_upstream_retries_total` (Counter) - The number of upstream requests retried under a path's `retry` policy.
  * labels:
    * `backend_name` - the name of the configured backend handling the proxy request
    * `path` - the configured path whose policy retried the request

* `trickster_proxy_mirror_requests_total` (Counter) - The number of requests copied to a path's `mirror` backend.
  * labels:
    * `backend_name` - the name of the configured backend handling the proxy request
    * `mirror_backend` - the backend receiving the copies
    * `result` - `sent`, or `dropped` when the mirror's in-flight bound was reached

* `trickster_accesslog_dropped_lines_total` (Counter) - The number of access and error log lines dropped because the log could not accept them.
  * labels:
    * `backend_name` - the name of the configured backend whose logger dropped the line
    * `log` - `access` or `error`

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

* `trickster_proxy_stream_connections_total` (Counter) - The number of connections and UDP sessions accepted by `tcp`, `tls` and `udp` listeners.
  * labels:
    * `listener_name` - the name of the configured listener
    * `protocol` - `tcp`, `tls` or `udp`
    * `result` - `proxied`, or why the connection was closed instead: `not_tls` (a `tls` listener received no ClientHello), `no_route` (no backend routes the server name), `no_upstream` (the backend's pool has no dialable member, or the member chosen refuses its share), `dial_failed`, or `refused` (a `udp` listener at its session limit, or a connection arriving as the listener closes)

* `trickster_proxy_stream_active_connections` (Gauge) - The number of connections and UDP sessions stream listeners are relaying.
  * labels:
    * `listener_name` - the name of the configured listener
    * `protocol` - `tcp`, `tls` or `udp`

* `trickster_proxy_stream_dropped_datagrams_total` (Counter) - The number of datagrams `udp` listeners dropped rather than relayed.
  * labels:
    * `listener_name` - the name of the configured listener
    * `reason` - `queue_full` (the client's flow, or every flow together, already held its allowance of datagrams waiting to be written) or `write_timeout` (the write to the backend blocked for the whole write bound)

* `trickster_proxy_stream_bytes_total` (Counter) - The bytes relayed by stream listeners.
  * labels:
    * `listener_name` - the name of the configured listener
    * `protocol` - `tcp`, `tls` or `udp`
    * `direction` - `in` from the client to the backend, `out` from the backend to the client

* `trickster_proxy_stream_member_connections_total` (Counter) - The number of connections and UDP sessions a stream listener committed to an ALB pool member.
  * labels:
    * `listener_name` - the name of the configured listener
    * `protocol` - `tcp`, `tls` or `udp`
    * `backend_name` - the name of the pool member backend
    * `result` - `proxied`, `dial_failed` (the member could not be connected to) or `unreachable` (a `udp` member answered a datagram with a port-unreachable)

* `trickster_proxy_stream_member_active_connections` (Gauge) - The number of connections and UDP sessions open to an ALB pool member.
  * labels:
    * `listener_name` - the name of the configured listener
    * `protocol` - `tcp`, `tls` or `udp`
    * `backend_name` - the name of the pool member backend

* `trickster_proxy_stream_member_connect_duration_seconds` (Histogram) - The time taken to connect to an ALB pool member.
  * labels:
    * `listener_name` - the name of the configured listener
    * `protocol` - `tcp`, `tls` or `udp`
    * `backend_name` - the name of the pool member backend

* `trickster_proxy_query_range_rejected_total` (Counter) - Trickster total number of queries rejected due to exceeding the `max_query_range` limit.
  * labels:
    * `backend` - the name of the configured backend rejecting the query

* `trickster_graphite_resolution_lookups_total` (Counter) - Count of Graphite step-resolution lookups. Labels never include a metric path or target expression.
  * labels:
    * `backend_name` - the name of the configured Graphite backend
    * `confidence` - how the step was established: `exact` (read from an origin response for this leaf set and age), `derived` (computed from known leaf ladders), `configured` (from `static_retentions`, not yet probe-confirmed), or `unknown` (no usable step; the request is served unaccelerated)
    * `source` - where it came from: `registry`, `response`, `probe`, `static`, `function`, or `none`
* `trickster_graphite_probes_total` (Counter) - Count of synthetic requests issued to learn a metric's archive ladder. Expect a spike at startup that collapses toward zero as ladders are learned.
  * labels:
    * `backend_name` - the name of the configured Graphite backend
    * `kind` - `narrow` (a one-second window that also discovers the retention edge), `wide` (what a real query at that age receives), or `find` (a `/metrics/expand` lookup)
    * `result` - `step` (a stepped series came back), `empty` (no series: beyond retention, or no such metric), or `error`
* `trickster_graphite_ladders` (Gauge) - Number of distinct archive ladders known to the resolution registry. Ladders come from `storage-schemas.conf` patterns, so this should flatten at a small number.
  * labels:
    * `backend_name` - the name of the configured Graphite backend
* `trickster_graphite_registry_entries` (Gauge) - Number of entries in each layer of the resolution registry.
  * labels:
    * `backend_name` - the name of the configured Graphite backend
    * `layer` - `leaf` (metric path to ladder), `ladder` (the ladders themselves), `target` (cached wildcard expansions), or `negative` (paths in resolution backoff)
* `trickster_graphite_step_mispredictions_total` (Counter) - Count of origin responses whose step differed from the predicted step. **This should always be zero.** A non-zero value means a cached ladder was wrong; Trickster discards the prediction, relearns and re-serves the request unaccelerated, so clients still receive correct data.
  * labels:
    * `backend_name` - the name of the configured Graphite backend
* `trickster_graphite_fallbacks_total` (Counter) - Count of render requests served without delta caching. Labels never include a target expression.
  * labels:
    * `backend_name` - the name of the configured Graphite backend
    * `reason` - `parse_error`, `non_series_format`, `function_not_allowlisted`, `unknown_step`, `missing_target`, `multi_target_step_mismatch`, `passthrough_max_data_points`, `misprediction`, `client_identity`, `tz_unavailable`, or `resolution_identity`
* `trickster_sql_query_analysis_total` (Counter) - Count of SQL query cache-eligibility classifications. Labels never include query text.
  * labels:
    * `backend_name` - the name of the configured backend analyzing the query
    * `dialect` - the SQL dialect of the analyzing backend (e.g., `clickhouse`)
    * `cache_mode` - the strongest cache mode supported by the query (`delta`, `object`, or `none`)
    * `reason` - the stable classification reason code (e.g., `delta_cacheable`, `unsafe_predicate`, `unsupported_bucket`)

* `trickster_sql_query_rewrite_failures_total` (Counter) - Count of SQL cache-miss extent rewrite failures. Labels never include query text.
  * labels:
    * `backend_name` - the name of the configured backend rendering the query
    * `dialect` - the SQL dialect of the rendering backend
    * `reason` - the fixed internal failure category

* `trickster_druid_query_analysis_total` (Counter) - Count of native Druid query cache-eligibility classifications. Labels never include query text or datasource names.
  * labels:
    * `backend_name` - the configured Druid backend
    * `cache_mode` - `delta`, `object`, or `proxy`
    * `reason` - the stable classification reason code

* `trickster_druid_query_rewrite_failures_total` (Counter) - Count of Druid cache-miss extent rewrite failures.
  * labels:
    * `backend_name` - the configured Druid backend
    * `reason` - the fixed internal failure category

* `trickster_cache_operation_objects_total` (Counter) - The total number of objects upon which the Trickster cache has operated.
  * labels:
    * `cache_name` - the name of the configured cache performing the operation
    * `provider` - the type of the configured cache performing the operation
    * `operation` - the name of the operation being performed (read, write, etc.)
    * `status` - the result of the operation being performed

* `trickster_cache_operation_duration_seconds` (Histogram) - The time, in seconds, required to perform an operation on the Trickster cache. Deletions include both requested removals and index reaper evictions.
  * labels:
    * `cache_name` - the name of the configured cache performing the operation
    * `provider` - the type of the configured cache performing the operation
    * `operation` - the name of the operation being performed (`get`, `set`, `setDirect`, `del`)
    * `status` - the result of the operation being performed (e.g., `hit`, `kmiss` for a full key miss, `none`)

* `trickster_cache_operation_bytes_total` (Counter) - The total number of bytes upon which the Trickster cache has operated. Deletions (`del`) record bytes only for cache providers that use an index, since other providers don't track object sizes.
  * labels:
    * `cache_name` - the name of the configured cache performing the operation
    * `provider` - the type of the configured cache performing the operation
    * `operation` - the name of the operation being performed (read, write, etc.)
    * `status` - the result of the operation being performed

* `trickster_alb_pool_admits_failing` (Gauge) - 1 when an ALB pool's `healthy_floor` admits members in the `unavailable` state, 0 otherwise. See [alb.md](./alb.md#health-based-backend-selection) for the recommended floor.
  * labels:
    * `backend_name` - the name of the configured ALB backend

* `trickster_alb_pool_floor_reset` (Gauge) - 1 when an ALB pool's `healthy_floor` was reset to 0 at startup because pool members have no health check and could never reach the configured floor, 0 otherwise. See [alb.md](./alb.md#health-based-backend-selection).
  * labels:
    * `backend_name` - the name of the configured ALB backend

* `trickster_alb_member_inflight` (Gauge) - Current number of requests in flight to an ALB pool member. Exported for the mechanisms that track it (`p2c`, `lc`, `lt`), for requests and for stream connections and sessions alike; read when the metrics endpoint is scraped, at no cost to request routing.
  * labels:
    * `alb_name` - the name of the configured ALB backend
    * `member` - the name of the pool member backend

* `trickster_alb_member_ejections_total` (Counter) - The number of times `alb.stream.passive_health` took a pool member out of selection after repeated connect failures.
  * labels:
    * `alb_name` - the name of the configured ALB backend
    * `member` - the name of the pool member backend

The following metrics are available when [ALB Autodiscovery](./alb-autodiscovery.md) is configured:

* `trickster_alb_discovery_members` (Gauge) - Current number of discovered ALB pool members
  * labels:
    * `alb_name` - the name of the discovery-backed ALB backend
    * `discoverer` - the name of the discoverer serving the ALB

* `trickster_alb_discovery_member_changes_total` (Counter) - Count of discovered pool member additions and removals
  * labels:
    * `alb_name` - the name of the discovery-backed ALB backend
    * `discoverer` - the name of the discoverer serving the ALB
    * `event` - `add` or `remove`

* `trickster_alb_discovery_snapshots_total` (Counter) - Count of membership snapshots processed, by result
  * labels:
    * `alb_name` - the name of the discovery-backed ALB backend
    * `discoverer` - the name of the discoverer serving the ALB
    * `result` - `applied` (membership updated), `unchanged` (no-op), `rejected` (guardrail-refused, e.g. `min_members`), or `partial` (applied with member instantiation failures)

* `trickster_alb_discovery_last_refresh_success_time_seconds` (Gauge) - Epoch timestamp of the last successfully processed snapshot, for staleness alerting
  * labels:
    * `alb_name` - the name of the discovery-backed ALB backend
    * `discoverer` - the name of the discoverer serving the ALB

* `trickster_discovery_refresh_errors_total` (Counter) - Count of provider-side refresh/watch errors (DNS resolution failures, Kubernetes list/sync failures, member-file read/parse failures)
  * labels:
    * `discoverer` - the name of the discoverer experiencing the error
    * `provider` - the discoverer's provider type
* `trickster_tls_certificate_expiration_time_seconds` (Gauge) - NotAfter time of a serving TLS certificate, as unix seconds. See [tls.md](./tls.md).
  * labels:
    * `listener` - the name of the listener serving the certificate
    * `entry` - the certificate's source identity

* `trickster_tls_certificate_last_load_time_seconds` (Gauge) - Epoch timestamp a serving TLS certificate was last loaded from its source
  * labels:
    * `listener` - the name of the listener serving the certificate
    * `entry` - the certificate's source identity

* `trickster_tls_certificate_swaps_total` (Counter) - Count of TLS certificates hot-swapped into a live listener by rotation detection
  * labels:
    * `listener` - the name of the listener serving the certificate
    * `entry` - the certificate's source identity

* `trickster_tls_certificate_validation_failures_total` (Counter) - Count of detected TLS certificate source changes that failed pair validation (e.g. a mid-rotation partial write) and were not swapped in
  * labels:
    * `entry` - the certificate's source identity

* `trickster_tls_watcher_errors_total` (Counter) - Count of errors reading watched TLS certificate source files
  * labels:
    * `entry` - the certificate's source identity

* `trickster_tls_certificate_store_size` (Gauge) - Number of certificates in a listener's TLS certificate store
  * labels:
    * `listener` - the name of the listener

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

The following metrics are available when the Kubernetes Gateway/Ingress controller is enabled (the top-level `kubernetes` section; see [kubernetes-gateway.md](./kubernetes-gateway.md)):

* `trickster_kgw_reconciles_total` (Counter) - Count of controller reconcile passes, by result
  * labels:
    * `result` - `applied` (the data plane was reloaded), `unchanged` (the generated configuration was already running), or `error` (the pass failed to compile or apply)

* `trickster_kgw_reconcile_duration_seconds` (Histogram) - Duration of a whole reconcile pass, from reading the caches to writing status

* `trickster_kgw_reconcile_errors_total` (Counter) - Count of reconcile passes that failed in a stage
  * labels:
    * `stage` - `compile`, `apply`, `certificates` (a certificate a listener's store refused) or `status` (a status write the API server refused)

* `trickster_kgw_watch_events_total` (Counter) - Count of Kubernetes watch events received by the controller
  * labels:
    * `kind` - the object kind (`Gateway`, `HTTPRoute`, `Ingress`, `Secret`, `Service`, ...)
    * `event` - `add`, `update` or `delete`

* `trickster_kgw_translate_duration_seconds` (Histogram) - Duration of translating the watched objects into the routing model

* `trickster_kgw_apply_duration_seconds` (Histogram) - Duration of applying generated configuration to the data plane, observed only on passes that reload it

* `trickster_kgw_generated_objects` (Gauge) - Size of the routing model the last pass produced
  * labels:
    * `kind` - `listeners`, `routes`, `backends`, `certificates` or `policies`

* `trickster_kgw_status_write_failures_total` (Counter) - Count of status writes the API server refused, after conflict retries
  * labels:
    * `kind` - the kind of the object whose status could not be written

* `trickster_kgw_leader` (Gauge) - 1 when this replica holds the leader election Lease and so writes status and Events, 0 otherwise

* `trickster_kgw_last_successful_sync_time_seconds` (Gauge) - Epoch timestamp of the last reconcile pass whose generated configuration is running, for staleness alerting

* `trickster_kgw_route_info` (Gauge) - A constant 1 per generated backend, joining it to the Kubernetes object it serves
  * labels:
    * `kind` - `HTTPRoute` or `Ingress`
    * `route` - the object's name
    * `namespace` - the object's namespace
    * `backend_name` - the generated backend's name, as the `backend_name` label of the request metrics carries it

  For example, request rates per Kubernetes route:

  ```promql
  sum by (kind, namespace, route) (
    rate(trickster_proxy_requests_total[5m])
    * on (backend_name) group_left (kind, namespace, route) trickster_kgw_route_info
  )
  ```

---

In addition to these custom metrics, Trickster also exposes the standard Prometheus metrics that are part of the [client_golang](https://github.com/prometheus/client_golang) metrics instrumentation package, including memory and cpu utilization, etc.
