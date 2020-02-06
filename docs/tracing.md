# Distributed Tracing via OpenTelemetry

Trickster has minimal support for OpenTelemetry, which is a currently emergent, comprehensive observability stack. We import the OpenTelemetry golang packages to instrument support for Jaeger tracing. As OpenTelemetry evolves to support additional exporter formats, we will work to extend  Trickster to support those as quickly as possible. Our first priority when considering support for additional exporter formats is centered around Zipkin.

## Configuration

Trickster allows the operator to configure multiple tracing configurations, which can be associated into each Origin configuration by name.

The [example config](../cmd/trickster/conf/example.conf) has exhaustive examples of configuring Trickster for distributed tracing.
