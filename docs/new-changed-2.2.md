# Trickster 2.2

Trickster 2.2 extends many of the new features introduced in 2.1, while adding support for delta-caching several new TSDB's that use the Postgres Query Dialect.

Trickster 2.2 just recently began development, so many of the planned features are still being designed or are under development.

## Load Balancing and Scaling

**Layer 4 Load Balancing** - We extend our HTTP L7 ALB to support Layer 4 as well, with a number of new balancing mechanisms.

- We now support sticky sessions for the Load Balancer feature.

**PLANNED** - We now provide a request rate limiter based on request attributes. it can be attached at the listener, backend, and path levels, with most specific winning.

- We've also added IP Access Control Lists to restrict access to certain backend resources by IP. it can be attached at the listener, backend, and path levels, with most specific winning.


s- We now support Geolocation Access Control lists to restrict content to geographical areas through IP -> Location translation via industry-standard locator services (MaxMind, RFC 8805 geofeeds, Header from trusted downstreams). Bring your own licensed database.

## New Acceleration-supported TSDBs

**TimescaleDB** - You can now accelerate TimescaleDB with the delta proxy cache! If you are tired of playing whack-a-mole with new continuous aggregates to manage performance, Trickster can stop the madness. Even better - any Postgres-compatible database can be fronted by Trickster for a `SELECT` result cache.

**GreptimeDB** - We've added GreptimeDB as an acceleration-supported backend time series provider.

**QuestDB** - And we also now support accelerating QuestDB.

**VictoriaMetrics** - And we also now support accelerating VictoriaMetrics on the MetricsQL and Grahpite HTTP endpoints.

**CloudWatch Metrics** - We now support accelerating AWS CloudWatch Metrics on the new PromQL endpoints. Classic GetMetricData endpoints are not yet supported. 

**PLANNED** - Better support for Time Series Merge for Mimir and Thanos backends

## HTTP Reverse Proxy Cache, CDN and Streaming Media

**PLANNED** - **Media over QUIC (MoQ)** -- In Trickster 2.1, we introduced support for HTTP/3 and QUIC. We now offer support for MoQ Relaying through the reverse proxy cache.

**HTTP QUERY Method** - Trickster accepts and caches the `QUERY` method ([RFC 10008](https://www.rfc-editor.org/rfc/rfc10008.html)), keyed on the request body. Time series query endpoints forward a `QUERY` to their origins as a `POST` and advertise `Accept-Query`. See [The QUERY Method](./paths.md#the-query-method).

**Automatic Certificates (ACME)** - Trickster can now obtain and renew its own serving certificates from Let's Encrypt or any other ACME certificate authority. See [Automatic Certificates](./acme.md) for details.

**Disk Caches** - The Filesystem and bbolt caches are rebuilt for large caches and large objects. See [Disk Caches](./caches.md#disk-caches) for details.

**Idle Connection Timeouts** - HTTP listeners now close a keep-alive connection that waits more than 2 minutes for its next request; until now idle connections were never closed. The new `idle_timeout`, `read_timeout` and `max_header_bytes` listener options tune this. See [Connection Timeouts and Header Size](./configuring.md#connection-timeouts-and-header-size).

## Security

**Path Normalization** - HTTP listeners now remove `.` and `..` path segments before routing and forward the cleaned path, closing a bypass of path-scoped controls such as `authenticator_name: none`; see [Path Normalization](./configuring.md#path-normalization) for the new `path_normalization` options and opt-out.

**Forwarded Hops From Trusted Proxies Only** - Trickster now forwards the `Forwarded`, `X-Forwarded-*` and `X-Real-IP` values a request arrived with only when one of the listener's `trusted_proxies` delivered it, appending its own hop; from any other peer the origin receives Trickster's hop alone, so a client cannot hand the origin a forged address. Passthrough paths, which forwarded no prior hops before, now forward a trusted proxy's. **A listener behind a load balancer must list it in `trusted_proxies`** for the origin to keep seeing client addresses. See [Forwarding Headers to the Origin](./configuring.md#forwarding-headers-to-the-origin).
