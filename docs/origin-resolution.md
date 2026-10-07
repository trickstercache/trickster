# Origin Resolution via DNS SRV

By default, an HTTP backend dials its origin by looking up the A/AAAA records of the `origin_url` host and connecting to the port in the URL. With `origin_resolution.mode: srv`, the host is instead treated as a DNS SRV owner name. Each new connection goes to a target from the SRV answer, on the port the answer publishes.

This lets a backend reach services whose ports are known only to DNS, such as:

* ECS tasks in `bridge` network mode with `hostPort: 0`, registered in AWS Cloud Map with SRV records
* Consul, Nomad or Kubernetes headless services exposed over DNS SRV

Combined with a [rule](./rule.md) and a `hostname` [request rewriter](./request_rewriters.md), one backend can serve any number of SRV destinations, chosen per request.

## Configuration

```yaml
backends:
  sites:
    provider: rp
    origin_url: 'http://_app._tcp.example.internal'  # the port, if any, is ignored in srv mode
    origin_resolution:
      mode: srv              # a (the default, A/AAAA lookup) | srv
      resolver: ''           # optional host:port of a DNS server to query directly
      min_ttl: 5s            # floor for cached answers
      max_ttl: 60s           # ceiling for cached answers
      negative_ttl: 5s       # how long NXDOMAIN or an empty answer is cached
      tls_server_name: owner # owner (the default) | target
```

| setting | default | description |
| ------- | ------- | ----------- |
| `mode` | `a` | `a` dials the URL host's A/AAAA addresses at the URL port, which is the behavior when `origin_resolution` is absent. `srv` resolves the URL host as an SRV owner name. |
| `resolver` | | The `host:port` of a DNS server to query directly. Direct queries make record TTLs visible. When empty, the Go standard library resolver is used. It reports no TTLs, so answers are cached for `min_ttl`. |
| `min_ttl` | `5s` | The shortest time an answer is cached, whatever its record TTL |
| `max_ttl` | `60s` | The longest time an answer is cached, whatever its record TTL. It also bounds how long the last good answer keeps serving while lookups fail. |
| `negative_ttl` | `5s` | How long a name with nothing to dial is cached: NXDOMAIN, an empty answer, or a failed lookup with no last good answer to serve |
| `tls_server_name` | `owner` | For `https` origins, the name the origin's certificate is verified against: `owner` is the URL host, as with any other backend; `target` is the SRV target that was dialed |

`resolver`, the TTL settings and `tls_server_name` apply only to `mode: srv`; setting them with `mode: a` is a configuration error.

The owner name is used exactly as given. Trickster does not derive a service name such as `_http._tcp` from the URL scheme.

## Routing Many Origins With One Backend

Without SRV resolution, a multi-tenant setup needs one backend per origin, each with its own fixed port, and every new origin is a config change and a reload. Origins whose ports are assigned dynamically can't be expressed at all, because an A/AAAA record carries no port.

With SRV resolution, one rule, one rewriter, one router backend and one origin backend cover every origin that has SRV records. Adding an origin means publishing its DNS records; Trickster needs no config change or reload.

```yaml
# DNS (example):
#   alpha.origins.example.com.    30 IN SRV 1 1 31842 node-07.svc.example.com.
#   _bravo._tcp.svc.example.com.  30 IN SRV 1 1 30117 node-02.svc.example.com.
#                                 30 IN SRV 1 1 30455 node-04.svc.example.com.
#   ; aliases, so a public hostname doesn't have to equal the service name:
#   bravo.origins.example.com.        60 IN CNAME _bravo._tcp.svc.example.com.
#   bravo-legacy.origins.example.com. 60 IN CNAME _bravo._tcp.svc.example.com.

request_rewriters:
  to-origin:
    instructions:
      # ${site} is the capture from the rule below. The rewritten name is
      # SRV-resolved at dial time, and the port comes from the SRV answer.
      - [ 'hostname', 'set', '${site}.origins.example.com' ]

rules:
  site-router:
    input_source: hostname
    input_type: string
    operation: rmatch
    # keep the capture strict: it becomes the upstream hostname
    operation_arg: '^(?P<site>[a-z0-9-]{1,63})\.example\.com$'
    cases:
      - matches: [ 'true' ]
        req_rewriter_name: to-origin
        next_route: origins
    # anything that doesn't match the pattern
    redirect_url: 'https://www.example.com/'

backends:
  router:
    provider: rule
    rule_name: site-router
    is_default: true

  origins:
    provider: rp
    origin_url: 'http://origins.example.com'  # placeholder; the rewriter sets the host per request
    preserve_host: true                       # the origin still sees e.g. Host: alpha.example.com
    path_routing_disabled: true
    origin_resolution:
      mode: srv
      resolver: '10.0.0.2:53'                 # optional; a direct resolver gives real TTLs
      min_ttl: 5s
      max_ttl: 60s
      negative_ttl: 5s
```

A request for `alpha.example.com` is rewritten to `alpha.origins.example.com`, which owns an SRV record, so it reaches `node-07` on port 31842. A request for `bravo-legacy.example.com` is rewritten to `bravo-legacy.origins.example.com`, whose CNAME leads to the `_bravo._tcp` records. A host that matches the pattern but has no records gets a `502 Bad Gateway` until its records are published.

Since the capture becomes a DNS name that Trickster resolves, keep the rule's pattern strict. Every distinct name a client can produce costs a lookup, and its answer is cached.

## How It Works

The transport's dial is the only thing SRV resolution changes. The request URL keeps the SRV owner name, so URL handling, the `Host` header, logging, the cache key and the connection-pool key are the same as for any backend.

### Target Selection

Targets are chosen as [RFC 2782](https://www.rfc-editor.org/rfc/rfc2782) describes:

* Targets in the lowest-priority tier (the smallest priority value) are tried first, ordered by weighted random selection. As RFC 2782 specifies, a target with weight `0` has a small chance of being chosen first, and a tier whose weights are all `0` is ordered uniformly at random.
* If a dial fails, the remaining targets in the tier are tried, then the next tier, all within the backend's 10s connect timeout. Each target gets a share of the time left, which covers resolving its name and trying each of its addresses in turn, so neither an unreachable target nor an unreachable address can use it all up.
* A target of `.` is ignored. An answer with no other targets means the service is not available.

Each target's hostname is resolved to A/AAAA addresses with the same resolver. When the DNS server includes the target's addresses in the additional section of the SRV answer, they are used without a further lookup. A target name that is an IP literal is dialed as given, and so is a URL host that a rewriter sets to an IP literal.

### Caching

Answers are cached per owner name, and target addresses per target name. Each is kept for its record TTL, clamped to `min_ttl` and `max_ttl`. Concurrent dials for a name that needs a lookup share a single in-flight query.

When a cached answer expires, the dial waits up to 500ms for the refresh. If the refresh takes longer, the dial uses the last good answer while it is within `max_ttl`, and the refresh finishes in the background. When the lookup fails (a timeout, `SERVFAIL` or `REFUSED`), the last good answer keeps serving, retried no sooner than every `min_ttl`, until `max_ttl` has passed since it was looked up. NXDOMAIN and empty answers are answers rather than failures: they replace the last good answer at once and are cached for `negative_ttl`.

Each backend's caches are bounded, so names derived from client-supplied hosts can't grow them without limit. A config reload builds new caches, and in-flight requests finish on the old transport, as with any other reload.

### Connection Pooling

`max_idle_conns` and `max_concurrent_conns` apply per SRV owner name, not per target, because the owner name is what the connection pool is keyed on. Idle connections to a target that has since left the answer are closed when they reach `keep_alive_timeout`.

### TLS

For an `https` origin, the server name sent and verified is the SRV owner name (the URL host), as for any other backend. Set `tls_server_name: target` for services whose certificates name the target host instead. It can't be combined with `tls.server_name`, which names a fixed server name for every target.

### Health Checks

A backend [health check](./health.md) probes through the same transport, so it is SRV-resolved too. Each probe reaches a single target, so it reports whether the service is reachable, not whether every target is.

Active per-target health checks, target ejection and load-balancing mechanisms are not part of SRV resolution. Use an [ALB](./alb.md) with [`dns_srv` autodiscovery](./alb-autodiscovery.md) when you need them.

## Validation

A backend with `mode: srv` fails validation when:

* `origin_url` is not `http://` or `https://`, or its host is an IP literal
* `resolver` is not a `host:port`
* `min_ttl` is greater than `max_ttl`, after defaults apply
* `tls_server_name: target` is combined with `tls.server_name`

`origin_resolution` is not supported on `alb`, `rule` and `static` backends, or with a non-HTTP `protocol`.

## Metrics

* `trickster_proxy_origin_srv_lookups_total` counts SRV lookups by `result`. The results are `hit`, `miss`, `stale` (a failed refresh served the last good answer), `negative` (a cached NXDOMAIN, empty answer or failure) and `error`.
* `trickster_proxy_origin_srv_dial_attempts_total` counts dial attempts to SRV targets by priority `tier` (`0` is the preferred tier) and `result` (`success` or `failure`).

Both are labeled by `backend_name`. See [metrics](./metrics.md).
