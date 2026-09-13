# Kubernetes Gateway API

Trickster serves the `gateway.networking.k8s.io/v1` objects it claims —
GatewayClass, Gateway, HTTPRoute and ReferenceGrant — translating them into
its own configuration and reloading onto it. Enable the controller with the
top-level `kubernetes` section ([configuring.md](./configuring.md)) and grant
it the permissions in [kubernetes-rbac.md](./kubernetes-rbac.md). Ingress v1
is served alongside; see [kubernetes-ingress.md](./kubernetes-ingress.md).
Caching behavior — a cache, TTLs, a time series provider, the cache key — is
attached to Gateways, routes and Services with the `TricksterCachePolicy`
resource; see [kubernetes-cache-policy.md](./kubernetes-cache-policy.md).

The Gateway API is optional at runtime. Its CRDs are an add-on that most
clusters do not have, so the controller probes for the group at startup and,
where it is absent, watches none of its kinds and serves Ingress alone.

## What is claimed

A GatewayClass belongs to this controller when its `spec.controllerName`
equals `kubernetes.gateway_class_controller_name`. A Gateway belongs to it
when its `spec.gatewayClassName` names a claimed class, and a route of any
served kind (HTTPRoute, GRPCRoute, TCPRoute, TLSRoute, UDPRoute) attaches
through a `parentRef` naming a claimed Gateway. Anything else is ignored
entirely: not translated, not counted, and never statused. A parentRef
naming a Gateway this controller does not claim is another controller's
business and draws no complaint.

## GatewayClass parameters

A GatewayClass may carry a `parametersRef` to a ConfigMap. Its `data` keys
are the `kubernetes.defaults` fields, spelled the same way, and override
those defaults for every route served through a Gateway of the class:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: trickster-cached
spec:
  controllerName: trickstercache.org/gateway-controller
  parametersRef:
    group: ""
    kind: ConfigMap
    name: cached-gateway-params
    namespace: trickster
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: cached-gateway-params
  namespace: trickster
data:
  cache_name: objects
  negative_cache_name: api-errors
  timeout: 45s
  authenticator_name: gateway-auth
```

| Key | Value |
|---|---|
| `routing_mode` | `service` or `endpoint` |
| `cache_name`, `negative_cache_name` | a configured cache or negative cache |
| `tracing_name`, `req_rewriter_name`, `authenticator_name` | a configured tracer, request rewriter or authenticator |
| `timeout` | a duration with a unit, such as `30s` |
| `health_mode` | `probe` or `provider`, for generated discovery-backed ALBs |

Unlike an Ingress annotation, a GatewayClass may set the operator-tier names,
because a GatewayClass is cluster-scoped infrastructure and whoever can write
its ConfigMap decides the defaults for the class. A `TricksterCachePolicy` on
a Gateway, an HTTPRoute, one of its rules, or a backend's Service is written
over the class's parameters, most specific last; see
[kubernetes-cache-policy.md](./kubernetes-cache-policy.md#targets-and-precedence).

Because those parameters carry operator controls, a class whose parameters
cannot be honored is **not served**: none of its Gateways open a port and
none of the routes attached to them are emitted, until the parameters are
fixed. That covers a `parametersRef` that cannot be read (wrong kind, no
namespace, ConfigMap absent or outside the watched namespaces) and any key
that is unknown, does not parse, or names something the configuration does
not define. Every affected Gateway is reported with the reason. A ConfigMap
with no keys is a class with no overrides, and is served.

## Listeners

Each Gateway listener becomes a Trickster listener bound to the declared
port. `HTTP` opens a plaintext port; `HTTPS` opens a TLS port with
`tls_runtime_certs`, whose certificates arrive from the referenced Secrets at
runtime; `TCP`, `TLS` and `UDP` open a [stream listener](./configuring.md#stream-listeners)
of the matching protocol, which relays what it receives without reading it.
Listeners on one port — within a Gateway or across Gateways — merge onto one
Trickster listener, because a port can be bound once; a `UDP` listener binds
in its own port space, so it may share a port number with a `TCP`, `TLS`,
`HTTP` or `HTTPS` listener. Two listeners on one port must serve one
protocol; where they do not, the listener of the older Gateway (then the
earlier listener) keeps the port and the other is reported and not served.
Listeners of different Gateways on one port may share a hostname, or have
none: each admits its own routes and the port serves the union, with
overlapping routes resolved by the ordinary precedence (the older route
wins). On `HTTPS` the merged listeners' certificates share one store, which
answers for a name with the first certificate carrying it, so two listeners
on one port, of one Gateway or two, may not bring different certificates
carrying one name (a wildcard being a name of its own); the older Gateway's
listener, then the earlier one, keeps the name and the other is reported
with `HostnameConflict` and not served until a rotation ends the overlap. A
Secret rotated to unusable material withdraws nothing, so on a port whose
store holds a certificate under it that certificate keeps its names until a
usable rotation replaces it, while on a port holding nothing under it the
Secret claims nothing.
Two Gateways naming one hostname on one port must also name the same
certificate. Hostless `HTTPS` listeners merge whatever their certificates,
each selected by the names it carries, so long as those names are disjoint.
A Gateway may not repeat a hostname on a port within itself, which the API
refuses before it reaches the controller.

`hostname` may be precise or a single leading wildcard label
(`*.example.com`). It restricts the routes the listener admits, as described
below. A `TCP` or `UDP` listener cannot carry one, since nothing in the
stream names a host; a listener that declares one is refused with
`UnsupportedValue`. A `TLS` listener's hostname is matched against the server
name a client offers.

On an `HTTPS` listener `tls.mode` must be `Terminate` (the default). Every
`certificateRefs` entry must name a `kubernetes.io/tls` Secret, in the
Gateway's namespace or in one whose ReferenceGrant permits it. A listener
that cannot terminate TLS at all — no `tls` block, no `certificateRefs`, or
`Passthrough` — is not served. A reference that merely does not resolve yet
is reported and the listener still opens, so that creating the Secret later
heals it without any further change. Certificates never travel in
configuration: they are supplied to the running listener and re-supplied when
the Secret changes, so a rotation costs no reload. See
[tls.md](./tls.md#runtime-certificates) for how a listener holds them and for
the certificate inventory that lists them.

On a `TLS` listener `tls.mode` must be `Passthrough`: the listener relays the
client's handshake to the backend the server name selects and terminates
nothing, so it holds no certificate and `certificateRefs` are ignored. The
mode defaults to `Terminate`, which is refused with `UnsupportedValue`
rather than assumed; a `TLS` listener that would terminate and forward plain
TCP is not served in this release.

`allowedRoutes.namespaces.from` is honored: `Same` (the default), `All`, and
`Selector` against the labels of the route's Namespace. `allowedRoutes.kinds`
may list the kinds the listener's protocol carries: `HTTPRoute` and
`GRPCRoute` on `HTTP` and `HTTPS`, `TCPRoute` on `TCP`, `TLSRoute` on `TLS`
and `UDPRoute` on `UDP`. Any other kind is reported as unsupported on the
listener, and a listener admitting no supported kind admits no route.

**Deviation:** `allowedRoutes` decides which routes attach to a listener; it
does not fence requests. Listeners on one port share one router, which
resolves a request by hostname across every route served on the port, so a
request for `shop.example.com` whose path no route on the
`shop.example.com` listener claims may be answered by a route attached to a
`*.example.com` listener on the same port — including a route from a
namespace the precise listener would not have admitted. The Gateway API's
`GatewayHTTPListenerIsolation` feature, under which the most specific
listener alone answers its hostname, is not supported.

A listener no route has attached to is bound all the same: a generated
backend of its own answers `404` for whatever arrives, so the port is open
and an `HTTPS` listener already holds its certificates before its first
route, and the listener is `Programmed` as soon as it is declared.

Not supported in this release, reported and ignored: `spec.addresses`
(addresses come from `kubernetes.published_service`), `spec.tls`, and
`spec.allowedListeners` (ListenerSets). A Gateway naming
`spec.infrastructure.parametersRef` is refused with `InvalidParameters` and
not served, since nothing reads Gateway parameters and serving it without
them would misrepresent what was asked.

Binding a port below 1024 inside a container requires running as root,
granting `NET_BIND_SERVICE`, or the `net.ipv4.ip_unprivileged_port_start`
sysctl the deployment in `deploy/kube` sets
([kubernetes-deploy.md](./kubernetes-deploy.md#ports-and-addresses)); a
Gateway declaring port 80 behind a Service that maps it from a high
container port is not yet expressible, so declare the port the pod may bind.

## Route attachment and hostnames

A `parentRef` selects a claimed Gateway, optionally narrowed by
`sectionName` or `port`. The route attaches to each selected listener whose
`allowedRoutes` admit it and whose hostname intersects the route's:

- a listener with no hostname admits whatever the route names, or every
  hostname when the route names none;
- a route naming no hostname takes the listener's;
- otherwise each route hostname is kept when it equals the listener's or
  falls within its wildcard (`*.example.com` admits `shop.example.com` and
  `deep.shop.example.com`), and a wildcard route hostname is narrowed to a
  precise listener it covers.

When both sides name hostnames and none agree, the route is not accepted on
that listener, and a parentRef none of whose listeners accept the route is
reported with the reason.

The route is then served once per port and hostname it was accepted on.
Listeners that merged onto one port serve it for the union of the hostnames
they admitted.

A wildcard hostname spans any number of labels, as the Gateway API defines:
a route served on `*.example.com` answers `shop.example.com` and
`deep.shop.example.com`, but not `example.com`. See
[paths.md](./paths.md#path-matching-scope) for the order hosts are tried in.

## Matches and precedence

| HTTPRoute | Trickster |
|---|---|
| `path.type: Exact` | an exact path match |
| `path.type: PathPrefix` | a whole-segment prefix match |
| `path.type: RegularExpression` | a regular expression match, anchored at the start |
| no `path` | `PathPrefix /` |
| `method`, `headers`, `queryParams` | conditions on the matched path |
| `backendRefs` with `weight` | a weighted round-robin pool over the references |

A Kubernetes `PathPrefix` matches whole path elements: `/foo` matches `/foo`
and `/foo/bar` but never `/foobar`, and a trailing slash means nothing.

Requests are resolved by host first — the exact hostname, then wildcards
covering it from the nearest label boundary outward, then hostless routes —
then by path, exact before longest prefix before regular expression, then by
method, and finally by the match's header and query parameter conditions,
most specific first. The Gateway API's own precedence decides among matches
that meet on one path and method: a match naming a method first, then more
header matches, then more query parameter matches, then the older route.

- A header or query value is compared whole, and a `RegularExpression` one
  must match the whole value. The field must be present to match at all: a
  pattern the empty string satisfies, such as `.*`, matches a field that is
  present and empty but not one that is absent. A match naming one header or
  query parameter twice uses the first condition, whatever the case.
- A method that no match claims on a path some match did claim reaches the
  covering match on the same host where one exists, so a `POST` to a path
  that only names `GET` still reaches the prefix behind it, and answers 404
  otherwise. That fill does not cross into a less specific host tier.
- Two matches testing the same path, method and predicates are a duplicate:
  the older route keeps it (ties on creation time break on namespace then
  name) and the other is reported. An `Exact` match outranks a `PathPrefix`
  on the same path and a match naming a method outranks one that does not,
  whatever their ages, which is the order the Gateway API defines.

## Backend references

Each `backendRefs` entry must be a Service (the default kind) with a `port`
that exists on it. A reference into another namespace needs a ReferenceGrant
in that namespace permitting `HTTPRoute` from the route's namespace to
`Service`, optionally by name. `weight` defaults to 1; a weight of 0 sends no
traffic and drops the reference.

The request reaches the Service with the `Host` header the client sent, and
cached objects are keyed by
it, so two hosts one backend serves never share an object; a `URLRewrite`
`hostname` replaces it, and then keys nothing. A Service port whose
`appProtocol` is `kubernetes.io/h2c` is spoken to in cleartext HTTP/2 by
prior knowledge. A headless Service (`clusterIP: None`) resolves to its
pods: under `service` routing it is dialed on the port's numeric
`targetPort`, and one naming its `targetPort` by name, which only its pods
resolve, is refused there with `UnsupportedValue`; under `endpoint` routing
the pods are discovered through the EndpointSlices, which name the port, so
both shapes are served. The routing mode that decides is the one the route's
policies leave in force (class parameters, route, rule, then the Service's
own policy).

A reference that cannot be resolved — unsupported kind, no permitting grant,
Service or port absent — keeps its slot and its weight, and its share of the
traffic answers with a fixed `500`, which is what the Gateway API requires.
A rule with no usable reference answers `500` outright. Because Services are
watched, creating the Service later heals the route. Several references
become a weighted round-robin pool, apportioned exactly by integer weight.

### BackendTLSPolicy

A `BackendTLSPolicy` whose `targetRefs` name a Service, optionally one named
port of it through `sectionName`, makes every connection to that Service
TLS, verified as the policy says:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: BackendTLSPolicy
metadata:
  name: web-tls
  namespace: shop
spec:
  targetRefs:
    - group: ""
      kind: Service
      name: web-svc
      sectionName: https
  validation:
    caCertificateRefs:
      - group: ""
        kind: ConfigMap
        name: web-ca
    hostname: web.shop.internal
```

`validation.hostname` is sent as the SNI and is the name the certificate is
verified against. `caCertificateRefs` name ConfigMaps, or `kubernetes.io/tls`
Secrets, in the policy's namespace whose `ca.crt` key holds a PEM bundle;
`wellKnownCACertificates: System` trusts the system store instead. The two
are alternatives, as the API defines them: a bundle from `caCertificateRefs`
is the whole of the trust for that backend, so a certificate from a public
authority is refused, and only a policy selecting `System` trusts the system
roots. The bundle travels in configuration, so a rotated bundle costs a
reload; a CA certificate is public material. A port-specific policy
outranks one for the whole Service, and of two policies selecting one target
the older wins and the other is reported.

A policy that cannot be honored — a reference that does not resolve or holds
no certificate, a wildcard hostname, `subjectAltNames`, an unknown well-known
set — makes every backendRef it governs **invalid**: the reference answers
`500` as an unresolvable one does, because connecting without the
verification the policy asked for would be worse. `options` are
implementation-specific and are reported and ignored. Only `Service`
targets are supported.

## Filters

Filters on a rule apply to every request it matches; filters on a
`backendRef` apply after them, to the requests that reference receives.

`RequestHeaderModifier`, `ResponseHeaderModifier`, `URLRewrite`,
`RequestRedirect` and `RequestMirror` are supported.

Within one header modifier a header may be named once, across `set`, `add`
and `remove` and whatever its case; a filter naming `Authorization` under
`set` and `authorization` under `remove` has no defined order and makes the
route not served. Header names are case-insensitive throughout. Across
filters, modifications fold into one operation per header in the order they
apply, the rule's filters before a backendRef's: a `set` followed by an `add`
of the same header sets the joined value; an `add` after a `remove` is a
`set`; a `remove` after a `set` removes.

`replacePrefixMatch` replaces the declared prefix on a segment boundary, so
with a `PathPrefix` of `/api` and a replacement of `/v2`, `/api` becomes
`/v2`, `/api/` becomes `/v2/` and `/api/orders` becomes `/v2/orders`; an
empty replacement is `/`. It requires the rule to have exactly one match,
of type `PathPrefix`, and on a backendRef it also requires the rule to have
exactly one backendRef: behind a weighted dispatch the member no longer
knows which prefix was matched. A rule and one of its backendRefs may not
both rewrite the path.

`RequestRedirect` answers with the status it names (`301`, `303`, `307` or
`308`, or `302` by default) and a `Location` composed from the request as the filter leaves
it: `scheme`, `hostname`, `port` and `path` replace their parts of the
request URL and the rest is kept. An explicit `scheme` with no `port` sends
the client to the scheme's well-known port; neither `scheme` nor `port`
sends it back to the Gateway listener's port, whatever port the request's
`Host` header carried; and a port that is the well-known one for the scheme
is omitted. A redirecting rule forwards nothing, not even a request carrying
`Connection: Upgrade`, which is answered with the redirection like any
other. It is still a backend the operator's controls apply to: an
authenticator from `kubernetes.defaults` or the GatewayClass's parameters
guards it exactly as it guards a forwarding rule, and a
`ResponseHeaderModifier` on the same rule applies to the redirection.
`backendRefs` written on a redirecting rule are ignored and reported, and it
may not also carry a `URLRewrite`. Filters apply in the order declared, and a
redirect ends the request: a `RequestHeaderModifier` ahead of it modifies the
request the redirect answers, so a `Host` it sets is the `Location`'s
hostname when the redirect names none, while one after it has no request
left to modify and is reported and ignored. The redirect filter alone
defines the `Location`: a `ResponseHeaderModifier` on the same rule that sets, adds or
removes `Location`, in any case, makes the route not served rather than
being applied in one order or the other and silently losing to the
redirect. Express the target through the redirect's own `scheme`,
`hostname`, `port` and `path`.

`RequestMirror` copies the share of requests it names — `percent`, or
`fraction` rounded to a whole percent — to the Service its `backendRef`
resolves to, discarding their responses; see
[Mirroring Requests](./paths.md#mirroring-requests) for what the data plane
does with a copy. The reference resolves exactly as a forwarding
`backendRef` does, ReferenceGrants and BackendTLSPolicy included. One that
does not resolve is the one filter failure the API defines as partial: the
mirror is dropped, the route's `ResolvedRefs` is `False`, and the rest of
the route is served. A mirror on a rule fires once per request, ahead of a
weighted dispatch; one on a `backendRef` fires for the requests that
reference receives, and a rule served by one `backendRef` carrying both
fires both. A filter copying nothing (`percent: 0`) makes the route
not served.

A filter this build cannot honor — `ExtensionRef`, `CORS`, `ExternalAuth`,
a repeated filter type, a malformed hostname, header, scheme, port or
status — makes the route **not served** until it is fixed, because ignoring
a filter would serve the route wrongly rather than partially.

## Timeouts and retry

A rule's `timeouts` and `retry` apply to every request it serves, behind a
weighted dispatch included; see
[Timeouts and Retries](./paths.md#timeouts-and-retries) for their exact
semantics. `timeouts.request` bounds the whole upstream exchange, retries
included, and `timeouts.backendRequest` each attempt; a zero duration
bounds nothing, and `backendRequest` may not exceed `request`. A request
that runs out of either with no attempt answered is answered `504`. `retry`
repeats an idempotent request after a connection failure and after a
response whose status is in `codes`, `attempts` times (once when unset, at
most ten), waiting `backoff` between attempts. The Gateway API defines no
retry budget, so every eligible request is retried. A value the data plane
cannot express makes the route not served, and `sessionPersistence` is
reported and ignored.

## GRPCRoute

A GRPCRoute attaches, ranks and resolves exactly as an HTTPRoute does, and
lowers onto what gRPC is on the wire: every call is a `POST` to
`/{service}/{method}`. A `method` match with `service` and `method` is an
exact path; `service` alone is the path prefix beneath the service; `method`
alone matches the method under any service; and a `RegularExpression` type
matches each segment as a pattern, an absent one matching any segment. A
`headers` match is a header match. `RequestHeaderModifier`,
`ResponseHeaderModifier` and `RequestMirror` are the HTTPRoute's filters;
`ExtensionRef` is not honored. The generated backends select the `proxy`
handler, which relays trailers, and speak cleartext HTTP/2 by prior
knowledge to a plaintext Service, or HTTP/2 over TLS to one a
BackendTLSPolicy governs, since gRPC needs HTTP/2 end to end. A
`TricksterCachePolicy` on a Gateway or a Service still governs a GRPCRoute's
backends for the operator-tier controls, but a GRPCRoute caches nothing and
a policy cannot target one.

An HTTP or HTTPS listener admits both kinds by default and
`allowedRoutes.kinds` may restrict it to one; `supportedKinds` says which.
An HTTPRoute and a GRPCRoute attached to one listener with intersecting
hostnames cannot both be served: the older is, and the newer is refused
with `Accepted: False` and reason `RouteConflict`, which is the resolution
the Gateway API defines. The controller watches GRPCRoutes only in a cluster
whose Gateway API serves the kind; see
[kubernetes-rbac.md](./kubernetes-rbac.md).

## TCPRoute, TLSRoute and UDPRoute

The stream route kinds attach as an HTTPRoute does, through a `parentRef`
naming a claimed Gateway, narrowed by `sectionName` or `port` and subject to
the listener's `allowedRoutes`; a TCPRoute attaches only to `TCP` listeners,
a TLSRoute only to `TLS` listeners and a UDPRoute only to `UDP` listeners,
and one naming a listener of another protocol is refused with
`NotAllowedByListeners`. Each is served with exactly one rule, since nothing
in a stream selects among rules; a route declaring more is refused with
`UnsupportedValue`. The rule's `backendRefs` resolve as an HTTPRoute's do
(a Service port, ReferenceGrants for another namespace), with their weights,
and an unresolvable reference keeps its share of the connections and refuses
them, as the Gateway API requires; a BackendTLSPolicy does not apply, since
the stream is relayed unread.

A `TCP` or `UDP` listener carries one route: the oldest route naming it is
served and every later one is refused with `RouteConflict`. A TLSRoute's
`hostnames` are the server names it serves, intersected with the listener's
hostname as an HTTPRoute's are; a route naming none serves every server name
the listener admits. Listeners on one port bind one socket, so each server
name on a port belongs to one route, the oldest, however many listeners on
that port admitted it: a route admitted by a wildcard listener and a precise
one on the same port is served once there, a newer route that names only
names already served on the port is refused with `RouteConflict`, and one
that names some is served for the rest and told which it lost. A route
admitted by several Gateways on one port takes the first Gateway's policy.
A connection is relayed to the route whose hostname matches the server name
it offers, a precise name before a wildcard, and a connection whose server
name no route serves, or that offers no ClientHello at all, is closed.

A backendRef's port is resolved by the route's transport: a TCPRoute or
TLSRoute selects the Service port of that number carrying TCP and a UDPRoute
the one carrying UDP, so a Service exposing one port number over both, with
different names and targets, is reached at the right target in either mode;
a Service exposing the number over the other transport alone is an
unresolved reference, keeping its weight and refusing its share.

Every connection, and every UDP client's session, is committed to one
backendRef by weighted round robin, and in the endpoint routing mode to one
of that Service's ready endpoints in turn; a member that cannot be dialed
refuses the connection rather than passing it to a sibling, an unresolved
reference refuses its share without a lookup, and it is the Service's
readiness that takes an endpoint out of rotation. The
`health_mode` parameter does not apply to stream members, which are always
judged by readiness, since no probe speaks the protocol they carry. A stream
route caches nothing, and a `TricksterCachePolicy` cannot target one;
`kubernetes.defaults` and a GatewayClass's parameters reach it only for
`routing_mode`.

The controller watches the three kinds only in a cluster whose experimental
Gateway API channel serves them (`gateway.networking.k8s.io/v1alpha2`); see
[kubernetes-rbac.md](./kubernetes-rbac.md). Their status is written as an
HTTPRoute's is.

## Endpoint routing mode

With `routing_mode: endpoint`, from `kubernetes.defaults` or a GatewayClass's
parameters, a backendRef is served by an ALB whose pool is the Service's
ready endpoints, discovered from its EndpointSlices, rather than a backend
addressing the Service's cluster IP. Each pool carries everything the
backendRef would have carried — cache, timeout, TLS, filters. Endpoint churn
reaches the pools without a configuration reload, and a rolling restart
drains terminating endpoints before their pods stop. The controller's
service account needs `endpointslices` list and watch for it; see
[kubernetes-rbac.md](./kubernetes-rbac.md).

`health_mode` decides how a discovered member is judged healthy:
`provider` (the default) trusts the EndpointSlice's readiness, which is
what the pod's own readiness probe already established; `probe` runs an
active health check, configured by `kubernetes.defaults.healthcheck` or,
when that is unset, a probe of the origin's root every 5 seconds. See [alb-autodiscovery.md](./alb-autodiscovery.md)
for the semantics of both.

## Status

Every claimed object is told what became of it, by one replica at a time
(see [Leader election](#leader-election)):

- **GatewayClass**: `Accepted` is `True` for a claimed class, or `False`
  with reason `InvalidParameters` when its `parametersRef` cannot be
  honored, in which case none of its Gateways is served.
- **Gateway**: `Accepted` is `True` when every listener is valid, `True` with
  reason `ListenersNotValid` when only some are, and `False` when none is.
  `Programmed` follows `Accepted`, and is lowered to `False` with reason
  `Pending` when the data plane rejected the generated configuration, or
  `AddressNotAssigned` when `kubernetes.published_service` is configured but
  that Service has no address yet. `status.addresses` carries the published
  Service's load balancer addresses, or failing those its external IPs.
- **Gateway listeners**: one entry per declared listener, with `Accepted`,
  `Programmed`, `ResolvedRefs` and `Conflicted`, `supportedKinds` (the kinds
  the listener's protocol carries, those of them `allowedRoutes.kinds`
  admits, or nothing when it admits no kind this controller serves) and
  `attachedRoutes`, which counts only routes that are
  served. A refused listener keeps its entry and says why
  (`UnsupportedProtocol`, `UnsupportedValue`, `ProtocolConflict`,
  `HostnameConflict`, `InvalidCertificateRef`). A certificateRef that does
  not resolve lowers `ResolvedRefs` (`InvalidCertificateRef`,
  `RefNotPermitted`) without refusing the listener, and an unsupported route
  kind lowers it with `InvalidRouteKinds`.
- **HTTPRoute** (and every other route kind): one `parents` entry, under
  this controller's name, per parentRef naming a claimed Gateway. `Accepted`
  is `True` when at least one listener accepted the route, or `False` with
  `NoMatchingParent`, `NotAllowedByListeners`, `NoMatchingListenerHostname`,
  `RouteConflict` for a stream route whose listener or server names an older
  route already serves, or `UnsupportedValue` for an unroutable hostname, a
  filter that cannot be honored, or a stream route with more than one rule,
  each of which unserves the whole route. `ResolvedRefs` is `False`
  with `BackendNotFound`, `InvalidKind`, `RefNotPermitted` or
  `UnsupportedProtocol` (a BackendTLSPolicy that cannot be honored) when any
  backendRef did not resolve; the route stays attached and the unresolved
  reference answers with an error. Entries other controllers wrote are left
  untouched, and a parentRef naming a Gateway this controller does not
  claim gets no entry.

Every condition's `observedGeneration` is the generation that was
translated, and a verdict is written only onto that generation of that
object, so an object edited after translation, or recreated under the same
name, is never labeled with a verdict about its predecessor.

A Gateway whose class stops being accepted keeps its Gateway and listener
entries — the listeners `Accepted` on their own terms, `Programmed: False`
because the Gateway is not served — and every HTTPRoute naming it is refused
with `NoMatchingParent` and the class's reason, so a class rejection reaches
the routes rather than leaving them with the acceptance they had. An HTTPS
listener is `Programmed` only while the data plane holds a usable certificate
for it: one whose only material cannot be parsed is `Programmed: False` with
reason `Invalid`, while a rotation to unusable material leaves the certificate
already serving in place, so the listener stays `Programmed` with
`ResolvedRefs: False` saying why the new material was not taken.

Conditions of other types and other controllers' entries are kept, and an
object whose status already says what the pass concluded is not written at
all, so a resync that changes nothing makes no API call. A write the API
server refuses is logged, counted in
`trickster_kgw_status_write_failures_total`, and retried on the next pass.
Status writing never delays the routes and certificates a pass carries.
Status is not withdrawn from an object once it stops being claimed; the new
owner overwrites it.

## Events

Everything a pass could not do with an object is a `Warning` Event on that
object, visible in `kubectl describe`: `Rejected` for a translation problem,
`InvalidAnnotation` for a rejected annotation, `InvalidCertificate` for a TLS
Secret that is missing or unusable, `CertificateRejected` for one a
listener's certificate store refused, `InvalidParameters` for a
GatewayClass's parameters, and `Invalid`, `Conflicted` or `TargetNotFound`
for a `TricksterCachePolicy`. A claimed GatewayClass gets a `Normal` `Accepted`
Event. An Event is published when the problem first appears, and again when
the replica publishing it takes over leadership, not on every resync;
client-go's aggregation folds repeats and rate-limits per object.

## Leader election

Every replica watches, translates and programs its own data plane. Status and
Events are written by one replica, elected over a Lease
(`kubernetes.leader_election`), so two replicas never fight over one
object's status. Every write is bound to the leadership term it began under,
so a replica that loses the Lease stops writing at once and cannot overwrite
what its successor wrote. A replica that loses the election keeps serving.
`leader_election.enabled: false` makes every replica a writer, for a
single-replica deployment that wants no Lease; `read_only: true` makes none
of them one, and needs no write permission at all. The Lease lives in
`leader_election.namespace`, the pod's own namespace by default, and
`lease_duration` must be at least one second because the Lease API stores it
in whole seconds.

The leader is visible as `trickster_kgw_leader`; the controller's other
metrics are in [metrics.md](./metrics.md). When `kubernetes.defaults.tracing_name`
names a tracer, every reconcile pass is traced on it: `kgw.reconcile`, with
`kgw.translate`, `kgw.compile`, `kgw.apply` and `kgw.certificates` beneath
it, and each status write as a `kgw.status` trace of its own, since it runs
on its own worker.

## Pod readiness

The readiness endpoint (`/trickster/ready`; see
[Graceful shutdown](./configuring.md#graceful-shutdown-and-readiness)) reports
`503 not programmed` from before the listeners open until the controller's
first translation is serving, and `200 ready` from then on. A translation the
data plane rejects programs nothing, so the pod stays not ready until a later
reconcile succeeds. A pod is therefore not added to its Service's endpoints
until the routes it exists to serve are in place, which is what makes a
rolling update of the controller Deployment invisible to clients. A
controller that cannot start, because the API server is unreachable or the
service account lacks a permission, keeps the pod not ready, so the rollout
waits rather than replacing a working pod with one that serves nothing. A
reload that changes the `kubernetes` section restarts the controller without
touching readiness, since the routes already programmed keep serving; a
reload that enables the section after it was off holds readiness again until
the new controller publishes.

## Tuning for generic web ingress

The defaults suit a caching proxy in front of an API. A Gateway serving
arbitrary web traffic through Trickster should consider these settings on
the listeners it generates and in `kubernetes.defaults`:

| Setting | Where | Suggested | Why |
|---|---|---|---|
| `max_request_body_size_bytes` | listener | 10 MiB (the default), higher for upload paths | A request body above it is refused with `413` before it reaches an origin; `truncate_request_body_too_large` is for logging, not proxying |
| `read_header_timeout` | listener | 10s | Bounds a client that sends headers slowly; has no effect on the body or the response |
| `connections_limit` | listener | 0 (unlimited), or the pod's file descriptor budget | A limit blocks accepts rather than refusing them |
| `proxy_protocol`, `trusted_proxies` | listener | the load balancer's addresses | The real client address in logs and `max_query_range` decisions; see [Trusted Proxies](./configuring.md#trusted-proxies) |
| `timeout` | `kubernetes.defaults` | 60s (the default) | Bounds how long an origin may take to start a response and how long its body may stall; a route's `timeouts` bound more |
| `max_object_size_bytes` | backend | 512 KiB (the default), higher for large cached objects | A response above it is served but never cached; streaming responses are unaffected |
| `access_log` | `kubernetes.defaults` | the `json` preset to stdout | One line per request with the route, upstream and request identifiers; see [access-logs.md](./access-logs.md) |

A response the origin streams is relayed as it arrives on every handler;
the caching handlers buffer only what they store. Range requests are served
through the cache as described in [range_request.md](./range_request.md).

## Conformance

The controller is tested against the upstream
[Gateway API conformance suite](https://gateway-api.sigs.k8s.io/concepts/conformance/)
for the `GATEWAY-HTTP` profile on every change, against the experimental CRD
channel. `make kind-conformance` (or `kind-conformance-docker` on macOS) runs
it against a local kind cluster, and CI publishes the report as a workflow
artifact.

The core result is `partial` rather than a core conformance claim: every core
test passes but `HTTPRouteMultipleGateways`, which is skipped. It expects two
Gateways declaring one port to answer at distinct addresses, which one
process serving every Gateway of a class at one address cannot do; the
Gateways merge on the port as described under Listeners. A deployment that
needs Gateways at distinct addresses runs one controller Deployment per
GatewayClass ([kubernetes-deploy.md](./kubernetes-deploy.md#ports-and-addresses)).

The report lists the extended features claimed. They cover query parameter,
method and port matching, request and response header modification on rules
and backendRefs, path, host, scheme and port rewrites and redirects, request
mirroring, request and backend timeouts, retries, named route rules, and
WebSocket and h2c backend protocols.

## Not supported

| Feature | Status |
|---|---|
| `GatewayHTTPListenerIsolation` | Not supported; see the deviation under Listeners |
| `spec.addresses`, `GatewayStaticAddresses` | Addresses come only from `kubernetes.published_service` |
| `spec.infrastructure.parametersRef` | The Gateway is refused with `InvalidParameters` |
| ListenerSets (`spec.allowedListeners`) | Reported and ignored |
| Frontend and backend client certificates | Not supported |
| `CORS` and `ExternalAuth` filters | Refused; the route is not served |
| Multiple `RequestMirror` filters on one rule | A rule carries one mirror |
| `BackendTLSPolicy` status, `subjectAltNames` | The policy is honored; its status is not written back and `subjectAltNames` is refused |
| A `TLS` listener in `Terminate` mode | Not served; `Passthrough` only |
| `sessionPersistence` | Reported and ignored |
| Rate limiting | Rate-limit at the load balancer in front, or at the origin |

Session affinity has no Gateway API equivalent here: a weighted rule
apportions each request independently, so an origin that needs affinity
should carry its own session state, or be served by a single `backendRef` in
`routing_mode: service` so the Service's own session affinity applies.

## Generated configuration

Every listener, backend, ALB and request rewriter the controller generates
carries the reserved `kgw--` prefix and a name derived from the Kubernetes
object's identity, so the names you see in logs, metrics and the management
API are stable across restarts. A route served on two listeners is served by
one set of objects per port and hostname, each with its own cache keys, so an
object cached through one port is not served through the other.

Generated configuration is merged onto the file configuration and then
loaded, validated and applied by the same code that loads a configuration
file, so it is exempt from no check. A translation the daemon rejects leaves
the last good configuration serving and is not carried into later reloads.
