# Kubernetes Autodiscovery Integration Scenario

Runs `TestALBDiscoveryKind` (autodiscovery plan step 34): an in-cluster
Trickster whose ALB pool is discovered from a Service's EndpointSlices,
exercised with scale up/down, a rolling restart under load (zero client
errors), and an API-server outage (last-good pool behavior).

Requires `kind`, `kubectl`, and `docker` on the host. From the repo root:

```sh
make kind-integration-start   # create cluster, build+load image, deploy
cd integration
TRICKSTER_KIND_TEST=1 go test -run TestALBDiscoveryKind -v .
cd ..
make kind-integration-stop    # delete the cluster
```

The `integration-kind` CI job runs the same flow. The cluster maps the
in-cluster Trickster's NodePorts to host ports 30080 (front) and 30081
(metrics) via kind extraPortMappings.
