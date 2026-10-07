# Rate Limiting

Trickster counts requests, connections, sessions, or datagrams against named limiters and refuses what exceeds a limit. Limiters are defined once under `rate_limiters` and attached by `rate_limiter_name`. The exhaustive example is [`examples/conf/example.full.yaml`](../examples/conf/example.full.yaml).

```yaml
rate_limiters:
  per-client:
    keys: [client_ip]          # empty means one bucket for every event
    ipv6_prefix: 64            # leading bits of an IPv6 client address; default 64
    limit: 600                 # events per window
    window: 1m                 # 1s to 1h; default 1m
    algorithm: sliding_window  # the only algorithm
    missing_key: exempt        # exempt (default) | shared
    action: reject             # reject (default) | count | close
    policy_headers: none       # none (default) | ietf | legacy
    max_keys: 100000           # buckets tracked; default 100000
    max_keys_action: allow     # allow (default) | reject
    # unit: requests           # leave unset when the limiter is used on more than one plane
    response:
      status: 429              # 400-599; default 429
      body: "Too Many Requests\n"
      headers:
        Cache-Control: no-store

listeners:
  default:
    rate_limiter_name: per-client

backends:
  api:
    rate_limiter_name: per-client
    paths:
      - path: /healthz
        rate_limiter_name: none   # clears the backend limiter; the listener limiter remains
```

## Where a limiter applies

`rate_limiter_name` attaches a limiter to a listener, a backend, or a path. `none` is reserved and is not a limiter name. On a listener or a backend it is an undefined name and the configuration fails to load. On a path it clears the backend limiter for that path.

Two decisions can apply to one HTTP request:

- The listener limiter is its own decision. A path name does not replace it, and `none` does not clear it.
- The route decision is the path limiter when the path names one. That name replaces the backend limiter for that path. An empty path `rate_limiter_name` inherits the backend limiter. `none` clears it, so the route makes no rate-limit decision.

When both are set, each judges the request. Naming the same limiter on the listener and on the route spends two events from that limiter's buckets. A listener allowance can be spent by a request that a later route access list denies. A route access-list denial happens before the route limiter, so it does not spend the route allowance.

The same limiter name is one set of buckets everywhere it is attached, for as long as its keys, IPv6 prefix, window, limit, and `max_keys` stay the same. `missing_key`, `max_keys_action`, `action`, the HTTP response, and `policy_headers` can change on reload while those buckets stay.

`unit` says what an event is: `requests`, `connections`, `sessions`, or `datagrams`. Leave it unset when the limiter is attached on more than one plane. An unset unit uses `requests` on HTTP, `connections` on `tcp` and `tls`, and `sessions` on `udp`, and those events share one quota. A limiter attached on more than one plane with `unit` set fails to load. `close` is valid only on stream listeners. A native listener (`mysql`, `postgres`, `clickhouse`, `flight-sql`) cannot name a limiter.

A cache hit is still an event: the limiter judges the request before the cache.

## The estimate

`algorithm` is `sliding_window`. Empty means that. Each bucket keeps the count for the previous window and the count for the current one. At a moment `frac` of the way through the window, the estimate is the previous count times `(1 - frac)`, plus the current count. An event is allowed when the estimate plus one is at most `limit`. The event that would pass the limit is not added, except in `count` mode, which records it and still lets it through.

This is an estimate, not a log of every event. A burst at the end of one window and the start of the next can weigh as less than the number of events, so a client can be allowed slightly past what an exact sliding log would allow.

A bucket that is not read for a little more than two windows is dropped. The table may drop it up to a second early, or up to a sixteenth of that idle time when that is shorter.

## Keys

`keys` is up to five attributes, in order. Together they are one bucket. Empty `keys` is one bucket for every event.

| Key | Read on |
|---|---|
| `client_ip` | HTTP, HTTP/3, `tcp`, `tls`, and `udp`. The client address after [`trusted_proxies`](./configuring.md#trusted-proxies), never the port. IPv6 keeps the leading `ipv6_prefix` bits (default 64), so a client that rotates its privacy address stays on one key. |
| `host` | HTTP. The request host, without its port, without regard to case. |
| `header:<name>` | HTTP. The first value of that request header. |
| `cookie:<name>` | HTTP. The value of that request cookie. |
| `query:<name>` | HTTP. The raw value of that query parameter. |
| `query` | HTTP. The query string as the client sent it. |
| `method` | HTTP. The request method. |
| `path` | HTTP. The request path, without the query string, after path normalization. |
| `sni` | `tls` only. The server name the client offered. It is known after the handshake, so the connection is counted then. |
| `proxy_tlv:<type>` | `tcp` and `tls` listeners that accept a PROXY protocol header. `<type>` is the TLV type, as a decimal or hex integer. |
| `user` | The authenticated name on a native session. No listener that can carry a limiter reads it, so a limiter keyed on `user` cannot be attached. |

A key the attachment cannot read fails to load. A listener limiter can use only keys that need nothing but the connection or the request itself.

`missing_key` is `exempt` or `shared`. Empty means `exempt`. `exempt` lets an event through without counting it when a key has no value, such as a header the request did not send. `shared` puts every such event in one extra bucket.

`client_ip` is the address Trickster already resolved. Clients behind one NAT share a key. A listener behind a proxy that is missing from `trusted_proxies` sees that proxy as the client, so every request shares the proxy's key. On a stream listener that reads a PROXY header and lists no `trusted_proxies`, a `client_ip` limiter still loads and warns: an empty trusted-proxy list believes every peer's header.

Two different identities that hash to the same 64-bit key share a bucket, and therefore one quota. A collision spends the allowance faster. It does not open a second one.

## Capacity

`max_keys` is how many buckets the limiter tracks. Empty means 100000. The table is split into as many as 64 shards, and each shard rounds its share up, so the table can hold up to 63 buckets past `max_keys`. A new key can also be refused while the table is still under `max_keys`, once the shard it lands in is full.

`max_keys_action` is what a new key gets when it does not fit. Empty means `allow`: the event proceeds and is not counted. `reject` treats it as over the limit. Both record the `full` result.

Each tracked key is a 40-byte counter plus the table entry that holds it. On a 64-bit process that comes to about 110 bytes of heap per key, including the map slot.

## HTTP

On an HTTP or HTTP/3 listener the limiter sits after the listener IP access list and path normalization, and before the router. The route limiter sits after the route's IP and geo access lists and before the authenticator. The readiness path, after normalization, is exempt on every listener except the metrics listener. A health check is not exempt. An ACME `http-01` challenge is answered before any limiter.

`action` is `reject` or `count` on HTTP. Empty means `reject`. `close` fails to load when the limiter is attached to anything HTTP serves.

`reject` writes the configured response. The status is 400–599 and defaults to 429. The body defaults to `Too Many Requests\n` and is omitted for `HEAD`. `Cache-Control: no-store` is always set. `Retry-After` is the wait until a new event could pass, in whole seconds, rounded up, and at most about two windows. A full table that rejects uses the window length. A response header cannot replace `Retry-After`, the quota headers below, `Content-Length`, `Transfer-Encoding`, or a hop-by-hop field.

`policy_headers` adds quota fields to responses the limiter allowed as well as to refusals:

- `none` adds none.
- `ietf` adds `RateLimit-Policy` (`"<name>";q=<limit>;w=<window seconds>`) and `RateLimit` (`"<name>";r=<remaining>;t=<seconds until the window ages out>`). Each limiter adds its own pair.
- `legacy` sets `X-RateLimit-Limit`, `X-RateLimit-Remaining`, and `X-RateLimit-Reset` when those fields are still absent, so the route limiter's values win over the listener's.

`count` forwards the event after recording it, including one that is over the limit and a new key a rejecting full table would have stopped. The limiter appends its name to the request header `X-Trickster-Rate-Limited`. A listener that has a limiter, or that serves a backend or path which names one, removes any value the client sent before judging.

A refusal, a counted event, and a full table write one debug log line, `rate limit decision`, with the limiter name and the result. Allows are not logged.

## Streams

A `tcp` or `tls` listener counts a connection unless `unit` says otherwise. A `udp` listener counts a session unless `unit` is `datagrams`.

- `connections` are counted when the connection is accepted, from `client_ip` and `proxy_tlv`. A key of `sni`, or an IP or geo access list that still judges after the flow is known, waits until then, so that list can refuse the connection before it is counted. `sni` is always counted after the handshake.
- `sessions` are counted once, when the UDP flow opens. A refused session is held until a new event could pass, and that wait does not count again. A full table that rejects holds for the window.
- `datagrams` are counted on every datagram, including the first.

`reject` resets a TCP or TLS connection and drops a UDP datagram. `close` finishes a TCP or TLS connection and drops a UDP datagram. `count` allows the event after the same single charge. A connection that ends before the stage that counts it is not counted. That includes a TLS name that routes nowhere.

A ClickHouse query that arrives over HTTP uses the HTTP route limiter. That is separate from a limiter on a native session, which a native listener does not enforce.

## Reload and restart

A reload that keeps the limiter's name, keys, IPv6 prefix, window, limit, and `max_keys` keeps its buckets. Changing any of those starts that limiter empty. Removing the limiter drops its buckets after the new configuration is in place. A reload that fails to apply leaves the previous buckets alone. Restarting the process starts every limiter empty. The clock is monotonic for the life of the process, so a step of the wall clock does not roll a window backward or forward.

## Metrics

`trickster_ratelimit_decisions_total` counts each decision. The labels are `limiter`, `plane` (`http` or `stream`), and `result`:

| `result` | Meaning |
|---|---|
| `allowed` | counted, and at or under the limit |
| `limited` | over the limit, refused, and not counted |
| `counted` | over the limit in `count` mode, recorded, and still forwarded |
| `exempt` | not counted: a missing key under `exempt`, or the readiness path |
| `full` | a new key that did not fit. `allow` forwards it without counting; `reject` refuses it |

`trickster_ratelimit_keys` is the number of buckets that limiter holds at scrape time, labeled by `limiter`. See [metrics.md](./metrics.md).

## Kubernetes

`kubernetes.defaults.rate_limiter_name` puts one limiter on every HTTP and gRPC route the controller generates. A stream route does not take it. A GatewayClass parameter of the same name overrides it for that class. The limiter must use HTTP keys, a unit of `requests` or unset, and an action other than `close`. There is no Ingress annotation and no `TricksterCachePolicy` field, and a generated route cannot set the name to `none`. The name is not copied onto pool members, endpoint templates, mirror targets, or generated listeners. See [kubernetes-ingress.md](./kubernetes-ingress.md) and [kubernetes-gateway.md](./kubernetes-gateway.md).
