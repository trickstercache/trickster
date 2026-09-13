# Gateway API Conformance

This module runs the upstream [Gateway API conformance suite](https://gateway-api.sigs.k8s.io/concepts/conformance/)
against a Trickster Gateway API controller. It is a module of its own because
the suite carries controller-runtime and its own Kubernetes client versions,
which the main module and the integration suite do not need.

`conformance_test.go` declares the extended features the controller supports
beyond the `GATEWAY-HTTP` core, identifies the implementation for the report,
and otherwise defers to the suite's own flags (`-gateway-class`,
`-run-test`, `-skip-tests`, `-report-output`, `-debug`, and so on; pass them
after `-args`). The suite's default assumptions hold: the current kubeconfig
context is the cluster under test, and the Gateways it creates are reachable
from wherever `go test` runs at the address the controller publishes.

From the repository root, with the kind cluster prepared as `integration/kind/README.md`
describes:

```sh
make kind-integration-start
make kind-integration-ingress
make kind-integration-gateway
make kind-conformance          # Linux: the worker node's address routes from the host
make kind-conformance-docker   # macOS: the same, inside a container on the kind network
```

The report is written to `reports/<version>.yaml`; the `integration-kind` CI
job publishes it as a workflow artifact, and the most recent run's report is
kept in `reports/`. Its core result is `partial`: every core test passes but
`HTTPRouteMultipleGateways`, which `skippedTests` names because it expects
two Gateways declaring one port to answer at distinct addresses. The cluster
serves the experimental CRD channel, since the suite's retry tests need
`spec.rules[].retry`. To run one test while working on the controller:

```sh
cd integration/conformance
TRICKSTER_KIND_TEST=1 go test -v -run TestConformance . -args -gateway-class=trickster -run-test=HTTPRouteMatching
```
