# Trickster 2.2

Trickster 2.2 extends many of the new features introduced in 2.1, while adding support for delta-caching several new TSDB's that use the Postgres Query Dialect, as well as a new Global Query Evaluator that works with mixed Prom, Thanos and Mimir deployments. 2.2 adds multiple security and enforcement features, including ACME automatic cert renewal, IP ACLs, Rate Limiting and GeoACLs; and new CDN-grade enhancements including MoQ support.

## Load Balancing and Scaling

**L4 Load Balancing** - We've extended the HTTP L7 ALB to support Layer 4 as well (TCP and UDP), with a number of new balancing mechanisms.

**Sticky Sessions** - We now support configurable sticky sessions for the Load Balancer feature.

## New Acceleration-supported TSDBs

**TimescaleDB** - You can now accelerate TimescaleDB with the delta proxy cache! If you are tired of playing whack-a-mole with new continuous aggregates to manage performance, Trickster can stop the madness. Even better - any Postgres-compatible database can be fronted by Trickster for a `SELECT` result cache.

**GreptimeDB** - We've added GreptimeDB as an acceleration-supported backend time series provider.

**QuestDB** - And we also now support accelerating QuestDB.

**VictoriaMetrics** - And we also now support accelerating VictoriaMetrics on the MetricsQL and Graphite HTTP endpoints.

**CloudWatch Metrics** - We now support accelerating AWS CloudWatch Metrics on the new PromQL endpoints. Classic GetMetricData endpoints are not yet supported. 

**TSM Global Evaluation** - A Time Series Merge ALB can now evaluate each PromQL query once over the raw series of every member, deduplicating replicas the way Thanos does, rather than merging the members' answers. Members serve raw series through Prometheus remote read (the new `mimir` flavor) or the Thanos StoreAPI (the new `thanos` flavor), and complete results are cached, while the members' raw series are delta-cached, so each member is read only for the samples the cache lacks. Merging also now counts label sets returned by several members once for `count` or `group` over an inner `count` or `group`. See [Global Evaluation](./alb.md#global-evaluation).

## HTTP Reverse Proxy Cache, CDN and Streaming Media

**Media over QUIC (MoQ)** - Building on the HTTP/3 and QUIC support introduced in 2.1, Trickster is now a caching Media over QUIC Transport relay, for drafts -17 through -22. Native QUIC clients connect to a `protocol: moq` listener and browsers connect by WebTransport over HTTP/3. One upstream subscription per track fans out to every subscriber, the newest groups stay in memory and older ones in the cache for FETCH, and publishers can send tracks straight to Trickster. Origin pools, token authorization and the Kubernetes Gateway API are supported too. See [Media over QUIC](./moq.md) for details.

**HTTP/3 on Kubernetes Gateways** - HTTPS listeners the Kubernetes controller generates now also serve HTTP/3, and WebTransport, on the same port number over UDP. Turn it off with `kubernetes.defaults.http3: false` or a GatewayClass's `http3` parameter. See [GatewayClass parameters](./kubernetes-gateway.md#gatewayclass-parameters).

**HTTP/3 Port Release** - A reload that removes or drains an HTTP/3 endpoint now releases its UDP port. In 2.1, the port stayed bound until the process exited, so a later reload could not bind it again.

**HTTP QUERY Method** - Trickster accepts and caches the `QUERY` method ([RFC 10008](https://www.rfc-editor.org/rfc/rfc10008.html)), keyed on the request body. Time series query endpoints forward a `QUERY` to their origins as a `POST` and advertise `Accept-Query`. See [The QUERY Method](./paths.md#the-query-method).

**Automatic Certificates (ACME)** - Trickster can now obtain and renew its own serving certificates from Let's Encrypt or any other ACME certificate authority. See [Automatic Certificates](./acme.md) for details.

**Disk Caches** - The Filesystem and bbolt caches are rebuilt for large caches and large objects. See [Disk Caches](./caches.md#disk-caches) for details.

**Chunked Caching Fixes** - `use_cache_chunking: true` is honored from configuration again; since 2.0 it was silently reset to `false` at load. Chunked byterange caching no longer zeroes stored chunks on a partial hit, stores range misses, retries chunks that were evicted rather than serving their bytes as zeros, and no longer truncates bodies when chunks are read concurrently. A configuration reload that changes a memory cache's chunking settings now rebuilds the cache. See [Chunked Caching](./chunked_caching.md).

**SRV Origin Resolution** - A backend can resolve its origin host as a DNS SRV owner name, dialing a target and port from the SRV answer, with failover across targets and tiers. Combined with a rule and a hostname rewriter, one backend can route to any number of origins published in DNS, including ECS, Consul and Nomad services with dynamically assigned ports. See [Origin Resolution via DNS SRV](./origin-resolution.md).

**Idle Connection Timeouts** - HTTP listeners now close a keep-alive connection that waits more than 2 minutes for its next request; until now idle connections were never closed. The new `idle_timeout`, `read_timeout` and `max_header_bytes` listener options tune this. See [Connection Timeouts and Header Size](./configuring.md#connection-timeouts-and-header-size).

## Security

**Path Normalization** - HTTP listeners now remove `.` and `..` path segments before routing and forward the cleaned path, closing a bypass of path-scoped controls such as `authenticator_name: none`; see [Path Normalization](./configuring.md#path-normalization) for the new `path_normalization` options and opt-out.

**Forwarded Hops From Trusted Proxies Only** - Trickster now forwards the `Forwarded`, `X-Forwarded-*` and `X-Real-IP` values a request arrived with only when one of the listener's `trusted_proxies` delivered it, appending its own hop; from any other peer the origin receives Trickster's hop alone, so a client cannot hand the origin a forged address. Passthrough paths, which forwarded no prior hops before, now forward a trusted proxy's. **A listener behind a load balancer must list it in `trusted_proxies`** for the origin to keep seeing client addresses. See [Forwarding Headers to the Origin](./configuring.md#forwarding-headers-to-the-origin).

**Rate Limiting** - A named limiter counts requests, connections, sessions, or datagrams and refuses what exceeds its limit. It attaches at the listener, backend, and path. The listener limiter is its own layer. See [Rate Limiting](./rate-limiting.md).

**IP ACLs** - We've also added IP Access Control Lists to restrict access to certain backend resources by IP. it can be attached at the listener, backend, and path levels, with most specific winning.

**Geo ACLs** - We now support Geolocation Access Control lists to restrict content to geographical areas through IP -> Location translation via industry-standard locator services (MaxMind, RFC 8805 geofeeds, Header from trusted downstreams). Bring your own licensed database.

## Fixes

**Log Level Names Ignore Case** - `log_level`, `TRK_LOG_LEVEL` and `-log-level` now accept a level name in any case. Until now, an uppercase name such as `DEBUG` passed config validation but logged at `info`, and a config with no `logging` block warned that its own default level was unknown. See [Environment Variables](./configuring.md#environment-variables) and [Command Line Arguments](./configuring.md#command-line-arguments).

**Rule Inputs Read the Requested Host** - A rule's `url`, `url_no_params`, `scheme`, `host`, `hostname` and `port` inputs now describe the URL the client requested, taking the host from the `Host` header and the scheme from whether the request arrived over TLS. Until now they read only the request URL, which carries no host or scheme on a client request, so rules matching on these inputs never matched. `port` now also infers `80` or `443` from the scheme when the host names no port, as documented. A request rewriter's `host`, `hostname` and `port` replace instructions likewise now act on the requested host. See [input_source permitted values](./rule.md#input_source-permitted-values).
