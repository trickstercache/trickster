# Kubernetes Controller RBAC

Every permission the Kubernetes Gateway/Ingress controller uses, and why. This
is the source the Helm chart's Role and ClusterRole are generated from; a verb
that does not appear here is one the controller does not use.

The controller is entirely absent unless the top-level `kubernetes`
configuration section is present, so a Trickster deployment that is not a
gateway needs none of this.

## Watched resources

The controller reads these to build its routing model. It never writes them
themselves; the `get` verbs on the four statused kinds exist only to re-read
an object whose status write was refused as a conflict, and a read-only
instance does not need them.

| API group | Resource | Verbs | Scope | Why |
| --- | --- | --- | --- | --- |
| `gateway.networking.k8s.io` | `gatewayclasses` | get, list, watch | Cluster | Deciding which classes name this controller; `get` re-reads one whose status write conflicted |
| `gateway.networking.k8s.io` | `gateways` | get, list, watch | Namespaced | Listeners, hostnames, TLS references; `get` as above |
| `gateway.networking.k8s.io` | `httproutes` | get, list, watch | Namespaced | Route matches and backend references; `get` as above |
| `gateway.networking.k8s.io` | `grpcroutes` | get, list, watch | Namespaced | gRPC method matches and backend references; only in a cluster whose Gateway API serves the kind, and `get` as above |
| `gateway.networking.k8s.io` | `tcproutes`, `tlsroutes`, `udproutes` | get, list, watch | Namespaced | Stream route backend references and, for TLSRoute, server names; only in a cluster whose experimental Gateway API channel (`v1alpha2`) serves the kind, and `get` as above |
| `gateway.networking.k8s.io` | `referencegrants` | list, watch | Namespaced | Permitting cross-namespace Service and Secret references |
| `gateway.networking.k8s.io` | `backendtlspolicies` | list, watch | Namespaced | TLS to backends; only in a cluster whose Gateway API serves the kind |
| `networking.k8s.io` | `ingressclasses` | list, watch | Cluster | Deciding which classes name this controller, and which is the cluster default |
| `networking.k8s.io` | `ingresses` | get, list, watch | Namespaced | Hosts, paths, backends, TLS references; `get` as above |
| (core) | `services` | list, watch | Namespaced | Resolving backend references to an address and port; also in `published_service`'s namespace, for its addresses |
| (core) | `secrets` | list, watch | Namespaced | TLS certificates for HTTPS listeners |
| (core) | `configmaps` | list, watch | Namespaced | A GatewayClass's `parametersRef` and a BackendTLSPolicy's CA bundle; only in a cluster serving the Gateway API |
| (core) | `namespaces` | list, watch | Cluster | When `namespace_selector` is configured, or the cluster serves the Gateway API |
| `discovery.k8s.io` | `endpointslices` | list, watch | Namespaced | Only with `routing_mode: endpoint`: the generated discoverer reads the endpoints of every referenced Service over the controller's connection |
| `trickstercache.org` | `trickstercachepolicies` | get, list, watch | Namespaced | Caching policy on Gateways, routes and Services; only in a cluster that serves the resource, and `get` as above |

The `gateway.networking.k8s.io` grants are needed only in a cluster that
serves the Gateway API; its CRDs are an add-on. The controller probes for the
group at startup and, where it is absent, watches none of its kinds and serves
Ingress objects alone. Granting the verbs for resources the cluster does not
define is harmless, so one Role covers both cases; a cluster that will never
install the CRDs can leave them out.

`secrets` access is narrowed server-side to `type=kubernetes.io/tls`, so the
controller never holds an application's credentials in memory. The grant
itself cannot express that narrowing, which is the strongest reason to scope
the controller to named namespaces where that is possible.

`namespaces` is requested when `namespace_selector` is configured, and in any
cluster that serves the Gateway API, because a Gateway listener may admit
routes by the labels of their namespace (`allowedRoutes.namespaces.from:
Selector`) and only the Namespace object carries those labels. An Ingress-only
controller using `watch_namespaces` avoids both the cluster-wide namespace
read and the cluster-scoped Role binding for it.

`configmaps` is requested only in a cluster that serves the Gateway API, for
the ConfigMap a GatewayClass's `parametersRef` may name and the CA bundle a
BackendTLSPolicy's `caCertificateRefs` may name. It is watched in the same
namespaces as Services, so a reference into a namespace outside
`watch_namespaces` is reported as not found.

`backendtlspolicies` graduated to the Gateway API's `v1` later than the core
kinds, so a cluster may serve the group without it. The controller reads the
served resources at startup and builds the informer only where the kind is
served; a policy on a cluster without it is simply never seen.

`trickstercachepolicies` is this project's own custom resource
([kubernetes-cache-policy.md](./kubernetes-cache-policy.md)), installed from
`deploy/kube/crds`. The controller probes for it at startup exactly as it
probes for the Gateway API and watches it only where the cluster serves it,
so the grant is harmless on a cluster without the definition.

EndpointSlices are not watched by the controller itself: endpoint churn is
the autodiscovery provider's job and reaches the data plane through pool
mutation rather than a configuration reload. In the `endpoint` routing mode
the controller generates a discoverer over its own connection, so the
service account then also needs the `endpointslices` grant above in every
namespace whose Services routes reference, exactly as a hand-configured
discoverer would; see the RBAC section of
[alb-autodiscovery.md](./alb-autodiscovery.md). In the `service` routing
mode it is not needed.

## Cluster writes

Status and Events are the only writes the controller makes; everything above
is read. Setting `read_only: true` gives up all of them, and the grant below
along with them, for an instance whose service account cannot or should not
write to the cluster. It still watches, translates and serves traffic —
read-only describes the relationship with the cluster, not the data plane.

| API group | Resource | Verbs | Scope | Why |
| --- | --- | --- | --- | --- |
| `gateway.networking.k8s.io` | `gatewayclasses/status` | update | Cluster | Accepted condition on claimed classes |
| `gateway.networking.k8s.io` | `gateways/status` | update | Namespaced | Accepted and Programmed conditions, listener conditions, and published addresses |
| `gateway.networking.k8s.io` | `httproutes/status` | update | Namespaced | Per-parent Accepted and ResolvedRefs conditions |
| `gateway.networking.k8s.io` | `grpcroutes/status` | update | Namespaced | The same, for GRPCRoutes |
| `gateway.networking.k8s.io` | `tcproutes/status`, `tlsroutes/status`, `udproutes/status` | update | Namespaced | The same, for the stream route kinds where the cluster serves them |
| `networking.k8s.io` | `ingresses/status` | update | Namespaced | `status.loadBalancer` addresses |
| `trickstercache.org` | `trickstercachepolicies/status` | update | Namespaced | Per-target `Accepted` conditions |
| (core) | `events` | create, patch | Namespaced | Translation errors, rejected annotations, certificate failures, class acceptance; `patch` is how client-go counts a repeated Event |

## Leader election

Required only when `leader_election.enabled` is true (the default) and the
instance is not read-only: the election decides which replica writes status
and Events, so an instance that writes neither contends for nothing. Every
replica programs its own data plane regardless, so a replica that loses an
election still serves traffic.

| API group | Resource | Verbs | Scope | Why |
| --- | --- | --- | --- | --- |
| `coordination.k8s.io` | `leases` | get, create, update | Namespaced | The election itself, in `leader_election.namespace` |

## Address publishing

Required only when `published_service` is configured: the controller watches
that one Service (`list`, `watch` on `services`, narrowed by a field selector
to its name) in the Service's own namespace, which need not be one of the
watched namespaces, and publishes its assigned addresses into Gateway and
Ingress status.
