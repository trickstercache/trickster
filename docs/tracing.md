# Distributed Tracing via OpenTelemetry

Trickster instruments Distributed Tracing with [OpenTelemetry](http://opentelemetry.io/), which is a currently emergent, comprehensive observability stack that is in Public Beta. We import the [OpenTelemetry golang packages](https://github.com/open-telemetry/opentelemetry-go) to instrument support for Jaeger and Zipkin tracing, as well as a StdOut tracer that is useful for debugging and local testing. As OpenTelemetry evolves to support additional exporter formats, we will work to extend Trickster to support those as quickly as possible.

## Configuration

Trickster allows the operator to configure multiple tracing configurations, which can be associated into each Origin configuration by name.

The [example config](../cmd/trickster/conf/example.conf) has exhaustive examples of configuring Trickster for distributed tracing.

## Span List

Trickster adds several spans to the traces that it captures, as described in this table.

| Span Name              | Observes when Trickster is: |
| ---------------------- | ------------- |
| QueryCache             | querying the cache for an object |
| WriteCache             | writing an object to the cache |
| DeltaProxyCacheRequest | handling a Time Series-based client request |
| FastForward            | making a Fast Forward request for time series data |
| ProxyRequest           | communicating with an Origin server to fulfill a client request |
| PrepareFetchReader     | preparing a client response from a cached or Origin response |
| CacheRevalidation      | revalidating a stale cache object against its Origin |
