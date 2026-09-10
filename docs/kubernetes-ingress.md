# Kubernetes Ingress v1

Trickster serves Kubernetes `networking.k8s.io/v1` Ingress objects it claims,
translating them into its own configuration and reloading onto it. Enable the
controller with the top-level `kubernetes` section
([configuring.md](./configuring.md)) and grant it the permissions in
[kubernetes-rbac.md](./kubernetes-rbac.md).

## What is claimed

An Ingress belongs to this controller when:

- its `spec.ingressClassName` names an IngressClass whose `spec.controller`
  matches `kubernetes.gateway_class_controller_name`; or
- it names no class, and one of this controller's IngressClasses is annotated
  `ingressclass.kubernetes.io/is-default-class: "true"`; or
- it carries the deprecated `kubernetes.io/ingress.class` annotation naming
  the configured `kubernetes.ingress_class`.

Setting `kubernetes.ingress_class` narrows ownership to that one class even
when others name this controller, which is how two Trickster instances divide
a cluster. Anything not claimed is ignored entirely: not translated, not
counted, and never statused.

## Listeners

An Ingress cannot describe the port it is served on, so it names the
listeners instead — exactly as a backend names the listeners it is served
on. They are ordinary listeners, configured in the `listeners` section, so
their ports, bind addresses, body limits, timeouts and TLS settings are
tuned the same way every other Trickster listener's are:

```yaml
listeners:
  web:
    port: 8080
    max_request_body_size_bytes: 10485760
  websecure:
    tls_port: 8443
    # the certificates arrive at runtime from the Secrets the claimed
    # Ingresses reference, so the port is kept open with none behind it
    tls_runtime_certs: true
kubernetes:
  ingress:
    listener_names:
      - web
      - websecure
```

Every claimed route is served on all of them. Naming none serves claimed
Ingresses on the default frontend, which is where a backend that names no
listener is served. A listener that does not exist fails validation, the
same way an undefined listener named by a backend does.

A single listener may serve both ports, so `listeners: {web: {port: 8080,
tls_port: 8443, tls_runtime_certs: true}}` named on its own is equally
valid. Binding 80 and 443 inside a container requires running as root,
granting `NET_BIND_SERVICE`, or the `net.ipv4.ip_unprivileged_port_start`
sysctl the deployment in `deploy/kube` sets
([kubernetes-deploy.md](./kubernetes-deploy.md#ports-and-addresses)); an
install behind a load balancer that terminates TLS simply configures no TLS
port.

## Rules and paths

| Ingress | Trickster |
|---|---|
| `rules[].host` | the backend's `hosts`; a single leading `*.` wildcard is honored |
| a rule with no host | `any_host_routing` |
| `pathType: Exact` | `match_type: exact` |
| `pathType: Prefix` | `match_type: segment` |
| `pathType: ImplementationSpecific` | as `Prefix`, or `regex` with `trickstercache.org/use-regex` |
| `backend.service` | a generated backend at `http://<service>.<namespace>.svc:<port>` |
| `spec.defaultBackend` | a hostless catch-all ordered after every other route |

A Kubernetes `Prefix` matches whole path elements: `/foo` matches `/foo` and
`/foo/bar` but never `/foobar`, and a trailing slash means nothing, so
`/foo/` and `/foo` are the same rule. That is the router's `segment` match
type, so a `Prefix` path is one generated path; see
[paths.md](./paths.md#path-matching-scope).

The request reaches the Service with the `Host` header the client sent, so an
application that reads it sees the Ingress host rather than its own Service
name; cached objects are keyed by it, so a rule with no host serving several
never hands one host's object to another.

The referenced Service and port must exist in the cluster. A backend that
cannot be resolved still answers, with a fixed `500`, rather than silently
vanishing from the routing table; the reason is logged. Because Services are
watched, creating the Service later heals the route without any further
action. `backend.resource` references are not supported.

## Conflicts

Two Ingresses may claim the same host. The router resolves overlapping paths
on its own — exact before prefix before regex, longest first within each tier
— so the only genuine conflict is a duplicate of host and path once both have
been lowered. That is broken in favor of the older object; ties on creation
time are broken by namespace and name, so every replica reaches the same
answer. A declaration is reported only when it
loses everything it asked for, and keeps whatever else it claimed.

An `Exact` rule outranks a `Prefix` rule for the same path, whichever object
declared it and whichever is older, which is the precedence Kubernetes
defines. The prefix keeps everything below that path, so the two coexist and
neither is reported as a conflict.

`spec.defaultBackend` answers whatever no rule matched, so it is emitted as a
catch-all ordered after every other route, including a hostless regular
expression rule. Because it is the controller's own invention rather than
something the operator wrote, any declared rule that lowers onto the same
route takes it — a hostless `/` rule with `use-regex`, for instance — and the
unreachable default backend is reported. Two Ingresses declaring a default
backend do conflict, and the older one wins.

Regular expression paths are anchored before anything else looks at them, so
`/api/(.*)` and `^/api/(.*)` are one route rather than two that would race to
register the same pattern.

## TLS

Each `spec.tls[].secretName` must name a `kubernetes.io/tls` Secret in the
Ingress's own namespace. The certificate never travels in configuration: it
is supplied to the TLS listener at runtime and re-supplied whenever the
Secret's contents change, so rotating a Secret costs no reload at all. See
[tls.md](./tls.md#runtime-certificates).

Certificates reach whichever of the named listeners actually serves TLS;
that is a property of the listener's own configuration, so a plaintext one
is simply skipped. A listener's certificates live only as long as the
listener does, so a configuration change that removes one takes its
certificates with it; they are installed again when it comes back, without
the Secret having to change.

Because only `kubernetes.io/tls` Secrets are watched, a Secret of any other
type reads as absent, and is reported that way.

## Annotations

Annotations outside the `trickstercache.org/` namespace are another controller's
business and are ignored. One inside it that is unknown, or whose value does
not parse, is **rejected**: the annotation is not applied, the rest of the
object still translates, and the rejection is logged. Failing the whole
object would let one typo delete a live route; applying it silently would
leave an operator believing a setting is in force when it is not.

| Annotation | Value | Effect |
|---|---|---|
| `trickstercache.org/handler` | `proxy`, `proxycache` | selects the path handler; `proxycache` makes the generated backend cache-capable |
| `trickstercache.org/cache-name` | a configured cache name | which cache a caching route uses |
| `trickstercache.org/max-ttl` | a duration, e.g. `10m` | caps how long a cached object is served before revalidation |
| `trickstercache.org/negative-cache-name` | a configured negative cache name | how long error responses are cached |
| `trickstercache.org/timeout` | a duration | the upstream request timeout |
| `trickstercache.org/collapsed-forwarding` | `basic`, `progressive` | collapses concurrent requests for one object |
| `trickstercache.org/request-headers` | `Name: value` per line | header updates on the way upstream |
| `trickstercache.org/response-headers` | `Name: value` per line | header updates on the way back |
| `trickstercache.org/cors-mode` | `preserve`, `merge`, `replace`, `disable` | how origin CORS headers are combined with the configured ones |
| `trickstercache.org/cors-headers` | `Name: value` per line | the CORS headers `merge` and `replace` apply |
| `trickstercache.org/use-regex` | `true`, `false` | compiles this object's `ImplementationSpecific` paths as anchored regular expressions |
| `trickstercache.org/rewrite-target` | a path | rewrites the matched path on the way upstream |
| `trickstercache.org/health-mode` | `probe`, `provider` | how discovered members are judged healthy in the endpoint routing mode |

Durations require a unit: `600` is rejected, `600s` is not.

In a header list, a name prefixed with `-` deletes the header and one
prefixed with `+` appends to it rather than replacing:

```yaml
metadata:
  annotations:
    trickstercache.org/request-headers: |
      X-Forwarded-Host: shop.example.com
      -X-Internal-Token:
    trickstercache.org/response-headers: |
      +Vary: Accept-Encoding
```

`cache-name` and `negative-cache-name` select among things the operator has
already configured; a name the configuration does not define is rejected
like any other bad value, because emitting it would fail validation for the
whole generated configuration and stop every other route in the cluster from
reconciling. The route keeps serving, on the defaults.

That is the line the annotation set is drawn on: an annotation may **select
among what the operator provisioned**, but never grant a capability or remove
a control. What an annotation may not carry — a time series `provider`, the
cache key components, hiding the `X-Trickster-Result` header — belongs on a
`TricksterCachePolicy` targeting the Ingress or its Service, a resource with
RBAC of its own; a policy on the Ingress is written over its annotations. See
[kubernetes-cache-policy.md](./kubernetes-cache-policy.md). Anything on the
far side of that line is an operator setting under `kubernetes.defaults`,
where it applies to every generated backend and no Ingress author can change
it:

| `kubernetes.defaults` | Effect |
|---|---|
| `cache_name`, `negative_cache_name` | the defaults a route may override by annotation |
| `tracing_name` | the configured tracer generated backends report to |
| `req_rewriter_name` | a configured rewriter every generated backend runs, ahead of any route's own |
| `authenticator_name` | the configured authenticator every generated backend is behind |

`authenticator_name` in particular has no annotation and will not get one: an
annotation that can name an authenticator is one that can also omit it, and
whoever can create an Ingress in their own namespace would then be able to
take their route out from behind authentication. A name in `defaults` that
the configuration does not define fails startup, rather than the first
reconcile, because it is the operator's own mistake to see immediately.

`max-ttl`, `cache-name` and `negative-cache-name` describe caching, so they take
effect only on a route that caches — one whose effective handler is
`proxycache`, either from `trickstercache.org/handler` or from a configured
`kubernetes.defaults.cache_name`. On a non-caching route they are emitted
nowhere, because a non-caching backend would ignore them.

### Rewriting

`trickstercache.org/rewrite-target` replaces the part of the path the rule matched:

- an `Exact` or regular expression match knows the whole path it matched, so
  the path is set outright;
- a `Prefix` match knows only its leading segments, so only those are
  replaced and the rest of the path is carried through.

With `trickstercache.org/use-regex: "true"`, capture groups in the path are
available to the target as `${1}`, `${2}`, and so on (`${0}` is the whole
match, and named groups are available under their names). The pattern is
anchored at the start for you, since the API server requires every Ingress
path to begin with `/`:

```yaml
metadata:
  annotations:
    trickstercache.org/use-regex: "true"
    trickstercache.org/rewrite-target: /v2/${1}
spec:
  rules:
    - host: shop.example.com
      http:
        paths:
          - path: /api/(.*)
            pathType: ImplementationSpecific
            backend:
              service:
                name: web-svc
                port:
                  number: 8080
```

A request for `/api/orders` reaches the Service as `/v2/orders`.

## Endpoint routing mode

With `kubernetes.defaults.routing_mode: endpoint`, a rule's backend is an ALB
whose pool is the Service's ready endpoints, discovered from its
EndpointSlices, rather than a backend addressing the Service's cluster IP:
the controller generates a `discovery` entry over its own connection, a
template backend carrying the rule's settings, and a discovery-backed ALB
whose query selects the Service's port. Endpoint churn then reaches the pool
without a configuration reload, and a rolling restart of the Deployment
behind the Service drains terminating endpoints before their pods stop.
The controller's service account needs `endpointslices` list and watch for
it; see [kubernetes-rbac.md](./kubernetes-rbac.md).

`kubernetes.defaults.health_mode` decides how a discovered member is judged
healthy, and `trickstercache.org/health-mode` overrides it per Ingress:
`provider` (the default) trusts the EndpointSlice's readiness, which the
pod's own readiness probe established; `probe` runs an active health check
from the generated template, configured by `kubernetes.defaults.healthcheck`
or, when that is unset, a probe of the origin's root every 5 seconds. See
[alb-autodiscovery.md](./alb-autodiscovery.md) for the semantics of both.

## Status and Events

When `kubernetes.published_service` names the Service in front of Trickster,
its load balancer addresses (or, failing those, its external IPs) are written
into every claimed Ingress's `status.loadBalancer` by the replica holding the
leader election Lease; see
[kubernetes-gateway.md](./kubernetes-gateway.md#leader-election). Without it
nothing is written there. Whatever could not be done with an Ingress — a
rejected annotation, a missing TLS Secret, a path that could not be lowered,
a host and path lost to an older Ingress — is a `Warning` Event on the
Ingress, visible in `kubectl describe ingress`.

## Beyond what an Ingress expresses

Ingress is served indefinitely. Settings an Ingress cannot express — a
weighted canary, a redirect, TLS to a backend, TLS passthrough, per-rule
timeouts and retries — are Gateway API features; see
[kubernetes-gateway.md](./kubernetes-gateway.md). Both kinds are served at
once, so a host may be moved to an HTTPRoute without disturbing the rest.

A `TricksterCachePolicy` may target an Ingress, one of its rules' Services,
or an HTTPRoute, so caching behavior can be moved off annotations
independently. These are the equivalents:

| `trickstercache.org/` annotation | `TricksterCachePolicy` field |
|---|---|
| `handler` | `handler` |
| `cache-name` | `cacheName` |
| `negative-cache-name` | `negativeCacheName` |
| `max-ttl` | `maxTTL` |
| `timeout` | `timeout`, or an HTTPRoute rule's `timeouts` |
| `collapsed-forwarding` | `collapsedForwarding` |
| `request-headers` | `requestHeaders`, a map, or a `RequestHeaderModifier` filter |
| `response-headers` | `responseHeaders`, a map, or a `ResponseHeaderModifier` filter |
| `cors-mode`, `cors-headers` | `cors.mode`, `cors.headers` |
| `health-mode` | `healthMode` |
| `use-regex` | an HTTPRoute path match of type `RegularExpression` |
| `rewrite-target` | a `URLRewrite` filter, whose `ReplacePrefixMatch` replaces the matched prefix and `ReplaceFullPath` the whole path |

A header list becomes a map with the same `-` and `+` prefixes. A
`rewrite-target` using regular expression captures has no Gateway API
equivalent: `URLRewrite` cannot reference captures, so a `RegularExpression`
match paired with `ReplaceFullPath` rewrites to a fixed path only. A policy
on an Ingress is written over its annotations; see
[kubernetes-cache-policy.md](./kubernetes-cache-policy.md#targets-and-precedence).

## Generated configuration

Every backend, ALB and request rewriter the controller generates carries the
reserved `kgw--` prefix and a name derived from the Kubernetes object's
identity, so the names you see in logs, metrics and the management API are
stable across restarts. Listeners are not generated for Ingress: the ones an
Ingress is served on are the operator's.

Generated configuration is merged onto the file configuration and then
loaded, validated and applied by the same code that loads a configuration
file, so it is exempt from no check. A translation the daemon rejects leaves
the last good configuration serving and is not carried into later reloads.
