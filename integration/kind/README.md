# Kubernetes Integration Scenarios

A kind cluster hosting the autodiscovery, Ingress controller, Gateway API
controller and conformance scenarios. Every test here is gated on
`TRICKSTER_KIND_TEST=1`; `make kind-integration-test` runs them all once the
three deploy targets below have been applied.

## Kubernetes autodiscovery scenario

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

The `integration-kind` CI job runs the same flow, followed by the controller
scenario below. The cluster maps the in-cluster Trickster's NodePorts to host
ports 30080 (front) and 30081 (metrics) via kind extraPortMappings.

## Kubernetes controller endpoint-mode scenario

`ingress-endpoint.yaml` deploys a Trickster configured as an Ingress
controller in the `endpoint` routing mode: the backend its Ingress generates
is an ALB whose pool is discovered from the `webecho` Service's
EndpointSlices, on NodePorts 30083 (front) and 30084 (metrics).
`TestIngressEndpointModeKind` drives a rolling restart of `webecho` under
sustained load through the Ingress and asserts zero client errors, then
scales the Deployment and asserts membership follows.

```sh
make kind-integration-start
make kind-integration-ingress
cd integration
TRICKSTER_KIND_TEST=1 go test -run TestIngressEndpointModeKind -v .
cd ..
make kind-integration-stop
```

## Kubernetes controller cache policy scenario

`cache-policy.yaml` deploys a real Prometheus behind three Ingresses served by
the `trickster-ingress` controller below, with a `TricksterCachePolicy` on the
Prometheus Service selecting the `prometheus` provider, another on the
second Ingress hiding `X-Trickster-Result`, and a third on a second Service
naming the `objects` cache that the controller's configuration declares but
no file backend references. `TestCachePolicyKind` asserts a range query is
answered by the Delta Proxy Cache, that the same query is a cache hit the
second time, that the second Ingress carries no result header, and that the
third Ingress reports a `kmiss` then a hit rather than degrading to a plain
reverse proxy because its cache was pruned at load. The CRD from
`deploy/kube/crds` is applied first, since the controller probes for it once
at startup.

```sh
make kind-integration-start
make kind-integration-ingress
cd integration
TRICKSTER_KIND_TEST=1 go test -run TestCachePolicyKind -v .
cd ..
make kind-integration-stop
```

## Ingress controller scenario

`ingress.yaml` deploys a second Trickster into the same namespace, configured
as an Ingress v1 controller in the `service` routing mode in front of the
`webecho` Service, on NodePort 30082. Its `kubernetes.ingress.listener_names`
names an ordinary `web` listener defined in the same config. It is applied
by `make kind-integration-ingress` alongside the endpoint-mode scenario;
`TestIngressKind` asserts the rewrite, the response header annotation and
that nothing unclaimed is served.

```sh
make kind-integration-start
make kind-integration-ingress
cd integration
TRICKSTER_KIND_TEST=1 go test -run TestIngressKind -v .
curl -sSi -H 'Host: shop.example.com' http://localhost:30082/api/hello
```

The response is `webecho`'s echo of the request, showing the path rewritten
to `/hello`, and carries the `X-Trickster-Ingress` header the annotation
adds. A host or path the Ingress does not claim returns 404.

## Gateway API controller scenarios

`gateway.yaml` deploys a Trickster configured as a Gateway API controller in
the `service` routing mode, claiming the `trickster` GatewayClass in every
namespace, plus the fixture apps the scenarios need: a canary copy of
`webecho` whose body names it, and go-httpbin for WebSocket echo, large
bodies and cacheable responses. The edge Gateway's HTTP and HTTPS listeners
are NodePorts 30085 and 30086, its metrics 30087, and the listener the
annotation scenario's Ingress below names 30088. `make kind-integration-gateway` installs
the Gateway API CRDs (experimental channel, since HTTPRoute retries exist
only there) from the module `go.mod` pins, applies the manifests,
and sets the controller Service's `externalIPs` to the worker node, which is
the address published into Gateway status and how the conformance suite
reaches the Gateways it creates. It expects `make kind-integration-ingress`
to have run first, for the cache policy CRD and the Prometheus it deploys.

- `TestGatewayKind` covers host and path routing, a header match, a prefix
  rewrite, a weighted canary, a redirect, WebSocket echo, a 10 MiB body, a
  cache hit on a cacheable response, Prometheus acceleration through a
  `TricksterCachePolicy`, and TLS from a Secret rotated under load without a
  reload or a connection reset.
- `TestGatewayRollingUpgradeKind` rolls the controller Deployment under load,
  which is what a `helm upgrade` of the controller drives, and asserts zero
  client errors: a new pod is ready only once its routes are programmed.
- `TestIngressAnnotationsKind` serves `ingress-annotations.yaml`, an Ingress
  carrying annotations of another controller's namespace beside
  `trickstercache.org/` ones, and asserts the foreign annotations are
  ignored, the recognized ones apply, and an unrecognized
  `trickstercache.org/` annotation is rejected with an Event.

```sh
make kind-integration-start
make kind-integration-ingress
make kind-integration-gateway
cd integration
TRICKSTER_KIND_TEST=1 go test -run 'TestGatewayKind|TestGatewayRollingUpgradeKind|TestIngressAnnotationsKind' -v .
```

## Gateway API conformance

`make kind-conformance` runs the upstream conformance suite's `GATEWAY-HTTP`
profile against the same controller and writes the report to
`integration/conformance/reports/`; see `integration/conformance/README.md`.
The suite runs on the host and reaches the Gateways at the worker node's
address, which routes from a Linux host but not from macOS, where
`make kind-conformance-docker` runs the same command inside a container on the
kind network instead. The `integration-kind` CI job runs the suite after the
scenarios above and publishes the report as a workflow artifact.

## Gateway soak (manual)

`TestGatewaySoakKind` runs the gateway under sustained load with continuous
HTTPRoute add/remove, a certificate rotation every minute, and a rolling
restart at the end, sampling goroutines, file descriptors, memory and the
reload and reconcile counters throughout. It is run by hand, never by CI:

```sh
make kind-soak SOAK_DURATION=60m SOAK_TIMEOUT=90m
```
