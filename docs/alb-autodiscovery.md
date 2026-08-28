# ALB Autodiscovery

Autodiscovery keeps an [ALB](./alb.md) pool's membership current at runtime:
instead of hand-maintaining a `pool` list, Trickster watches a discovery
source and adds, updates, and drains pool members as the source changes —
with no config reload or listener restart. Discovered members are cloned
from a designated template backend, so they inherit its caching, TLS,
health check, path, and timeout configuration.

## Supported Discovery Providers

There are several discovery providers supported by Trickster

* Kubernetes (endpointslices, services, or pods, via the Kubernetes API)
* DNS SRV records (`dns_srv`)
* DNS A/AAAA records (`dns_a`)
* Watched member-list file (`file`)

Support for additional providers (consul, ec2, gce, etcd, docker) is
planned; until each lands, its users are generally served by the `file`
provider (any external service-discovery tool can emit the member list) or
by the DNS providers (e.g., Consul's DNS interface, cloud private DNS
zones, Docker's embedded DNS). Developers can add providers against a
simple interface; see the
[provider authoring guide](./developer/discovery-providers.md).

## Configuration Overview

Autodiscovery has three configuration parts:

1. a named **discoverer** in the top-level `discovery` section — the
   provider and its connection-level settings (Kubernetes client, DNS
   resolver). Multiple ALBs can share one discoverer, and therefore one
   client/watch/resolver stack.
2. a **template backend** — a normal backend definition marked
   `is_template: true`. Templates are never routed, never eligible for
   `is_default`, never static pool members, and don't require an
   `origin_url`; they exist to be cloned per discovered member.
3. the ALB's **`alb.discovery`** block — which discoverer to use, a
   provider-specific `query` describing what to select, the template to
   clone, and guardrail options.

```yaml
discovery:
  in-cluster:
    provider: kubernetes
    kubernetes:
      in_cluster: true

backends:
  prom-template:
    provider: prometheus
    is_template: true
    cache_name: default
    healthcheck:
      interval: 5s

  prom-alb:
    provider: alb
    alb:
      mechanism: tsmerge
      discovery:
        discoverer_name: in-cluster
        template_backend: prom-template
        query:
          kind: endpointslices
          namespace: monitoring
          service: prometheus
          port: web
```

A discovery-backed ALB may also list static `pool` members; discovered
members are additive to them. Static pool entries (and discovered members
from sources that convey weights) support integer weights for the
`round_robin` mechanism:

```yaml
pool:
  - static-member          # weight 1
  - name: bigger-member
    weight: 3
```

### Discoverer Connection Options

Each entry in the top-level `discovery` section declares a `provider` and
that provider's connection-level block:

| provider | block | options |
| ----- | ----- | ----- |
| `kubernetes` | `kubernetes` | `in_cluster` (default true) or `kubeconfig` path — mutually exclusive |
| `dns_srv`, `dns_a` | `dns` | `resolver` (host:port; default: system resolver), `interval` (poll cadence, default 30s, min 1s; record TTLs act as a floor) |
| `file` | `file` | `poll_interval` (stat-poll fallback cadence, default 30s, min 1s) |

A block is only valid on its own provider's entries; anything else fails
startup.

### Template Backends

The `template_backend` is an ordinary backend definition marked
`is_template: true`. A discovered member overrides exactly the template's
**name**, **origin_url** (and its derived scheme/host/path-prefix, with
the member's path prefix falling back to the template's), and **replica
group** (see below); the member's weight applies to its pool entry. Every
other option — cache settings, TLS client configuration, healthcheck,
paths, request rewriters, authenticator, timeouts, concurrency limits,
tracing — is inherited from the template.

Templates are excluded from everything that applies only to live
backends: they are never routed, cannot be `is_default`, cannot appear in
a static ALB pool, and do not require an `origin_url`. A template must
use an origin-serving provider (not `alb` or `rule`), and a TSM-merged or
`replica_group_label`-using ALB requires a time-series-merge-capable
template provider.

### The `alb.discovery` Block

| option | description | default |
| ----- | ----- | ----- |
| `discoverer_name` | name of a discoverer in the top-level `discovery` section (required) | |
| `template_backend` | name of a backend with `is_template: true` (required) | |
| `query` | provider-specific selection (required; see each provider below) | |
| `min_members` | reject snapshots that would shrink the discovered membership below this count, keeping the last-good pool (guards against source blips returning empty results). 0 disables. | `0` |
| `debounce_window` | coalesce membership changes arriving within this window into one pool update, damping flapping sources. 0 disables. | `0` |
| `startup_policy` | `retry` starts the ALB with only its static members and keeps retrying the discoverer; `fail` fails startup when the discoverer is unavailable | `retry` |
| `health_mode` | `probe`: discovered members inherit the template's active health check. `provider`: the discoverer's readiness reporting (currently Kubernetes only) drives member health instead of active probes | `probe` |

### Health and Readiness Semantics

In `probe` mode, each discovered member gets its own health check from the
template's `healthcheck` config, and the ALB's `healthy_floor` applies
exactly as it does to static members. If the template has no probe
interval and the floor requires passing probes, the floor is reset to 0
with a loud warning (as with static members).

In `provider` mode, readiness reported by the discoverer maps onto member
health: ready members enter as passing (1), not-ready and terminating
members as failing (-1), readiness-unknown members as unchecked (0) — so a
`healthy_floor` of 1 excludes members until the provider reports them
ready.

Members a provider reports as shutting down (terminating Kubernetes
endpoints, deletion-stamped pods) are removed from the snapshot entirely,
so they drain from the pool *before* the workload is killed — enabling
zero-error rolling deploys. On removal, a member's in-flight requests
complete, its health check stops, its metrics series are deleted, and its
idle upstream connections close after the configured drain timeout.

### TSM Replica Groups

When the ALB uses the [Time Series Merge](./alb.md#time-series-merge)
mechanism, discovered members participate in `replica_group` semantics
three ways, in priority order:

1. **Per-member groups from the discovery source** (highest priority): the
   Kubernetes provider can read a configured label from each discovered
   workload via `query.replica_group_label`, and `file` provider entries
   may carry a `replica_group` field. Members sharing a group value are
   treated as HA replicas of one logical shard and coalesced; distinct
   values are distinct shards, merged. This handles sharded-HA topologies
   (e.g., Thanos-style Prometheus pairs) discovered with a single
   selector:

   ```yaml
   backends:
     prom-alb:
       provider: alb
       alb:
         mechanism: tsmerge
         discovery:
           discoverer_name: in-cluster
           template_backend: prom-template
           query:
             kind: endpointslices
             namespace: monitoring
             service: prometheus
             port: web
             replica_group_label: prometheus/shard
   ```

2. **A `replica_group` set on the template backend**: inherited by every
   member without a source-conveyed group — all such members are HA
   replicas of one shard (the common HA-pair case).
3. **Neither** (default): each member is its own replica group, i.e. its
   own logical shard — a pure federation/merge of every member.

For the `pods` and `service` kinds the label is read from the discovered
Pod or Service itself. For `endpointslices`, endpoints don't carry pod
labels, so setting `replica_group_label` joins a Pod watch in the query's
namespace to resolve each endpoint's target pod (see the RBAC note below);
an endpoint whose pod isn't yet observed joins ungrouped and regroups
automatically when the pod appears. A member whose group changes is
rebuilt in place under the same name.

Because `replica_group` is only meaningful on time-series backends,
configuration validation requires a TSM-capable `template_backend`
whenever the ALB mechanism is `tsmerge` or `replica_group_label` is set.
The DNS providers convey no grouping metadata; use the template-level
group (mode 2) or the `file` provider for grouped non-Kubernetes pools.

## Kubernetes

The `kubernetes` provider watches the cluster via the API server (shared
informers; watch-driven, no polling) and supports three query kinds.

Connection options:

```yaml
discovery:
  my-cluster:
    provider: kubernetes
    kubernetes:
      in_cluster: true              # use the pod's service account (default)
      # kubeconfig: /path/to/kubeconfig   # or run against a remote cluster
```

`in_cluster` and `kubeconfig` are mutually exclusive; with neither set,
in-cluster is assumed.

### Query Kinds

**`endpointslices`** (default) discovers the ready endpoint addresses of a
named Service — the pod IPs behind it — and is the right choice for
routing around the Service's own load balancing:

```yaml
query:
  kind: endpointslices
  namespace: monitoring
  service: prometheus
  port: web          # port name or number; optional when the service has one port
```

**`service`** discovers the ClusterIP of each Service matching a label
selector (or one named Service), one member per Service:

```yaml
query:
  kind: service
  namespace: monitoring
  selector:
    app: prometheus
  port: 9090
```

**`pods`** discovers pod IPs directly by label selector, without requiring
a Service (the original ask of issue #609):

```yaml
query:
  kind: pods
  namespace: monitoring
  selector:
    app: prometheus
  port: web
```

`namespace` defaults to the pod's own namespace when running in-cluster.

### Port and Scheme Resolution

`port` may be a named port, a number, or omitted when the target declares
exactly one port; ambiguity is logged and the object skipped. The member
scheme comes from `query.scheme` if set; otherwise a declared
`appProtocol: https` on the selected port, or a `trickster.io/scheme`
annotation on the watched Service/Pod, selects `https`; the default is
`http`.

### Zero-Error Rolling Deploys

Terminating endpoints are removed from the discovered membership as soon
as the change is observed, so members drain out ahead of pod deletion.
One piece belongs to the workload, though: Kubernetes marks an endpoint
`terminating` and signals the container at the same moment, so — as with
any EndpointSlice consumer, kube-proxy included — the pod must keep
serving briefly while its removal propagates. Give discovered workloads a
short `preStop` delay (a few seconds comfortably covers Trickster's
sub-second pickup):

```yaml
lifecycle:
  preStop:
    sleep:         # native sleep action (Kubernetes >= 1.30); images with
      seconds: 5   # a shell can use exec with command: ["sleep", "5"]
```

Without it, an instantly-exiting container can produce a brief window of
connection errors during rollouts — regardless of what is consuming the
EndpointSlices.

### RBAC

The provider needs only **list** and **watch** on the resources your query
kinds use — it never reads Secrets or ConfigMaps and never writes:

| query `kind` | apiGroup | resource | verbs |
| ----- | ----- | ----- | ----- |
| `endpointslices` | `discovery.k8s.io` | `endpointslices` | list, watch |
| `service` | `""` (core) | `services` | list, watch |
| `pods` | `""` (core) | `pods` | list, watch |

Queries are namespace-scoped, so a namespaced `Role`/`RoleBinding` per
watched namespace is sufficient and preferred:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: trickster-autodiscovery
  namespace: monitoring
rules:
  - apiGroups: ["discovery.k8s.io"]
    resources: ["endpointslices"]
    verbs: ["list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: trickster-autodiscovery
  namespace: monitoring
subjects:
  - kind: ServiceAccount
    name: trickster
    namespace: trickster
roleRef:
  kind: Role
  name: trickster-autodiscovery
  apiGroup: rbac.authorization.k8s.io
```

Add `services` and/or `pods` rules only for the kinds you configure. Note
that using `replica_group_label` with the `endpointslices` kind joins a
Pod watch, so it additionally requires `pods` list/watch in the query's
namespace. RBAC cannot scope below the resource level, so the grant covers
all objects of that resource in the bound namespace. Out-of-cluster
(kubeconfig-based) discoverers need the same permissions for their user or
service account.

## DNS SRV

The `dns_srv` provider polls SRV records. SRV target and port map to the
member address; SRV weight maps to the member's load-balancing weight; and
only the highest-priority tier (the lowest `priority` value present in the
answer) becomes members — lower tiers are treated as standby capacity
managed on the DNS side.

```yaml
discovery:
  corp-dns:
    provider: dns_srv
    dns:
      resolver: 10.0.0.53:53   # optional; default: the system resolver
      interval: 30s            # poll cadence (default 30s)

backends:
  my-alb:
    provider: alb
    alb:
      mechanism: rr
      discovery:
        discoverer_name: corp-dns
        template_backend: my-template
        query:
          srv_name: _prometheus._tcp.example.com
          # scheme: https      # optional; default http
```

Record TTLs act as a floor on the poll cadence: an answer is never
re-resolved before its shortest TTL expires, so `interval` is the *most
frequent* the provider will query. Resolution failures keep the last-good
membership; an authoritative empty answer empties it.

## DNS A/AAAA

The `dns_a` provider resolves a hostname's A and AAAA records, with a
fixed port and scheme from the query. This covers round-robin DNS,
headless-service-style DNS outside Kubernetes, Docker's embedded DNS, and
Consul's DNS interface without a bespoke provider:

```yaml
query:
  hostname: prometheus.service.consul
  port: 9090           # required
  # scheme: https      # optional; default http
```

The `dns` connection options, poll cadence, TTL floor, and failure
semantics are the same as `dns_srv`.

## File

Note: like Prometheus's `file_sd`, the `file` provider is the universal
integration point — anything that can write a file (cron, sidecar,
consul-template, your deploy tooling) can drive an ALB pool.

The provider watches a local YAML or JSON member-list file and applies it
atomically on change:

```yaml
discovery:
  external-sd:
    provider: file
    # file:
    #   poll_interval: 30s   # stat-poll fallback cadence (default 30s)

backends:
  my-alb:
    provider: alb
    alb:
      mechanism: rr
      discovery:
        discoverer_name: external-sd
        template_backend: my-template
        query:
          path: /etc/trickster/members.yaml
```

The file is a list of members:

```yaml
- name: prom-1            # optional; defaults to the address
  address: 10.0.0.1:9090  # required host:port
- name: prom-2
  scheme: https           # optional; default http
  address: 10.0.0.2:9090
  path_prefix: /base      # optional
  weight: 3               # optional; default 1
  replica_group: shard-0  # optional; TSM replica group (see above)
```

Writers should replace the file atomically (write to a temp file in the
same directory, then rename). A file that fails to read or parse keeps the
last-good membership; an empty file is a valid, empty membership.

### Change Detection

The provider uses two mechanisms together (via Trickster's shared
filesystem watcher), so updates are never missed:

1. **Filesystem notification** (on the file's parent directory,
   debounced): near-instant pickup of writes, atomic renames, and symlink
   swaps on local filesystems; a directory watch dropped by deletion or
   recreation is automatically re-armed.
2. **A content-comparing poll** of the file (`file.poll_interval`,
   default `30s`, minimum `1s`): the guaranteed fallback wherever
   notification is unreliable or unavailable.

Guidance for Kubernetes-mounted member files:

- **ConfigMap / Secret volume mounts**: kubelet applies updates with an
  atomic symlink swap inside the mount directory, which generates inotify
  events — notification works, and updates are picked up promptly. Note
  that kubelet itself syncs ConfigMap content periodically (typically up
  to a minute), which usually dominates end-to-end latency.
- **`subPath` mounts of a ConfigMap/Secret**: Kubernetes never updates
  these after pod start — no mechanism (notification or polling) can see
  a change. Mount the directory, not a `subPath`.
- **Network- or FUSE-backed volumes** (NFS PVs, some CSI drivers):
  inotify generally does not observe writes made by other clients, so the
  stat poll is the effective update mechanism — set
  `file.poll_interval` to the freshness your deployment needs.
- **`emptyDir`/`hostPath` written by a sidecar in the same pod**:
  notification works.

The poll compares file content (not timestamps), so any change is
detected within one `poll_interval` regardless of filesystem timestamp
granularity.

## Observing Discovered Members

Discovered members appear on the [health status page](./health.md)
alongside static pool members, under their generated backend names
(`<alb-name>-<member-name>`), tagged with their provider, owning ALB, and
discoverer (e.g., `prometheus (prom-alb via in-cluster)`).

Membership additions and removals are logged at info with the member name
and origin (credentials embedded in origin URLs are masked), and the full
membership is logged at debug on every change; all autodiscovery log
events carry `scope=discovery` for easy filtering. Per-ALB member-count,
member-change, snapshot-result, and refresh-staleness metrics — and
per-discoverer refresh-error counters — are documented in
[metrics.md](./metrics.md). When the ALB has a `tracing_name` configured,
each membership reconcile cycle is traced as an `alb.discovery.reconcile`
span via the standard [tracing](./tracing.md) subsystem; the request hot
path is never traced by discovery.
