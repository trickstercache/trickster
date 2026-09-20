# Kubernetes Cache Policy

`TricksterCachePolicy` is the custom resource that attaches Trickster's caching behavior to
the objects the Kubernetes controller serves. It carries everything the
`trickstercache.org/*` Ingress annotations carry
([kubernetes-ingress.md](./kubernetes-ingress.md#annotations)), and three things they
never will: a time series **provider**, the **cache key** components, and whether the
`X-Trickster-Result` header reaches the client. A custom resource has RBAC of its own, so
whoever may create one is decided by the cluster rather than by whoever may edit a route.

## Installing the resource

The definition lives at
[deploy/kube/crds/trickstercachepolicies.yaml](../deploy/kube/crds/trickstercachepolicies.yaml).
Install it before the controller starts:

```sh
kubectl apply -f deploy/kube/crds/trickstercachepolicies.yaml
kubectl wait --for=condition=Established crd/trickstercachepolicies.trickstercache.org
```

The controller probes for the resource once at startup, as it does for the Gateway API,
and where the cluster does not serve it, serves every route without one. Installing the
definition later takes effect at the next restart or `kubernetes` configuration change.
The controller's service account needs `list` and `watch` on `trickstercachepolicies` and
`update` on `trickstercachepolicies/status`; see
[kubernetes-rbac.md](./kubernetes-rbac.md).

## Targets and precedence

A policy governs the objects its `targetRefs` name, in the policy's own namespace:

| `kind` | `group` | governs | `sectionName` |
|---|---|---|---|
| `Gateway` | `gateway.networking.k8s.io` | every route attached to the Gateway | not supported |
| `HTTPRoute` | `gateway.networking.k8s.io` | every rule of the route | one rule, by the rule's `name` |
| `Service` | `""` | every backendRef and Ingress backend naming the Service | one named port |
| `Ingress` | `networking.k8s.io` | every rule of the Ingress | not supported |

`group` may be omitted; when given it must be the kind's own.

Several policies may govern one rule. They are applied least specific first, each field an
inner one sets written over the outer one's, and a field none of them sets takes the
configured default:

1. the GatewayClass's `parametersRef` ([kubernetes-gateway.md](./kubernetes-gateway.md#gatewayclass-parameters)), or an Ingress's annotations;
2. a policy on the Gateway;
3. a policy on the HTTPRoute or Ingress;
4. a policy on the route's rule, by `sectionName`;
5. a policy on the backend's Service, then one on the Service's port.

Header updates fold in that order, one operation per header whatever its spelling: an
inherited `Authorization` set and a more specific `-Authorization` leave the header removed,
and a more specific `+Vary` appends to an inherited `Vary`. A map may name a header once,
across spellings and operators, because a map is applied in no particular order.
`cacheKeyParams` and `cacheKeyHeaders` replace as a whole: a policy that omits a list
inherits it, and one that sets it empty (`cacheKeyHeaders: []`) clears what was inherited.
A Service policy applies to one backendRef of a rule and not its siblings, so two members of
a weighted rule may be served differently, `resultHeader` included.

Of two policies naming one target, the older one governs it (ties on creation time break on
namespace then name) and the newer one is reported `Conflicted` on that target. A policy any
field of which cannot be honored — an unknown value, a duration without a unit, a
`cacheName` the configuration does not define — governs nothing at all and is reported
`Invalid`, since a route governed by half a policy would look configured and be something
else.

```yaml
apiVersion: trickstercache.org/v1alpha1
kind: TricksterCachePolicy
metadata:
  name: metrics
  namespace: monitoring
spec:
  targetRefs:
    - kind: Service
      name: prometheus
      sectionName: http
  provider: prometheus
  cacheName: timeseries
  timeout: 60s
  resultHeader: Hide
```

## Fields

| Field | Value | Effect |
|---|---|---|
| `handler` | `proxy`, `proxycache` | the path handler; `proxycache` makes the backend cache-capable |
| `provider` | `prometheus`, `influxdb`, `clickhouse`, `graphite` | serves the target through a time series provider; see below |
| `cacheName` | a configured cache | which cache a caching route uses |
| `negativeCacheName` | a configured negative cache | how long error responses are cached |
| `maxTTL` | a duration, e.g. `10m` | caps how long a cached object is served before revalidation |
| `timeout` | a duration | the upstream request timeout |
| `collapsedForwarding` | `basic`, `progressive` | collapses concurrent requests for one object |
| `cacheKeyParams` | query parameter names | hashed into the cache key of every caching path |
| `cacheKeyHeaders` | header names | hashed into the cache key of every caching path |
| `requestHeaders` | a map of header updates | header updates on the way upstream |
| `responseHeaders` | a map of header updates | header updates on the way back |
| `cors.mode` | `preserve`, `merge`, `replace`, `disable` | how origin CORS headers combine with the configured ones |
| `cors.headers` | a map of headers | the CORS headers `merge` and `replace` apply |
| `healthMode` | `probe`, `provider` | how discovered members are judged healthy in the endpoint routing mode |
| `loadBalancing` | `rr`, `p2c`, `lc`, `lt`, `hrw` | how traffic is spread across a Service's endpoints in the endpoint routing mode; `rr` unless set. See [the ALB mechanisms](./alb.md) |
| `loadBalancingKey` | `client_ip`, `host`, `header:<name>`, `cookie:<name>`, `query:<name>` | what `hrw` keeps on one endpoint; `client_ip` unless set |
| `resultHeader` | `Expose`, `Hide` | whether `X-Trickster-Result` reaches the client; see below |

In a header map a name prefixed with `-` deletes the header and one prefixed with `+`
appends to it rather than replacing, exactly as in the annotations and in Trickster's own
`request_headers`. Durations require a unit. `cacheName` and `negativeCacheName` select
among what the operator configured; the operator-tier names — a tracer, a request
rewriter, an authenticator — have no field here either, for the reason given in the
Ingress document: a policy that could name an authenticator could also omit one.

`maxTTL`, `cacheName`, `negativeCacheName`, `cacheKeyParams` and `cacheKeyHeaders` take
effect only on a route that caches: one whose effective handler is `proxycache`, from
`handler`, a configured `cacheName`, or a `provider`.

## Provider-aware acceleration

`provider` makes the generated backend that provider — `prometheus`, `influxdb`,
`clickhouse` or `graphite` — instead of a reverse proxy cache, so a request for
`/api/v1/query_range` behind a `prometheus` policy reaches the Delta Proxy Cache and is
served by time range rather than as an opaque object, exactly as it would from a
hand-configured `provider: prometheus` backend. See the provider documents
([prometheus.md](./prometheus.md), [influxdb.md](./influxdb.md),
[clickhouse.md](./clickhouse.md), [graphite.md](./graphite.md)) for what each accelerates.

A provider never widens a route. Its accelerated paths are reachable only beneath the
route's own `PathPrefix` match, and carry that match's headers, query parameters and
methods: a request that does not satisfy the match, that names a method the match did not
allow, or that asks for a provider path outside the prefix is served exactly as it would
be without the provider — by the next route the router would have chosen, or not at all. A
provider path that another route on the same host owns, by declaring it exactly or by a
longer prefix, is left to that route.

Each accelerated path keeps the provider's own cache key components (`query`, `start`,
`end`, `step` for a range query), which are what make acceleration possible, and takes the
policy's `cacheKeyParams` and `cacheKeyHeaders` beside them, its header updates over the
provider's own, its CORS policy, its collapsed forwarding and its result header
disposition. Behind a weighted or endpoint-mode dispatch, each member is accelerated under
its own effective policy.

Three things follow:

- **The route must expose the provider's API at its native paths.** The router selects the
  handler by the request path before any rewrite, so a `PathPrefix` of `/` or `/api` puts
  the provider's paths in reach, while a route that serves Prometheus under `/prom` and
  rewrites the prefix away never lets a request reach `/api/v1/query_range` on the
  provider. Serve a provider at its own paths, on a hostname of its own where it has to
  share a listener.
- **Only a `PathPrefix` match accelerates.** An `Exact` match names one path, and a
  `RegularExpression` match cannot be intersected with the provider's paths ahead of time;
  requests under either are served by the provider backend's plain object cache.
- **A route may not declare a path the provider predefines.** An `Exact` match for
  `/api/v1/query_range` would take the path from the provider's handler. The controller
  refuses that: the provider is withheld from that rule alone, the rest of the policy still
  applies, and both the route and the policy are told by Event. The root path is not a
  conflict, since every provider predefines it as a plain proxy catch-all.

The generated backend carries only what the policy and the configured defaults describe:
an origin, a cache, timeouts, headers. Provider settings with no policy field — a
Prometheus `instant_round`, an InfluxDB `flux` block, a Graphite `render` section — take
their defaults. MySQL is served over its own wire protocol rather than HTTP and cannot be
selected.

## Cache key components

A cached object is stored under a key derived from the backend, the path and, for a
caching path, whatever `cacheKeyParams` and `cacheKeyHeaders` name; see
[paths.md](./paths.md#cache-key-components). The policy's components reach every caching
path the route serves, the provider's predefined paths included, where they join the
provider's own. Name a header whose value partitions the cache — a tenant, an
authorization scope — and every value gets an object of its own.

## Exposing the result header

Every Trickster response carries `X-Trickster-Result`, which says how the request was
handled ([trickster-result.md](./trickster-result.md)). It is a debugging aid, and on a
gateway in front of the public it discloses which paths are cached and how. `resultHeader:
Hide` withholds it from the client on every path the policy governs. Metrics and the
access log still record the result: the value is kept for the access log's
`%{cache-status}x` and `%{engine}x` fields, so an operator sees what the client does not;
hand-written configuration does the same with the `hide_result_header` path option
([paths.md](./paths.md#hiding-the-result-header)). The disposition is
the serving path's: behind a weighted rule that hides the header, a member whose Service
policy says `Expose` exposes it, and a response the dispatch answered itself, with no
member selected, follows the rule's.

## Shared caches

The controller's generated backends use whatever `kubernetes.defaults.cache_name`, a
GatewayClass's parameters, or a policy names, and the `default` in-memory cache when nothing
does. Memory is right for one replica and for objects that are cheap to fetch again; it is
per replica, so two replicas behind one Service each fetch and store their own copy, and a
restart starts cold.

For several replicas, or for time series that are expensive to backfill, configure a Redis
cache ([caches.md](./caches.md#redis)) and name it:

```yaml
caches:
  shared:
    provider: redis
    redis:
      endpoint: redis.trickster.svc:6379
kubernetes:
  defaults:
    routing_mode: service
    cache_name: shared
```

Every replica then reads and writes one store, a request served by any replica warms the
cache for all of them, and a rolling restart keeps what was cached. A policy may still
select a different configured cache for the routes it governs, so a shared store for time
series and memory for everything else is one `cacheName` on one policy.

Cache keys are stable across restarts and replicas. They are derived from the Kubernetes
object's identity rather than the rule's position, so inserting a rule does not invalidate
what its neighbors cached. Renaming a route or moving it between namespaces changes the
key; editing its rules does not.

## Status and Events

The controller writes one `status.ancestors` entry per `targetRefs` entry, under its own
`controllerName`, with an `Accepted` condition:

| `reason` | meaning |
|---|---|
| `Accepted` | the policy governs the target |
| `Conflicted` | an older policy governs the target; the message names it |
| `TargetNotFound` | the target is not in a watched namespace |
| `Invalid` | the target's `kind`, `group` or `sectionName` is not supported, the target is named twice, or the policy as a whole cannot be honored; the message says which |

Status is written by the replica holding the leader election Lease, as every other status
is ([kubernetes-gateway.md](./kubernetes-gateway.md#status)), and by none under
`read_only`. Entries other controllers wrote are kept. The same verdicts, and a provider
withheld from a rule, are `Warning` Events on the policy, visible in `kubectl describe
trickstercachepolicy`; the withheld provider is an Event on the route as well.

A policy's target is judged to exist when the object is in a watched namespace, whether or
not this controller claims it: a policy on an HTTPRoute attached to another controller's
Gateway is `Accepted` and has no effect.
