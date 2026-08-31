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

Support for additional providers (consul, ec2, gcp, etcd, docker) is
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
| `http_sd` | `http` + `http_sd` | connection settings in the shared `http` block (below); `http_sd.format` selects the member-list document: `trickster` (default) or `prometheus` |
| `consul` | `http` + `consul` | connection settings in the shared `http` block; `consul.datacenter`, `namespace`, `partition`, `wait`, `allow_stale`, `only_passing`, `warning_is_ready` |
| `nomad` | `http` + `nomad` | connection settings in the shared `http` block; `nomad.namespace`, `region`, `wait`, `allow_stale` |
| `aws` | `aws` (+ optional `http`) | `aws.service` (**required**: `ec2` or `ecs`), `region`, and the credential fields; the endpoint is derived, so `http.endpoint` is an optional override |
| `gcp` | `gcp` (+ optional `http`) | `gcp.service` (required; `gce`), `gcp.project` (from the metadata server when unset) and `credentials_file`; the endpoint is the Compute API, so `http.endpoint` is an optional override |

A block is only valid on its own provider's entries; anything else fails
startup.

#### The shared `http` block

Providers that discover members by polling an HTTP endpoint share one
connection block rather than each defining its own, so that configuring a
second such provider does not mean learning a second vocabulary. It is
required by `http_sd`, and rejected on providers that do not poll HTTP.

| option | meaning |
| ----- | ----- |
| `endpoint` | base URL of the service to poll (`http`/`https`, host required) |
| `interval` | poll cadence (default 30s, min 1s) |
| `timeout` | bound on a single poll (default 10s, min 100ms) |
| `tls` | outbound client TLS: `client_cert_path`/`client_key_path`, `certificate_authority_paths`, `insecure_skip_verify` |
| `headers` | headers set on every request; where a registry's credential is a bespoke header (`X-Consul-Token`, `X-Nomad-Token`), it goes here |
| `username`, `password` | static HTTP Basic credential |
| `bearer_token` | sent as `Authorization: Bearer <token>` |
| `bearer_token_file` | path re-read before each poll, so a rotated credential is picked up without a restart — prefer this over `bearer_token` for anything that expires |
| `follow_redirects` | default false; a redirect away from the configured endpoint is surfaced rather than chased |

`username`/`password` and the bearer-token fields are mutually exclusive,
as are the two bearer-token forms: a config that sets both fails startup
instead of silently preferring one, which is how an operator ends up
debugging a 401 against a config that looks correct.

Providers whose normal poll includes a server-side wait (blocking queries)
need `timeout` comfortably above that wait. There is no second,
client-level timeout underneath it that could cut a long poll short.

### The `http_sd` Provider

`http_sd` fetches a member list from an HTTP endpoint. It is the universal
adapter: any service-discovery system Trickster has no in-tree provider for
can feed it through a few lines of glue that serve a member list, with no
Trickster change and no restart.

```yaml
discovery:
  fleet:
    provider: http_sd
    http:
      endpoint: https://sd.example.com
      interval: 15s
      bearer_token_file: /var/run/secrets/sd-token
    http_sd:
      format: trickster

backends:
  prom-template:
    provider: prometheus
    is_template: true
  prom-alb:
    provider: alb
    alb:
      mechanism: tsm
      discovery:
        discoverer_name: fleet
        template_backend: prom-template
        query:
          path: /pools/prometheus
```

The query's `path` is optional and is appended to the endpoint, so one
server can serve a different member list per ALB while they share the
discoverer's connection settings. The query's `scheme` supplies the scheme
for `prometheus`-format targets, which are bare `host:port`; native-format
entries carry their own and override it.

**Formats.** `trickster` is the same document the `file` provider reads and
is the default:

```yaml
- name: prom-1
  scheme: https
  address: 10.0.0.1:9090
  path_prefix: /base
  weight: 2
  replica_group: shard-0
```

`prometheus` is the document Prometheus's own `file_sd` and `http_sd`
consume, so an existing endpoint can be pointed at Trickster unchanged:

```json
[{"targets": ["10.0.0.1:9090"], "labels": {"env": "prod"}}]
```

A group's `__scheme__` label overrides the query scheme, letting one
endpoint serve mixed-scheme members. Note that the Prometheus format cannot
express **weight** or **replica_group** — deployments that need weighted
pools or TSM replica groups want the native format.

The format is named explicitly rather than sniffed. The two documents are
structurally distinguishable, but guessing means a typo in one format can
parse as a valid, wrong membership in the other, and the cost of guessing
wrong is a silently drained pool.

**Efficiency.** Requests carry `X-Prometheus-Refresh-Interval-Seconds`, as
Prometheus's does, so servers that generate member lists on demand can pace
their own work. `ETag` is honored: an unchanged membership costs one
conditional request and a `304`, with no re-parse and no snapshot. The
validator is only stored after a document parses, so a rejected document
can never be confirmed as current by a later `304`.

**Failure.** A transport error, an unexpected status, an oversized body
(the limit is 16 MiB), or a document that will not parse all keep the
last-good membership and are logged once per failure streak and counted on
`trickster_discovery_refresh_errors_total`. An endpoint that
*authoritatively* returns no members (`[]`) is a valid membership and is
applied, so a scaled-to-zero pool can be reported as such.

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

### The `consul` Provider

`consul` reads service instances from Consul's health endpoint. It is
**event-driven rather than polled**: each request is a Consul blocking
query, so the server parks it until the service changes or `wait` elapses.
A membership change is observed within a round trip instead of within a
poll interval, and a stable service costs one parked connection rather
than a request per interval.

```yaml
discovery:
  consul-dc1:
    provider: consul
    http:
      endpoint: http://127.0.0.1:8500
      # a rotated ACL token; Consul accepts the Authorization Bearer scheme
      # as an equivalent to its own X-Consul-Token header
      bearer_token_file: /var/run/secrets/consul-token
    consul:
      datacenter: dc1
      wait: 5m          # how long a blocking query parks; 1s–10m
      allow_stale: true # answer from any server, not only the leader

backends:
  prom-template:
    provider: prometheus
    is_template: true
  prom-alb:
    provider: alb
    alb:
      mechanism: tsm
      health_mode: provider   # Consul's own checks decide readiness
      discovery:
        discoverer_name: consul-dc1
        template_backend: prom-template
        query:
          service: prometheus
          tags: [production]
          # filter: 'Service.Meta.version == "2"'
          # replica_group_label: shard   # read from the service's Meta
```

**Readiness.** Consul reports per-instance check status, so this is the
first provider outside kubernetes that can honestly answer "is this member
ready", which makes `health_mode: provider` meaningful for VM and container
fleets. An instance's readiness is its **worst** check: all passing is
ready, `critical` and `maintenance` are not ready, and `warning` is ready by
default (matching how Consul treats warning for DNS) — set
`warning_is_ready: false` to drain warning instances instead. A status a
future Consul release introduces is treated as not-ready rather than
ignored.

Failing instances are reported as `NotReady` rather than omitted, so that an
ALB using the default `health_mode: probe` can decide for itself and a
wholly-unhealthy service does not look like an empty one. Set
`only_passing: true` to have Consul filter them out server-side instead.

**Weights.** Consul's own `Weights.Passing` / `Weights.Warning` map onto
member weights, including the passing/warning distinction — an operator who
has already told Consul the relative capacity of each instance does not have
to tell Trickster again.

**Addresses.** A service that registers its own address overrides its node's,
which is how sidecars and containers with their own routable address are
represented. An instance with no usable address or port fails the whole
refresh rather than being silently dropped, so a pool never quietly shrinks
because of a catalog change nobody noticed.

**Labels.** Members carry `service`, `service_id`, `node`, `datacenter`,
`status`, and `tags` (comma-bracketed, as `,a,b,`). Service metadata is
carried as `meta_<key>` so that an operator-defined key cannot shadow a
Trickster-assigned label.

**Timeouts.** `http.timeout` must outlast `consul.wait`, because a blocking
query legitimately takes that long. Its default is derived from the wait
rather than shared with the other HTTP providers (Consul adds up to
`wait/16` of its own jitter, which the margin covers), and a config that
sets it too low is rejected at startup rather than producing a stream of
timeouts. `http.interval` is not the poll cadence here — with blocking
queries there is no cadence — it is the retry delay after a failure.

### The `nomad` Provider

`nomad` reads service instances from Nomad's **native** service registry
(Nomad 1.3+). Like `consul` it is event-driven, using the same HashiCorp
blocking-query protocol, so a membership change is observed within a round
trip rather than within a poll interval.

```yaml
discovery:
  nomad-eu:
    provider: nomad
    http:
      endpoint: http://127.0.0.1:4646
      # a rotated ACL token; Nomad accepts the Authorization Bearer scheme
      # as an equivalent to its own X-Nomad-Token header
      bearer_token_file: /var/run/secrets/nomad-token
    nomad:
      namespace: default
      region: eu-1
      wait: 5m
      allow_stale: true

backends:
  prom-alb:
    provider: alb
    alb:
      discovery:
        discoverer_name: nomad-eu
        template_backend: prom-template
        query:
          service: prometheus
          tags: [production]
          # filter: 'JobID == "monitoring"'
```

**Native registry, not Consul.** This reads the registry a job selects with
`provider = "nomad"` in its `service` block. Jobs that register into Consul
instead are discovered with the **`consul`** provider, and that is the more
capable choice where it applies: Nomad's service endpoint carries no
per-instance check state, so members are reported `ReadyUnknown` and
`health_mode: provider` falls back to Trickster's own probes. A deployment
that wants discovery-conveyed readiness should register its services into
Consul.

**Tags filter client-side.** Unlike Consul's catalog endpoint, Nomad's
service endpoint has no `tag` parameter, so `query.tags` is applied by
Trickster after the response arrives. It is a conjunction — every listed tag
must be present. `query.filter` is passed through to Nomad and evaluated
server-side.

**Labels.** Members carry `service`, `service_id`, `job_id`, `alloc_id`,
`node_id`, `namespace`, `datacenter`, and `tags` (comma-bracketed). The
allocation and job identifiers are what an operator needs to trace a member
back to the workload that registered it.

**Timeouts** work exactly as for `consul`: `http.timeout` must outlast
`nomad.wait`, its default is derived from the wait, and `http.interval` is
the retry delay after a failure rather than a poll cadence.

### The `aws` Provider

`aws` discovers members from an AWS API, selected by `aws.service`: `ec2`
for instances, `ecs` for tasks. It is **required** — with more than one AWS
API supported, defaulting would be an arbitrary guess at which one you
meant, so a config that omits it fails at startup. Further AWS sources
arrive as new `service` values rather than new providers, inheriting this
provider's credentials, signing, pagination and options.

```yaml
discovery:
  fleet:
    provider: aws
    aws:
      service: ec2      # required: ec2 or ecs
      region: us-east-1
      # credentials omitted: use the standard chain (IRSA, instance
      # profile, environment, shared config). See docs/aws.md.
    http:
      interval: 60s   # instance inventories change slowly; poll gently

backends:
  prom-alb:
    provider: alb
    alb:
      discovery:
        discoverer_name: fleet
        template_backend: prom-template
        query:
          filters:
            tag:service: [prometheus]
            instance-state-name: [running]
          port_label: trickster-port   # read the port from an EC2 tag
          address_type: private        # private (default), public, or ipv6
          # port: 9090                 # or a static port for every member
          # replica_group_label: shard
```

Credentials, region resolution and IAM are documented once in
[AWS Integration](./aws.md). The IAM principal needs
**`ec2:DescribeInstances`**.

**Hosts, not endpoints.** An instance inventory returns hosts with several
addresses and no port, so two query fields exist that the registry providers
do not need:

- **`address_type`** — `private` (default), `public`, or `ipv6` — selects
  which of the instance's addresses becomes the member address.
- **`port_label`** — names an EC2 tag whose value is the member's port.
  `port` supplies a static one. At least one is required, and they compose:
  where both are set, the tag wins per instance and `port` is the fallback,
  which is what makes `port_label` safe to adopt incrementally across a
  fleet.

**Selection.** `filters` is passed to EC2 as `Filter.N`, evaluated
server-side — use `tag:<key>` to filter on a tag value, and any other
[DescribeInstances filter](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeInstances.html)
name. `tags` additionally requires the *presence* of tag keys, applied after
the response arrives.

**Instance state.** `running` is `Ready` and `pending` is `NotReady`.
Instances that are `shutting-down`, `stopping`, `stopped` or `terminated`
are **omitted entirely** rather than reported unready, so they drain from
pools before they stop answering — the same rule the kubernetes provider
applies to terminating endpoints. A state a future EC2 release introduces is
treated as not-ready rather than assumed healthy.

**Instances that cannot become members are excluded, not fatal.** An
instance with no `port_label` tag and no static `port`, or with no address
of the requested type, is skipped and logged (once, until the set of
excluded instances changes) rather than failing the refresh. This differs
deliberately from the `consul` and `nomad` providers, where a malformed
entry fails the whole refresh: a service registry contains only instances of
the service, so a bad entry means the API is broken, while an EC2 inventory
routinely contains hosts that simply are not tagged yet. Failing there would
drain a working pool because of one unrelated instance.

**Labels.** Members carry `instance_id`, `instance_type`, `instance_state`,
`image_id`, `availability_zone`, `vpc_id`, `subnet_id`, `architecture`,
`private_ip`, `public_ip`, `private_dns` and `public_dns`. Instance tags are
carried as `tag_<Key>`, prefixed so an operator-defined tag cannot shadow a
Trickster-assigned label. The `Name` tag becomes the member name, falling
back to the instance id.

**Endpoint.** Derived from the region and service
(`https://ec2.<region>.amazonaws.com`). Set `http.endpoint` to override it
for a VPC endpoint, a FIPS endpoint, or a test double; a region is required
when it is not overridden, because the endpoint is built from it.

#### `service: ecs`

Discovers ECS tasks, **including Fargate**, which `service: ec2`
structurally cannot see — a Fargate task has no EC2 instance behind it.

```yaml
discovery:
  tasks:
    provider: aws
    aws:
      service: ecs
      region: us-east-1
    http:
      interval: 30s

backends:
  prom-alb:
    provider: alb
    alb:
      discovery:
        discoverer_name: tasks
        template_backend: prom-template
        query:
          cluster: prod           # ECS cluster; the account default when unset
          service: prometheus     # ECS service name; optional
          port_label: trickster-port
          # port: 9090            # or a static port for every task
```

The IAM principal needs **`ecs:ListTasks`** and **`ecs:DescribeTasks`**.

**awsvpc only.** Each task in `awsvpc` mode has its own elastic network
interface, so `DescribeTasks` alone yields a routable address. That is the
only mode Fargate offers and the default for new EC2-launch-type services.
Under **bridge or host networking the address belongs to the container
instance rather than the task**, and resolving it would take two further API
calls, a second signing service and broader IAM — so such tasks are
**excluded with a reason saying so** rather than silently missing. If you
need bridge or host mode, say so and it can be added.

**Tags must be propagated.** `port_label` reads an ECS **task** tag, and ECS
does not copy service tags onto tasks unless the service is created with
`--propagate-tags SERVICE`. Trickster asks `DescribeTasks` for tags
explicitly (`include: [TAGS]`); if `port_label` finds nothing, check the
propagation setting first.

**Task state.** `RUNNING` is `Ready`; `PROVISIONING`, `PENDING` and
`ACTIVATING` are `NotReady`; `DEACTIVATING`, `STOPPING`, `DEPROVISIONING`
and `STOPPED` are **omitted entirely**, so tasks drain from pools before they
stop answering. Container health is only reported when the task definition
declares a health check — `UNHEALTHY` is `NotReady`, and `UNKNOWN` or absent
is treated as ready, because the alternative would make every
un-instrumented task permanently unusable.

**Selection** is by `cluster` and `service`; `filters` and `address_type` are
rejected for `ecs`, since ECS selects by cluster rather than by instance
attribute and an awsvpc task has exactly one address. `tags` still filters on
tag presence after the response arrives.

**Labels.** Members carry `task_arn`, `task_id`, `cluster`,
`task_definition`, `group`, `launch_type`, `availability_zone`,
`task_status` and `health_status`, plus task tags as `tag_<Key>`. The `Name`
tag becomes the member name, falling back to the task id.

**Churn.** A task that disappears between `ListTasks` and `DescribeTasks` is
ordinary churn; it is reported as an exclusion rather than silently
shrinking the pool.

### The `gcp` Provider

`gcp` reads a Google Cloud API named by **`gcp.service`**. The provider is
named for the cloud rather than for Compute Engine, matching `aws` — Google
Cloud APIs outside Compute Engine belong here too, and would sit oddly under
a provider called `gce`.

`service` is **required** even though `gce` is the only value today. A
default added now could never be removed, and every service added later
would then be reached by opting out of a value the operator never chose.

A value names the **product** an operator would recognize, not the API that
serves it — the same convention as `aws.service`. That matters here: Cloud
Load Balancing is served by the Compute API alongside instances, so naming
these for the API would collide where naming them for the product does not.

#### `service: gce`

Discovers Google Compute Engine instances through the Compute API's
`instances.aggregatedList`, which covers **every zone in the project** in
one paged call — so no zone list has to be configured or kept current.

```yaml
discovery:
  fleet:
    provider: gcp
    gcp:
      service: gce               # required
      project: my-project        # from the metadata server when unset
      # credentials_file: /etc/trickster/sa.json   # ADC when unset
    http:
      interval: 60s              # instance inventories change slowly

backends:
  prom-alb:
    provider: alb
    alb:
      discovery:
        discoverer_name: fleet
        template_backend: prom-template
        query:
          filter: 'labels.role = "prometheus" AND status = "RUNNING"'
          tags: [http-server]     # network tags, matched on presence
          port_label: port        # instance label, or metadata key
          address_type: private   # private (default), public, or ipv6
          # port: 9090            # or a static port for every member
          # replica_group_label: shard
```

**Credentials.** Leaving `credentials_file` empty selects **Application
Default Credentials**: `GOOGLE_APPLICATION_CREDENTIALS`, gcloud user
credentials, **Workload Identity on GKE**, or the instance metadata server
on GCE. Prefer those over a key file wherever the platform offers one.
Credentials resolve lazily and only successes are cached, so Trickster
starts even when the metadata server is briefly unreachable and a momentary
failure does not permanently disable discovery.

`credentials_file` must be a **service account** key. The credential type is
required rather than taken from the file: an `external_account` or
`impersonated_service_account` configuration can name an arbitrary token URL
or local executable, so accepting whichever type a file happens to declare
would hand credential resolution somewhere unintended. For user credentials,
use ADC instead of this field.

The IAM principal needs **`compute.instances.list`** on the project — the
`roles/compute.viewer` role includes it. The OAuth scope requested is
`compute.readonly`; Trickster never mutates a project.

**Project.** Taken from `gcp.project`, then from the credentials, then from
the metadata server when Trickster runs on GCE. It is deliberately not
required in config, because reading it from the metadata server is the
idiomatic deployment.

**Hosts, not endpoints**, exactly as for `aws` `service: ec2`:
`address_type` chooses the address and `port`/`port_label` supplies the
port, with the label winning per instance and the static port as fallback.
**`port_label` reads an instance label first, then instance metadata** —
both are key/value namespaces on a GCE instance, and a deployment already
carrying the value in metadata does not have to move it.

**Instance status.** `RUNNING` is `Ready`; `PROVISIONING`, `STAGING` and
`REPAIRING` are `NotReady`; `STOPPING`, `SUSPENDING`, `SUSPENDED` and
`TERMINATED` are **omitted entirely**, so instances drain from pools before
they stop answering. A status a future Compute Engine release introduces is
treated as not-ready rather than assumed healthy.

**Selection.** `filter` is a GCE filter expression, evaluated server-side.
`tags` matches GCE **network tags**, which are names without values, so it
filters on presence. Requests set `returnPartialSuccess`, so one unreachable
zone contributes no instances rather than failing the whole refresh.

**Labels.** Members carry `instance_id`, `instance_name`, `status`, `zone`,
`machine_type`, `network`, `subnetwork`, `private_ip`, `public_ip`, and
`tags` (comma-bracketed). Instance labels are carried as `label_<key>`,
prefixed so a user-defined label cannot shadow a Trickster-assigned one.
Resource URLs are shortened to their last segment, since a member label full
of `https://www.googleapis.com/compute/v1/...` is unreadable.
