# Distributed Tracing via OpenTelemetry

Trickster instruments Distributed Tracing with [OpenTelemetry](http://opentelemetry.io/), which is a currently emergent, comprehensive observability stack that is in Public Beta. We import the [OpenTelemetry golang packages](https://github.com/open-telemetry/opentelemetry-go) to instrument support for tracing.

As OpenTelemetry evolves to support additional exporter formats, we will work to extend Trickster to support those as quickly as possible. We also make a best effort to update our otel package imports to the latest releases, whenever we publish a new Trickster release. You can check the [go.mod](../go.mod) file to see which release of opentelemetry-go we are is using. In this view, to see which version of otel a specific Trickster release imports, use the branch selector dropdown to switch to the tag corresponding to that version of Trickster.

## Supported Tracing Backends

- Jaeger
- Jaeger Collector
- Zipkin
- Console/Stdout (printed locally by the Trickster process)

## Configuration

Trickster allows the operator to configure multiple tracing configurations, which can be associated into each Origin configuration by name.

The [example config](../cmd/trickster/conf/example.conf) has exhaustive examples of configuring Trickster for distributed tracing.

## Span List

Trickster can insert several spans to the traces that it captures, depending upon the type and cacheability of the inbound client request, as described in the table below.

| Span Name              | Observes when Trickster is: |
| ---------------------- | ------------- |
| request                | initially handling the client request by an Origin |
| QueryCache             | querying the cache for an object |
| WriteCache             | writing an object to the cache |
| DeltaProxyCacheRequest | handling a Time Series-based client request |
| FastForward            | making a Fast Forward request for time series data |
| ProxyRequest           | communicating with an Origin server to fulfill a client request |
| PrepareFetchReader     | preparing a client response from a cached or Origin response |
| CacheRevalidation      | revalidating a stale cache object against its Origin |

## Tags / Attributes

Trickster supports adding custom tags to every span via the configuration. See the example.conf.

Trickster also supports omitting any tags that Trickster inserts by default. For example on the "request" span, an `http.url` tag is attached with the current full URL. In deployments where that tag may introduce too much cardinality in your backend trace storage system, you may wish to omit that tag and rely on the more concise `path` tag. Each tracer config can be provided a string list of tags to omit from traces.
