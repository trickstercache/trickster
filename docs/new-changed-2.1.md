# Trickster 2.1

Trickster 2.1 adds tons of new features to put acceleration in even more places: Kubernetes routing, more supported backend providers, and modern protocols. 👌

### A Brand New Trick: Mecone v1.0

Alongside Trickster v2.1, today we launch a new companion project, [Mecone](https://github.com/trickstercache/mecone) (pronounced like McConey) - a Reverse Proxy conformance tester that is extensible via YAML configs. We use Mecone to measure Trickster's conformance to the HTTP protocol specifications, and also compare its conformance against other industry solutions via published quarterly reports. Check out the Mecone repo to see how Trickster stacks up in [the Q3 2026 report](https://github.com/trickstercache/mecone/blob/main/soti/2026-q3-soti.md).

## Trickster v2.1 Features

### Request Routing

* Kubernetes Gateway API and Ingress Controller - Trickster now runs as a Kubernetes controller, serving `gateway.networking.k8s.io` GatewayClass, Gateway, HTTPRoute, GRPCRoute, TCPRoute, TLSRoute, UDPRoute, ReferenceGrant and BackendTLSPolicy objects, and `networking.k8s.io/v1` Ingress objects, translating them into its own configuration and reloading onto it in-process. Routes may be served through the Service's cluster IP or load balanced across discovered endpoints, TLS certificates arrive from Kubernetes Secrets without a reload, and status, conditions and Events are written back to every claimed object. Enable it with the new top-level `kubernetes` section. See [kubernetes-gateway.md](./kubernetes-gateway.md), [kubernetes-ingress.md](./kubernetes-ingress.md), [kubernetes-rbac.md](./kubernetes-rbac.md) and [kubernetes-deploy.md](./kubernetes-deploy.md).
   * TricksterCachePolicy Custom Resource - A custom resource that attaches caching behavior — a cache, TTLs, cache key components, CORS, header updates and time series acceleration — to Gateways, HTTPRoutes, Ingresses and Services, so a Prometheus or ClickHouse Service behind a route is accelerated rather than merely proxied. See [kubernetes-cache-policy.md](./kubernetes-cache-policy.md).

* Auto-discovery - The ALB can now manage pool members through common [auto-discovery](./alb-autodiscovery.md) mechanisms such as Kubernetes APIs, DNS A and SRV records, etc.

* Regex Path Matching - You can now [define path routes with regexes](./paths.md#regex-paths) to match incoming requests, and expose their capture groups to request rewriters.

* Wildcard Host Routing - A backend's `hosts` may name a single-label wildcard (`*.example.com`) or an any-depth wildcard (`**.example.com`), resolved by specificity ahead of global routes. See [Path Configuration Documentation](./paths.md#host-resolution).

### Supported Backend Providers

* We now support accelerating [InfluxDB 3.x](./influxdb.md#influxdb-3x-support), including over Flight SQL (gRPC). Support for InfluxDB 1.x and 2.x remains and is unchanged.

* We've expanded ClickHouse delta proxy cache support to include the [native (TCP/binary) protocol](./clickhouse.md#native-binary-protocol-support). You can configure a ClickHouse Backend that accepts HTTP and proxies to an origin via Native, or conversely a Backend that accepts Native and proxies via HTTP. A single Backend configuration can also listen on both Native and HTTP.

* We now support accelerating [Graphite](./graphite.md).

* We now support accelerating [Apache Druid](./druid.md).

* We now support accelerating [MySQL](./mysql.md) with Time Series-like queries (e.g., Grafana dashboards).

### HTTP Protocol Conformance

* HTTP/2 and HTTP/3 - Between the client and Trickster, cleartext listeners now accept prior-knowledge cleartext HTTP/2 (h2c) alongside HTTP/1.1, as gRPC and other h2c clients require; no configuration is required. Between Trickster and the origin, HTTP/2 is negotiated automatically via ALPN with `https://` origins that offer it, also with no configuration. For `http://` origins that serve only h2c, set the new `h2c_prior_knowledge` backend option; it is opt-in because there is no HTTP/1.1 fallback for such an origin. Listeners can also now serve their routes over HTTP/3 (QUIC) alongside HTTP/1.1 and HTTP/2. Enable the new `http3` block on any TLS-enabled listener; responses on the TLS endpoint advertise the HTTP/3 endpoint via `Alt-Svc` so clients upgrade on their own. See [http3.md](./http3.md).

* Protocol Upgrades - WebSocket and other `Connection: Upgrade` requests are now tunneled end-to-end rather than rejected, including through paths that are otherwise cached.

* Streaming - Responses with an unknown length and Server-Sent Events streams are now flushed to the client as bytes arrive rather than buffered, and HTTP trailers are passed through.

* Stream Listeners - Listeners can now relay TCP connections, TLS connections by server name without terminating them, and UDP datagrams to a backend or a load balancer pool, without reading what passes. Set `protocol: tcp`, `tls` or `udp` on a listener; see [configuring.md](./configuring.md#stream-listeners). The Kubernetes controller serves TCPRoute, TLSRoute and UDPRoute through them.

### Configuration, Observability & Security

* Logging - We've added support for customizable access logging and error logging per backend in NCSA format, a top-level `access_log` that captures every request no backend handled, and logging to stdout or stderr for containerized deployments. See [access-logs.md](./access-logs.md).

* Config Files - We now support loading multiple config files in the same subdirectory below the main config. We've also added automatic config reloading when the file contents change - including automatic detection and reloading when a TLS certificate is swapped out. See the [Configuring Documentation](./configuring.md#automatic-config-reload) for more info.

* TLS Certificate Rotation and Runtime Certificates - Serving certificates renewed in place on disk by tools such as certbot or cert-manager are detected and hot-swapped into the live listener, with no reload and without dropping established connections; `tls_watch_interval` tunes the backstop poll. The new `tls_runtime_certs` listener option keeps a TLS port open with no certificate files behind it, so certificates can be supplied to the running process instead — which is how the Kubernetes controller serves Gateway and Ingress TLS from Secrets. The mgmt listener exposes a read-only certificate inventory at `/trickster/certificates`, and certificate expiry, load, swap and validation metrics are available with example alerting rules. See [tls.md](./tls.md).

* Graceful Shutdown and Readiness - A new readiness endpoint (`/trickster/ready`) reports whether the process is serving, and `mgmt.shutdown_delay` and `mgmt.shutdown_drain_timeout` hold listeners open and then drain in-flight requests on SIGTERM, so a rolling deployment is invisible to clients. `/trickster/ping` remains a liveness check. See [configuring.md](./configuring.md#graceful-shutdown-and-readiness).

* Real Client Addresses - Listeners now accept the PROXY protocol, and the new `trusted_proxies` listener option resolves the real client address from `Forwarded`, `X-Forwarded-For` or `X-Real-IP` only when the connection comes from a trusted address. The resolved address is what the access log records and what `max_query_range` rejections are logged against. See [Trusted Proxies](./configuring.md#trusted-proxies).

### Developer Environment

* All of the new supported backend time series providers are included in the Developer Environment Docker Compose.

* We've refactored the Developer Environment Data Seeder job to use 100% locally generated data. No more big S3 file downloads. This also speeds up the GitHub CI/CD integration tests that depend on the dev env. Seed data is generated once as a TSV, and all seedable databases (MySQL, Druid, ClickHouse) mount the same file to load the same data to enable cross-provider testing and verification.
