# Trickster 2.2

Trickster 2.2 extends many of the new features introduced in 2.1, while adding support for delta-caching several new TSDB's that use the Postgres Query Dialect.

Trickster 2.2 just recently began development, so many of the planned features are still being designed or are under development.

## Load Balancing and Scaling

**Layer 4 Load Balancing** - We extend our HTTP L7 ALB to support Layer 4 as well. Supported mechanisms beyond Round Robin are TBD.

- We now support sticky sessions for the Load Balancer feature.

**PLANNED** - We now provide a request rate limiter based on request attributes. it can be attached at the listener, backend, and path levels, with most specific winning.

**PLANNED** - We've also added IP Access Control Lists to restrict access to certain backend resources by IP. it can be attached at the listener, backend, and path levels, with most specific winning.

## New Acceleration-supported TSDBs

All three of these newly-supported providers consume a new `pgwire` package for the postgres wire protocol, along with a common lexer and per-dialect parsers. Any future Postgres-compatible providers Trickster supports will reuse this for faster bootstrapping.

**TimescaleDB** - You can now accelerate TimescaleDB with the delta proxy cache! If you are tired of playing whack-a-mole with new continuous aggregates to manage performance, Trickster can stop the madness. Even better - any Postgres-compatible database can be fronted by Trickster for a `SELECT` result cache.

**GreptimeDB** - We've added GreptimeDB as an acceleration-supported backend time series provider.

**PLANNED** - **QuestDB** - And we also now support accelerating QuestDB.

**PLANNED** - Better support for TSM with a distributed Mimir system.

## HTTP Reverse Proxy Cache & Streaming

**PLANNED** - **Media over QUIC (MoQ)** -- In Trickster 2.1, we introduced support for HTTP/3 and QUIC. We now offer support for MoQ Relaying through the reverse proxy cache.

**HTTP QUERY Method** - Trickster accepts and caches the `QUERY` method ([RFC 10008](https://www.rfc-editor.org/rfc/rfc10008.html)), keyed on the request body. Time series query endpoints forward a `QUERY` to their origins as a `POST` and advertise `Accept-Query`. See [The QUERY Method](./paths.md#the-query-method).

**Automatic Certificates (ACME)** - Trickster can now obtain and renew its own serving certificates from Let's Encrypt or any other ACME certificate authority. See [Automatic Certificates](./acme.md) for details.

- A backend opts in with `tls.acme`, and certificates are issued for its `hosts`.
- The `http-01`, `tls-alpn-01` and `dns-01` challenges are supported, and `dns-01` issues wildcard certificates through the Cloudflare, Route 53 or RFC 2136 providers.
- Certificates are stored on the filesystem, or in Redis to share them across a cluster of instances.
- `acme.wait_on_startup` holds readiness until a new deployment's certificates are issued.
- On-demand TLS issues a certificate during the first handshake for a permitted name, gated by an `ask` endpoint or a list of allowed domains.
- The mgmt listener lists managed domains and forces renewals at `/trickster/acme`.

**Disk Caches** - The Filesystem and bbolt caches are rebuilt for large caches and large objects. See [Disk Caches](./caches.md#disk-caches) for details.

- Cached objects are self-describing and checksummed, and are written atomically, so an incomplete or damaged object is never served.
- The Filesystem Cache spreads its files across two levels of directories, in place of a single directory.
- A Cache Index that is lost or out of date is rebuilt from the cache in the background. Nothing in the cache is orphaned.
- The Cache Index is persisted as a journal of changes, and its cost no longer grows with the size of the cache. Expiration and eviction no longer rank every object in the cache.
- Large objects are served from a disk cache as they are read, and `Range` requests read only the ranges asked for, without holding the object in memory.
- New options: `scan_interval`, `scan_batch_size` and `scan_batch_pause` for the Cache Index, and `min_free_bytes` for the Filesystem Cache.
- **Upgrade note:** Filesystem and bbolt caches start cold after upgrading to 2.2, as objects cached by earlier versions are stored in another format. Trickster removes them on its own, in the background.
