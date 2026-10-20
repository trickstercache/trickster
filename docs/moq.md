# Media over QUIC (MoQ) Relay

Trickster relays [Media over QUIC Transport](https://datatracker.ietf.org/doc/draft-ietf-moq-transport/)
(MoQT) as a caching relay. Clients subscribe to tracks through Trickster, which holds one
subscription per track toward its origin and fans each object out to every subscriber. It keeps
the newest groups in memory and older ones in a cache, so FETCH and late joiners are served
from Trickster with only the missing objects fetched upstream. Publishers can also send tracks
straight to Trickster, making it the origin.

MoQT is a live-media transport: a track is a sequence of groups (for video, typically one group
per GOP), each a sequence of objects, delivered over QUIC streams or datagrams. Trickster never
parses the media inside objects; it caches and forwards them as opaque payloads.

## Compatibility

MoQT is still an Internet-Draft, and implementations in the field speak different drafts.
Trickster speaks drafts -17 through -22 and negotiates one per session; a session on one draft
can relay a track whose origin speaks another.

| Draft | ALPN | Notes |
| --- | --- | --- |
| -22 | `moqt-22` | the newest; offered to clients but not to origins by default (see below) |
| -21 | `moqt-21` | the preferred draft |
| -20 | `moqt-20` | fill fetch streams replace Joining FETCH |
| -19 | `moqt-19` | range filters, `MAX_REQUEST_UPDATES`, multiple subscriptions per track |
| -18 | `moqt-18` | `moqt://` URIs, `SUBSCRIBE_TRACKS`, `REDIRECT` |
| -17 | `moqt-17` | Required Request ID, `PUBLISH_OK`, the combined `SUBSCRIBE_NAMESPACE` |

Native QUIC clients negotiate the draft by ALPN, and WebTransport clients by
`WT-Available-Protocols`. Trickster prefers -21, then -22, then -20 and the rest, newest first,
and offers origins -21, -20, -19, -18 and -17. moq-dev's relay and CLI encode -22 as -21, so
preferring -21 keeps them working; set `drafts` to change the order. Drafts before -17 are not
supported.

Interoperability is tested on every draft against [moq-dev](https://github.com/moq-dev/moq)'s
relay (0.17.0) and `moq` CLI (0.14.0).

## How clients reach the relay

- **Native QUIC.** A listener with `protocol: moq` accepts MoQT sessions on its UDP port. A
  session is routed to a `moq` backend by the AUTHORITY and PATH of its SETUP, matched
  against the backend's `hosts` and `paths`.
- **WebTransport.** Browsers open WebTransport sessions on an HTTP listener that serves
  [HTTP/3](./http3.md). A `moq` backend's `paths` become WebTransport routes there, matched
  like any other route by host and path.
- **Publishers** use either path, with `PUBLISH` or `PUBLISH_NAMESPACE`, when the backend
  accepts them.

All three meet in the backend's relay, which is shared by every listener the backend is bound
to.

## Configuration

```yaml
listeners:
  # native QUIC; TLS is part of the QUIC handshake, so there is no tls_port
  moq1:
    protocol: moq
    port: 443
  # browsers arrive by WebTransport over the default listener's HTTP/3 endpoint
  default:
    port: 8480
    tls_port: 8443
    http3:
      enabled: true

backends:
  live:
    provider: moq
    # moqt:// for a native QUIC origin, https:// for a WebTransport one; omit it to serve
    # only what publishers send
    origin_url: moqt://origin.example.com:443/live
    listener_names: [moq1, default]
    hosts: [live.example.com]
    cache_name: default
    max_ttl: 1h
    tls:
      # served to clients of both listeners
      full_chain_cert_path: /etc/trickster/tls/live.crt
      private_key_path: /etc/trickster/tls/live.key
      # trusted toward the origin
      certificate_authority_paths: [/etc/trickster/tls/origin-ca.crt]
```

[example.full.yaml](../examples/conf/example.full.yaml) documents every option.

### The `moq` listener

| Option | Default | Description |
| --- | --- | --- |
| `port`, `address` | | the UDP port and address to bind |
| `moq.handshake_timeout` | `5s` | the time a client has to send SETUP |
| `moq.idle_timeout` | `2m` | the QUIC idle timeout |
| `moq.max_sessions` | `10000` | sessions served at once; past it, a new connection is closed ("session limit reached") |
| `moq.max_requests_per_session` | `1024` | requests a client may have open at once; past it, a request is refused with `EXCESSIVE_LOAD` |
| `moq.max_filter_ranges`, `moq.max_request_updates` | `16`, `64` | advertised to clients in SETUP |
| `moq.drafts` | `[21, 22, 20, 19, 18, 17]` | accepted drafts, most preferred first |
| `moq.implementation` | `trickster/<version>` | the `MOQT_IMPLEMENTATION` setup option |
| `moq.goaway_uri` | | the `moqt://` URI a GOAWAY names when the listener stops; see [Reloads and shutdown](#reloads-and-shutdown) |

A `moq` listener needs a certificate. It takes one from the `tls` block of a backend it
serves, or at runtime with `tls_runtime_certs: true`, as the
[Kubernetes controller](#kubernetes) sets. Certificate files are watched and rotated as for any
listener ([tls.md](./tls.md)).

A `moq` listener may share its UDP address and port with the HTTP/3 endpoint of an HTTPS
listener, or with another `moq` listener, as long as they serve different ALPNs. Each QUIC
connection goes to the listener whose protocol it negotiated, and each keeps its own
certificates and routes. A `moq` listener never shares a port with a `protocol: udp`
listener.

The listener's `ip_acl_name` is judged when a connection arrives, since QUIC has no TCP accept,
and the geo ACL of the backends it serves gates the session the same way. A backend's own
`ip_acl_name` does not apply to native sessions.

### The `moq` backend

| Option | Default | Description |
| --- | --- | --- |
| `origin_url` | | `moqt://host[:port]/path` (native QUIC) or `https://host[:port]/path` (WebTransport); optional |
| `hosts`, `paths` | any, `/` | the AUTHORITY and PATH, or the WebTransport request's host and path, the backend serves |
| `cache_name` | `default` | the cache older groups spill to |
| `max_ttl` | `25h` | the most a spilled group lives, and its lifetime when its track sets no `MAX_CACHE_DURATION` |
| `tls` | | the certificate served to clients, and the origin's verification |
| `moq.upstream.alb` | | an alb of `moq` backends to reach the origins through, in place of `origin_url`; see [Origin pools](#origin-pools) |
| `moq.upstream.drafts` | `[21, 20, 19, 18, 17]` | drafts offered to the origin, most preferred first |
| `moq.upstream.prewarm` | `false` | keeps the upstream subscription forwarding while every subscriber is paused |
| `moq.upstream.reconnect_backoff` | `1s` | the first wait before re-subscribing a lost upstream subscription |
| `moq.upstream.linger` | `5s` | how long an upstream subscription stays after its last subscriber leaves |
| `moq.upstream.max_gap_fetches` | `16` | upstream fetches one FETCH or fill may issue |
| `moq.live_edge.groups` | `3` | groups of each track kept in memory |
| `moq.live_edge.max_size_bytes` | `64MiB` | payload bytes the live edge holds across the backend's tracks |
| `moq.spill` | `true` | writes groups leaving the live edge, and groups fetched upstream, to `cache_name` |
| `moq.fill_timeout` | `2s` | the wait for upstream fetches when a FETCH or fill names no `FILL_TIMEOUT` |
| `moq.max_object_size_bytes` | `64MiB` | the largest object; a larger one resets its stream |
| `moq.webtransport.*` | | see [WebTransport and browsers](#webtransport-and-browsers) |
| `moq.publish.*` | | see [Publishing](#publishing) |
| `moq.authorization.*` | | see [Authorization](#authorization) |

A `moq` backend without `origin_url` (or `upstream.alb`) has no origin. It serves what
publishers send, and validation warns when it accepts no publishers either. With an origin, the
health check completes a SETUP exchange with it on a fresh session; an origin-less backend is
left unprobed.

### Routing sessions

A native session names its AUTHORITY and PATH in SETUP; when it names no authority, the TLS
server name stands in, and when it names no path, `/`. Among the backends on the listener, one
naming the host exactly wins over one naming it by wildcard, which wins over one naming no host.
Among those, the longest segment-aligned path prefix wins. A listener with one backend serves
every session with it, and a session no backend matches is closed with `INVALID_PATH`. No two
backends on a listener may claim the same host and path.

## WebTransport and browsers

A `moq` backend bound to an HTTP listener serves WebTransport sessions on that listener's HTTP/3
endpoint ([http3.md](./http3.md)); validation warns when the listener has none. The endpoint
switches to WebTransport only while a `moq` backend uses the listener, and a reload that binds
or unbinds one changes how new connections are served without restarting the endpoint.

Each of the backend's `paths` accepts WebTransport sessions at and below it (a `regex` path
matches as a pattern), and a backend with no `paths` accepts them at `/`. The usual HTTP routing
applies: on a listener shared with other backends, give the `moq` backend `hosts` or make it
`is_default`, or reach it under its name (`https://host/<backend>/`). The CONNECT request runs
through the route's middleware, so `authenticator_name`, IP and geo ACLs, metrics and access logs
all apply to it.

| Option | Default | Description |
| --- | --- | --- |
| `moq.webtransport.drafts` | `[21, 22, 20, 19, 18, 17]` | drafts accepted from WebTransport clients, most preferred first |
| `moq.webtransport.default_draft` | `0` | the draft assumed for a client that offers no `WT-Available-Protocols`, and for a WebTransport origin that answers without `WT-Protocol`; `0` refuses both |
| `moq.webtransport.allowed_origins` | | browser origins (`scheme://host[:port]`) that may open sessions, or `*`; empty allows only an origin naming the requested host, and a client that sends no `Origin` is always allowed |
| `moq.webtransport.max_sessions` | `10000` | WebTransport sessions the backend serves at once |
| `moq.webtransport.goaway_uri` | | the `https://` URI a GOAWAY names when the listener stops |

Trickster answers `403` for a refused origin, `405` for a method other than CONNECT, `400` for a
CONNECT that is not WebTransport or offers no usable draft, and `503` past `max_sessions`.
WebTransport sessions take the `moq` listener defaults for their limits. Chrome 143, Firefox 155
and Safari 26.4 send `WT-Available-Protocols` through the WebTransport `protocols` option; a
browser needs a certificate it trusts.

Toward an `https://` origin, backends that reach the same host and port with the same `tls`
settings and drafts carry their WebTransport sessions on one QUIC connection, each backend with
its own session for its own path. The connection offers WebTransport flow control, which an
origin must also enable to take several sessions on one connection. An origin that refuses a
second session is dialed one connection per session from then on. The connection closes with
its last session. Native `moqt://` origins get a connection per backend, since a native
connection is a single session.

## Caching

Each track's newest `live_edge.groups` groups stay in memory, within a backend-wide
`live_edge.max_size_bytes` budget that evicts the oldest groups first. Objects are stored once
and forwarded cut-through as their bytes arrive, so every subscriber and the cache share one
copy. A group leaving the live edge spills to `cache_name` as one entry, by reference in a
memory cache. It lives for the track's `MAX_CACHE_DURATION` less the oldest object's age,
capped by `max_ttl`. An object past its `MAX_CACHE_DURATION` is unknown again in both tiers and
is not forwarded. Spilled groups are indexed in memory, so after a restart a persistent cache's
groups are fetched from the origin again rather than found. A track found malformed (a duplicate
object differing from the first copy, or an object past the end of its group or track) is
dropped uncached and its subscribers end with `MALFORMED_TRACK`.

`FETCH` and fills (-20 and later) are served from the live edge and the cache where a group is
known in full. Each run of groups between them is fetched upstream with one request, in the
requested group order, and cached for the next request. Past `max_gap_fetches` requests, the
remaining gaps are reported as an End of Unknown Range. A group is
known in full only when every object in the requested range is present or known absent. A
publisher that never signals the end of its groups therefore has past groups fetched upstream
rather than answered incompletely. `FILL_TIMEOUT` (default `fill_timeout`, at most 30 seconds)
bounds the upstream wait. Past it, the rest of the range is reported as an End of Timed-Out
Range, and an unreachable origin as an End of Unknown Range. A FETCH that names no group order
is served ascending, as the drafts require, whatever the track's publisher prefers. Joining
FETCH, on drafts through -19, is served the same way.

## Subscriptions

One upstream subscription per track carries every downstream subscription to it, each filtered
at the relay by its own location window and range filters. A subscription forwards what arrives
after it begins; earlier objects come by fill or FETCH. `SUBSCRIBE_OK` waits until the upstream
subscription is established. Forwarding follows the subscribers: the upstream subscription
forwards while any subscriber does, and pauses otherwise unless `upstream.prewarm` keeps it
forwarding. A lost upstream subscription is re-subscribed with doubling backoff, up to five
attempts, while the downstream subscriptions stay open. Only when that fails do they end with
`PUBLISH_DONE`. An upstream subscription lingers for `upstream.linger` after its last subscriber
leaves, so a returning viewer finds it warm.

When the upstream gives an `EXPIRES`, the relay asks to extend the subscription before it
elapses, with an empty `REQUEST_UPDATE` at three quarters of the time given. A new `EXPIRES` in
the answer schedules the next renewal. The relay never ends a downstream subscription on a
timer, so it sends no `EXPIRES` downstream.

`NEW_GROUP_REQUEST` reaches the publisher as the spec requires of relays:

- with the upstream `SUBSCRIBE` while the track has no established subscription;
- afterwards, as a `REQUEST_UPDATE` to every source, but only for a value of 0 or one past the
  Largest Group, on a track whose `DYNAMIC_GROUPS` is 1, and only while no request at least as
  large is outstanding. A request is outstanding until the Largest Group increases.

## Publishing

With `moq.publish.enabled`, a backend accepts `PUBLISH` and `PUBLISH_NAMESPACE` and becomes the
origin of what publishers send. Open ingest belongs behind a network boundary or
[authorization](#authorization).

| Option | Default | Description |
| --- | --- | --- |
| `moq.publish.enabled` | `false` | accepts publishers |
| `moq.publish.namespaces` | any | namespace prefixes publishers may claim, as fields joined by `/` (`live/studio`) |
| `moq.publish.max_publishers_per_track` | `4` | publishers of one track at once; `0` allows any number |

A track can have several sources at once: publishers' `PUBLISH`es, and the subscriptions the
relay opens toward every session that published a matching namespace, or toward the origin when
none did. The first copy of each object wins, an identical duplicate is dropped, and a differing
one makes the track malformed. A publisher can take over from another without its subscribers
noticing, and one whose session drops can return on a new session before the relay gives up on
the track. The relay never pauses a publisher, so an ingest backend's cache keeps everything
published.

`SUBSCRIBE_NAMESPACE` is answered with the namespaces publishers announced, and kept current as
they come and go. A backend with an origin also learns the origin's namespaces, in either of two
ways, and announces a namespace once however many sources report it:

- It forwards the request to the origin. What an origin reports is backend-wide, so every scope
  shares the upstream requests, and their prefixes never overlap, as one session requires: a
  prefix that an upstream request covers shares it, and a wider one replaces the narrower
  requests it covers. A replaced request's namespaces stay announced while the wider one
  reports them again, so subscribers see no flap. A lost request withdraws what it reported and
  is sent again. One the origin refuses is not retried, except a `PREFIX_OVERLAP` refusal,
  which the replacement can race. One whose last subscriber leaves lingers for
  `upstream.linger`.
- It accepts `PUBLISH_NAMESPACE` from the origin on its upstream session, as moq-dev's relay
  sends, and reports those namespaces until the origin withdraws them or the session ends.

`SUBSCRIBE_TRACKS` offers each published track under the prefix with a relay `PUBLISH`. A
backend with an origin forwards it the same way, shared by prefix (as `SUBSCRIBE_NAMESPACE` with
the PUBLISH option on draft -17). Each `PUBLISH` the origin sends in answer becomes a source of
its track and is offered to every matching subscriber, in every scope. A subscriber in another
scope is served the track pulled from the origin with `SUBSCRIBE`. An origin `PUBLISH` that no
subscription covers is refused with `UNINTERESTED`.

A `SUBSCRIBE` naming a `RENDEZVOUS_TIMEOUT` waits up to that long (at most 30
seconds) for a publisher to arrive; without one, a track nobody publishes is refused with
`DOES_NOT_EXIST`. Publishers are refused with:

- `NOT_SUPPORTED` when the backend takes no publishers;
- `UNAUTHORIZED` outside `publish.namespaces`, or without a granted token;
- `EXCESSIVE_LOAD` past `max_publishers_per_track`.

## Authorization

`moq.authorization.mode` is `none` (the default) or `token`. In `token` mode, every request needs
an `AUTHORIZATION_TOKEN` that `token_file` grants, sent in the request or in the session's SETUP.

```text
# <token> <publish|subscribe|any> [namespace]
studio-encoder publish live/studio
viewer-7f3a subscribe live
ops-tools any
```

`SUBSCRIBE`, `FETCH`, `TRACK_STATUS`, `SUBSCRIBE_NAMESPACE` and `SUBSCRIBE_TRACKS` need a
`subscribe` grant, and `PUBLISH` and `PUBLISH_NAMESPACE` a `publish` grant. A grant's namespace
must prefix the request's, so a grant for `live` covers `live/studio` but not `vod`. A grant with
no namespace covers every namespace. The file is read again when it changes, checked at most
once a second; a file that no longer parses keeps its last grants, and one that cannot be read
at load fails the backend. A WebTransport CONNECT is also subject to the route's
`authenticator_name`, before any MoQT request is made.

## Origin pools

`moq.upstream.alb` names an [alb](./alb.md) whose pool members are `moq` backends with origins.
The routed backend keeps its relay, cache, live edge, WebTransport, publishing and authorization,
and reaches its origins through the alb, which picks one member per track:

```yaml
backends:
  live:
    provider: moq
    listener_names: [moq1]
    moq:
      upstream:
        alb: origins
  origins:
    provider: alb
    alb:
      mechanism: hrw
      pool: [origin-a, origin-b]
  origin-a:
    provider: moq
    origin_url: moqt://origin-a.example.com/
  origin-b:
    provider: moq
    origin_url: moqt://origin-b.example.com/
```

The mechanism must pick a single member: `rr`, `p2c`, `hrw` (each track placed by its full name)
or `lc`. When a track's origin fails, its upstream subscription moves to another member and its
subscribers see no `PUBLISH_DONE`. A member that fails its health check is passed over until it
recovers, and `stream.passive_health` on the alb ejects members whose origins cannot be dialed.
Pool members must be `moq` backends with origins; nested albs and discovery are refused.

The routed backend dials each member on a session of its own, apart from the member backend's
own relay, so the two never share a Track Alias. A `SUBSCRIBE_NAMESPACE` or `SUBSCRIBE_TRACKS`
it forwards goes to the member the prefix places, and what that member's origin publishes on the
session reaches the routed backend's subscribers.

## Priorities

MoQT orders objects by subscriber priority, then publisher priority, then group order (each 0
to 255, lower first). QUIC offers eight stream urgencies (RFC 9218), so Trickster maps the two
priorities onto urgencies 1 to 6:

| Subscriber priority | Publisher priority below 128 | Publisher priority 128 and above |
| --- | --- | --- |
| 0–63 | 1 | 2 |
| 64–191 | 3 | 4 |
| 192–255 | 5 | 6 |

Control and request streams take urgency 0. Within an urgency, an ascending subscription's
streams are sent oldest first, while a descending subscription's streams share bandwidth so its
newest group keeps moving. Datagrams have no priority, and WebTransport streams take none either,
since webtransport-go exposes no stream urgency.

## Reloads and shutdown

A reload never ends a session. A session keeps the backend it was routed to for its life,
even when a reload removes that backend; only new sessions see the new configuration. Each
backend's relay (its live edge, upstream subscriptions and upstream session) carries over to the
reloaded backend while its relay settings are unchanged. A reload that changes them starts a
new relay for new sessions, and the old one serves its sessions to their end. Publishing and
authorization policy takes effect when a reload commits. A reload restarts a `moq` listener only
when its address, port or `moq` block changes; its sessions are then drained as at shutdown.

When Trickster stops, each session gets a GOAWAY naming the listener's `goaway_uri` (or the
backend's `webtransport.goaway_uri`), with the drain's remaining time as its timeout. New
requests on a departing session are answered with `REDIRECT` to that URI on drafts that have it
(-18 and later), or refused with `GOING_AWAY`. Sessions still open when the drain ends are closed
with `GOAWAY_TIMEOUT`.

## Limits

| Limit | Value |
| --- | --- |
| sessions per listener | `moq.max_sessions` (10000) |
| open requests per session | `moq.max_requests_per_session` (1024), then `EXCESSIVE_LOAD` with a 1-second retry |
| WebTransport sessions per backend | `moq.webtransport.max_sessions` (10000) |
| object size | `moq.max_object_size_bytes` (64 MiB) |
| live edge | `moq.live_edge.max_size_bytes` (64 MiB) per backend |
| upstream fetches per FETCH or fill | `moq.upstream.max_gap_fetches` (16) |
| upstream wait for a FETCH or fill | `FILL_TIMEOUT`, at most 30 seconds |
| rendezvous wait | `RENDEZVOUS_TIMEOUT`, at most 30 seconds |
| data streams waiting for an unknown track alias | 64 per session, each for at most 2 seconds |
| namespaces | 32 fields and 4096 bytes |

## Metrics and logs

| Metric | Labels |
| --- | --- |
| `trickster_moq_sessions_total` | `listener_name`, `backend_name`, `role`, `transport` (`quic`, `webtransport`), `draft`, `result` (`ok`, `refused`, `no_route`, `busy`, `failed`) |
| `trickster_moq_active_sessions` | `listener_name`, `backend_name`, `role` |
| `trickster_moq_requests_total` | `backend_name`, `type`, `result` |
| `trickster_moq_active_subscriptions` | `backend_name`, `direction` (`upstream`, `downstream`) |
| `trickster_moq_objects_total` | `backend_name`, `direction` (`in`, `out`), `delivery` (`stream`, `datagram`, `fetch`) |
| `trickster_moq_bytes_total` | `backend_name`, `direction` |
| `trickster_moq_dropped_objects_total` | `backend_name`, `reason` (`oversize`, `delivery_timeout`, `datagram_queue_full`, `spill_queue_full`) |
| `trickster_moq_fetch_objects_total` | `backend_name`, `source` (`live_edge`, `cache`, `upstream`) |
| `trickster_moq_live_edge_bytes` | `backend_name` |
| `trickster_moq_publishers_active` | `backend_name` |
| `trickster_moq_errors_total` | `backend_name`, `class` |

Track and namespace names are never labels. Spilled groups count in the cache's own
`trickster_cache_*` metrics. Each session's close is logged at info with its listener, backend,
draft, transport, client, duration and reason.

## Kubernetes

The Kubernetes controller serves MoQT through a Gateway listener with the protocol
`trickstercache.org/moq` and `UDPRoute`s, and WebTransport through an HTTPRoute whose
`TricksterCachePolicy` names `provider: moq`. See
[kubernetes-gateway.md](./kubernetes-gateway.md#moq-relays).

## Trying it locally

The developer environment's `moq` profile runs moq-dev's relay on UDP 10443, which serves as an
origin, and `make dev-certs` writes a self-signed certificate for `localhost`:

```bash
make dev-certs
make developer-start-moq
```

Point a relay at it, with a native listener on UDP 8490 and WebTransport on the default
listener's HTTP/3 endpoint:

```yaml
listeners:
  moq1:
    protocol: moq
    port: 8490
  default:
    tls_port: 8483
    http3:
      enabled: true
backends:
  demo:
    provider: moq
    origin_url: moqt://127.0.0.1:10443/
    listener_names: [moq1, default]
    tls:
      full_chain_cert_path: docs/developer/environment/certs/trickster-dev.crt
      private_key_path: docs/developer/environment/certs/trickster-dev.key
      # the dev relay generates its own certificate
      insecure_skip_verify: true
```

`hack/moq-client` subscribes, publishes and fetches over native QUIC or WebTransport. It
publishes synthetic groups of random bytes behind a small header, which a subscriber checks
byte for byte:

```bash
# publish a group every second into the dev relay
go run ./hack/moq-client -url moqt://127.0.0.1:10443/ -insecure -pub demo/video

# subscribe through Trickster's moq listener, and by WebTransport under the backend's name
go run ./hack/moq-client -url moqt://localhost:8490/ -ca docs/developer/environment/certs/trickster-dev.crt -sub demo/video
go run ./hack/moq-client -url https://localhost:8483/demo/ -ca docs/developer/environment/certs/trickster-dev.crt -sub demo/video

# fetch groups behind the live edge, by the IDs the subscriber printed
go run ./hack/moq-client -url moqt://localhost:8490/ -ca docs/developer/environment/certs/trickster-dev.crt -fetch demo/video -from 1791148101 -to 1791148103
```

A published group's ID is the Unix time it started, so a restarted publisher never reuses a
group a relay has cached. `-draft` offers a single draft, `-token` sends an
`AUTHORIZATION_TOKEN`, `-groups` stops after that many groups, and `-h` lists the rest. Each
received object prints `verified`, `unverified` (a payload without the synthetic header), or
`CORRUPT`, and the client exits nonzero when any object fails verification.

For real media, publish a fragmented MP4 with moq-dev's CLI into the dev relay, or into a
backend that accepts publishers, and play it in moq-dev's web player through Trickster's
WebTransport endpoint with a certificate the browser trusts.
[`hack/diagstreamer`](../hack/diagstreamer/README.md) generates a live feed with a clock and a
timing strip in the picture:

```bash
go -C hack/diagstreamer run ./cmd/diagstreamer -out fmp4 | moq --connect moqt://localhost:8490/ --broadcast demo import fmp4
```

## Troubleshooting

- **A session closes with `INVALID_PATH`:** no backend on the listener matches the session's
  AUTHORITY and PATH. Check `hosts` and `paths`, or bind one backend to the listener.
- **WebTransport answers `404`:** the CONNECT matched no route. Give the backend `hosts`, make it
  `is_default`, or use the backend's name as the path prefix.
- **WebTransport answers `400`:** the browser offered no draft the backend accepts. Check
  `moq.webtransport.drafts`, or set `default_draft` for a client that offers none.
- **A subscription is refused with `DOES_NOT_EXIST`:** the origin does not have the track, or an
  origin-less backend has no publisher for it.
- **Objects stop and resume:** compare `trickster_moq_dropped_objects_total` by reason, and
  `trickster_moq_active_subscriptions{direction="upstream"}` for upstream losses.
- **FETCH reaches the origin every time:** the groups never spilled (`moq.spill: false`, or a
  cache that refused them, counted in `trickster_moq_errors_total{class="cache"}`), or the
  publisher never marks the end of a group.

## Known gaps

- No TCP fallback: WebTransport over HTTP/2 is not served, so clients need UDP.
- No packaging: Trickster does not convert between MoQT and HLS, DASH or other formats.
- Upstream subscriptions are never filtered, so `MAX_FILTER_RANGES` is not used toward origins.
- On a draft -17 origin, `SUBSCRIBE_NAMESPACE` asks for namespaces and for PUBLISHes alike, so a
  forwarded namespace request and a forwarded track request with overlapping prefixes conflict.
  The second is refused with `PREFIX_OVERLAP`, and given up after the retries.
- moq-dev's relay cannot be a WebTransport origin, because it does not support the
  `reset_stream_at` extension that WebTransport over HTTP/3 needs.
- Stream priorities and reliable reset boundaries are not available over WebTransport (see
  [Priorities](#priorities)).
