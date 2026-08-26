# Kubernetes RBAC for ALB Autodiscovery

Trickster's `kubernetes` autodiscovery provider watches cluster resources
with **list** and **watch** only — it never reads Secrets or ConfigMaps and
never writes to the API. Grant exactly the resources your configured query
kinds use, and nothing else:

| query `kind`     | apiGroup            | resource         | verbs       |
|------------------|---------------------|------------------|-------------|
| `endpointslices` | `discovery.k8s.io`  | `endpointslices` | list, watch |
| `service`        | `""` (core)         | `services`       | list, watch |
| `pods`           | `""` (core)         | `pods`           | list, watch |

Discovery queries are namespace-scoped (the `namespace` field of each
`alb.discovery.query`, defaulting to the pod's own namespace in-cluster), so
a namespaced `Role`/`RoleBinding` per watched namespace is sufficient and
preferred. Use a `ClusterRole` with a `ClusterRoleBinding` only when many
namespaces are watched and per-namespace bindings are impractical.

## Example: endpointslices discovery in one namespace

The minimal grant for the default query kind (`endpointslices` of a named
Service), watching the `monitoring` namespace:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: trickster
  namespace: trickster
---
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

## Example: all three query kinds

Add rules only for the kinds you actually configure:

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
  - apiGroups: [""]
    resources: ["services", "pods"]
    verbs: ["list", "watch"]
```

## Notes

- The provider's informers use server-side label/field selectors, but RBAC
  cannot scope reads below the resource level; the grant covers all objects
  of that resource in the bound namespace.
- Out-of-cluster (kubeconfig-based) discoverers need the same permissions
  for the kubeconfig's user or service account.
- No `get` verb is required: shared informers operate entirely on
  list+watch.
