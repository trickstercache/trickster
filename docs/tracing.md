# Distributed Tracing via OpenTelemetry

Trickster instruments Distributed Tracing with [OpenTelemetry](http://opentelemetry.io/), which is a currently emergent, comprehensive observability stack that is in Public Beta. We import the [OpenTelemetry golang packages](https://github.com/open-telemetry/opentelemetry-go) to instrument support for Jaeger and Zipkin tracing, as well as a StdOut tracer that is useful for debugging and local testing. As OpenTelemetry evolves to support additional exporter formats, we will work to extend Trickster to support those as quickly as possible.

## Configuration

Trickster allows the operator to configure multiple tracing configurations, which can be associated into each Origin configuration by name.

The [example config](../cmd/trickster/conf/example.conf) has exhaustive examples of configuring Trickster for distributed tracing.
