# Deploying on Kubernetes

`deploy/kube` carries two raw-YAML deployments. `configmap.yaml`,
`deployment.yaml` and `service.yaml` run Trickster as a caching proxy with
the exhaustive example configuration; the `gateway-*.yaml` manifests run it
as the Kubernetes Gateway API and Ingress controller described in
[kubernetes-gateway.md](./kubernetes-gateway.md) and
[kubernetes-ingress.md](./kubernetes-ingress.md). The Helm chart at
<https://github.com/trickstercache/helm-charts> is built from these
manifests, so what they do is what the chart does with its values applied.

## What is in deploy/kube

| File | What it is |
|---|---|
| `gateway-namespace.yaml` | the `trickster` namespace every controller manifest names |
| `gateway-rbac.yaml` | ServiceAccount, ClusterRole and binding, and the leader election Role, per [kubernetes-rbac.md](./kubernetes-rbac.md) |
| `gateway-classes.yaml` | the `trickster` GatewayClass and IngressClass |
| `gateway-configmap.yaml` | the controller's configuration: Ingress listeners, probe listener, the `kubernetes` section |
| `gateway-deployment.yaml` | two replicas, readiness and liveness probes, drain settings, a restricted security context |
| `gateway-service.yaml` | the LoadBalancer whose address is published into status, and a ClusterIP for metrics |
| `gateway-pdb.yaml` | keeps one replica through voluntary disruptions |
| `gateway-hpa.yaml` | optional CPU autoscaling |
| `crds/trickstercachepolicies.yaml` | the `TricksterCachePolicy` resource ([kubernetes-cache-policy.md](./kubernetes-cache-policy.md)) |
| `configmap.yaml`, `deployment.yaml`, `service.yaml` | the plain caching proxy |
| `graphite-*.yaml`, `mysql-*.yaml` | demonstrations layered on the plain proxy |

## Installing the controller

The Gateway API is an add-on, installed from its own release. Install the
version the controller is built against (`sigs.k8s.io/gateway-api` in
`go.mod`); the `standard` channel carries the `v1` kinds, and the
`experimental` channel additionally carries HTTPRoute retries and the
TCPRoute, TLSRoute and UDPRoute kinds. The `TricksterCachePolicy` definition
ships in `deploy/kube/crds`. Both go in before the controller starts, since
it probes for them once at startup:

```sh
make -C deploy/kube install-gateway-crds                                    # standard channel
make -C deploy/kube install-gateway-crds GATEWAY_API_CHANNEL=experimental
```

A cluster that will never serve the Gateway API skips the first apply and
the GatewayClass in `gateway-classes.yaml`; the controller then serves
Ingress objects alone. Then the controller itself, in this order:

```sh
make -C deploy/kube bootstrap-trickster-gateway
```

which applies the namespace, RBAC, classes, ConfigMap, Deployment, Service
and PodDisruptionBudget and waits for the rollout. A pod is ready only once
its controller has translated the cluster's routing objects and the data
plane serves them, so the rollout completing means the controller is
serving. Verify what it claimed:

```sh
kubectl get gatewayclass trickster            # ACCEPTED True
kubectl get ingressclass trickster
kubectl -n trickster get service trickster-gateway   # the published address
kubectl get gateway,httproute,ingress -A      # PROGRAMMED, the address, and Accepted parents
```

A Gateway of the class opens the ports it declares; an Ingress naming the
class is served on the listeners the ConfigMap configures:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: edge
  namespace: shop
spec:
  gatewayClassName: trickster
  listeners:
    - name: http
      port: 80
      protocol: HTTP
```

## Configuration

`gateway-configmap.yaml` is the primary configuration file, mounted at
`/etc/trickster/trickster.yaml`. It carries what the cluster's routing
objects cannot: the listeners claimed Ingresses are served on, the listener
the probes use, logging, the `kubernetes` section, and the operator tier
(`kubernetes.defaults`) no route may change. Everything the controller
generates from Gateways, routes and Ingresses is merged after it, through
the same validation, and may only add objects: it can never change `main`,
`logging`, `mgmt` or a listener the file defines. Every option is described
in `configmap.yaml`, which is the example configuration wrapped in a
ConfigMap.

### Fragments in conf.d

A second ConfigMap, `trickster-gateway-conf-d`, is mounted at
`/etc/trickster/conf.d` and is optional. Each of its keys becomes a file
there, and every `.yaml`, `.yml` or `.conf` file in the directory is merged
into the primary file in name order, as described under
[Multiple Configuration Files](./configuring.md#multiple-configuration-files). It is
where the operator tier is provisioned without touching the primary file:
the caches, negative caches, tracers, request rewriters and authenticators
that `kubernetes.defaults`, a GatewayClass's parameters, a
`TricksterCachePolicy` or an Ingress annotation may then select by name.

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: trickster-gateway-conf-d
  namespace: trickster
data:
  caches.yaml: |
    caches:
      objects:
        provider: memory
        index:
          max_size_bytes: 536870912
  authenticators.yaml: |
    authenticators:
      gateway-auth:
        provider: basic
        users:
          api: ${API_PASSWORD_HASH}
```

A chart renders its values into exactly these fragments; a hand-run
deployment edits them in place.

### How a change reaches the pod

Two patterns, which may be combined:

**In-place reload.** The manifests set `mgmt.auto_reload_interval`, so the
running process polls its files and reloads when one changes, through the
same validation and graceful path a SIGHUP uses. The kubelet refreshes a
mounted ConfigMap within about a minute of an edit (its sync period), by an
atomic symlink swap the poll sees and filesystem notifications do not, which
is why polling is the mechanism. A reload that fails validation leaves the
previous configuration serving, logged as a failed reload; caches and
listeners whose settings did not change are kept, and a listener whose
settings did change is restarted with a drain. The volumes are whole-volume
mounts and never `subPath` mounts: a `subPath` mount is a copy the kubelet
does not refresh, so the poll would see nothing.

**Rollout on a checksum.** A chart that wants every configuration change to
pass through a rollout, with the Deployment's history as its rollback, puts
a hash of the rendered ConfigMap in a pod template annotation
(`checksum/config` in `gateway-deployment.yaml`); a changed hash rolls the
pods. Readiness holds each new pod out of the Service until its controller
has programmed the routes, and the old pod drains only after it has been
removed, so the rollout is invisible to clients. This is the pattern for a
change the in-place reload cannot make, and for a Secret consumed through
the environment.

### Secrets

Credentials never go in a ConfigMap. The configuration expands `${VARIABLE}`
in its credential-bearing fields, listed under
[Configuring Secrets](./configuring.md#configuring-secrets-or-sensitive-information):
authenticator users, a Redis `password`, a user router's `to_credential`, and
header values in path, CORS, health check and discovery blocks. Mount a
Secret into the environment
(`envFrom.secretRef` in `gateway-deployment.yaml`) and reference its keys.
A container's environment is fixed at start, so a rotated Secret reaches
the pod through a rollout, not a reload. TLS certificates for HTTPS Gateway
listeners and Ingress TLS sections are read from the `kubernetes.io/tls`
Secrets the objects reference and pushed into the listeners at runtime,
rotated without a reload; no certificate is mounted.

## Ports and addresses

A Gateway listener binds the port it declares, in the pod, and
`gateway-service.yaml` forwards by port number, so a Gateway declaring 80 is
reached at the load balancer's port 80. The pod runs as an unprivileged user
and binds 80 and 443 through the `net.ipv4.ip_unprivileged_port_start`
sysctl the Deployment sets, which every current Kubernetes release treats as
safe. The alternative, adding `NET_BIND_SERVICE` to the container, does not
reach a non-root process: the capability is dropped at `execve` unless the
binary carries file capabilities, which the image does not set. A Gateway
declaring some other port needs a matching Service entry.

A port is bound once, so a port belongs either to a Gateway or to a listener
configured in the ConfigMap. The manifests give 80 and 443 to Gateways and
serve Ingresses on 8080 and 8443, which the Service also forwards. To serve
Ingresses on 80 and 443 instead, move the `web` and `websecure` listeners to
those ports in the ConfigMap and have Gateways declare others; the Service
forwards by number and needs no change.

The addresses published into Gateway and Ingress status are those of the
Service named by `kubernetes.published_service`, a LoadBalancer here. On a
cluster with no load balancer implementation, give the Service `externalIPs`
or make it a NodePort, or install one such as MetalLB; the status address
follows whatever the Service is assigned.

Every Gateway of the class is served by every replica at the one published
address, and two Gateways declaring one port merge on it as described under
[Listeners](./kubernetes-gateway.md#listeners). A deployment that needs
Gateways at distinct addresses runs one controller Deployment per
GatewayClass: a copy of the manifests with its own namespace or names, its
own Service, and a distinct `gateway_class_controller_name` (and
`ingress_class`) that only that class names.

## Scaling and availability

The Deployment starts two replicas spread across nodes, with a
PodDisruptionBudget keeping one through drains. Every replica watches the
cluster, translates and serves every route; one, elected over the Lease
`kubernetes.leader_election` names, writes status and Events, so adding
replicas adds data-plane capacity and nothing else, which is what the
optional HorizontalPodAutoscaler scales on. The resource requests are
starting points.

A rolling update surges one pod in before one goes (`maxSurge: 1`,
`maxUnavailable: 0`), and `terminationGracePeriodSeconds` covers the
`preStop` sleep (which lets the endpoint removal propagate before SIGTERM)
plus `mgmt.shutdown_drain_timeout`, with margin; see
[Graceful shutdown](./configuring.md#graceful-shutdown-and-readiness).

## RBAC

`gateway-rbac.yaml` grants exactly the verbs
[kubernetes-rbac.md](./kubernetes-rbac.md) lists, cluster-wide, because the
shipped configuration watches every namespace. A controller narrowed with
`kubernetes.watch_namespaces` binds the namespaced kinds through a Role in
each watched namespace and keeps only `gatewayclasses`, `ingressclasses` and
`namespaces` cluster-wide; `read_only: true` drops every write. The
`endpoint` routing mode adds `endpointslices`, left commented in the file.

## The image

`trickstercache/trickster` (also `ghcr.io/trickstercache/trickster`) is a
statically linked binary built with `CGO_ENABLED=0` on a distroless static
base: no shell, no package manager, no libc. It runs as `nobody` (65534) and
the manifests pin that with `runAsNonRoot` and a read-only root filesystem.
The image carries the CA bundle, the license notices of every linked
dependency under `/licenses`, and the example configuration at
`/etc/trickster/trickster.yaml`, which the mounted ConfigMap replaces.
Images are signed; the README shows the `cosign verify` invocation. The
Kubernetes client and Gateway API libraries the controller is built on are a
fixed part of the binary whether or not the `kubernetes` section is
configured, and a Trickster without that section holds no watches and opens
no API connection.

## The plain caching proxy

`configmap.yaml` is `examples/conf/example.full.yaml` wrapped in a
ConfigMap, generated by `make kube-configmap` and checked in CI, so the two
never drift; edit the example, not the ConfigMap. `deployment.yaml` mounts it
and `service.yaml` exposes the proxy and metrics ports.
`graphite-deployment-patch.yaml` and `mysql-deployment-patch.yaml` layer an
in-cluster Graphite and a credential-bearing MySQL configuration on top of
it; each file describes how it is applied.
