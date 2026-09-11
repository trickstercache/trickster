# Query Range Limits

Trickster supports enforcing limits on the maximum time range (duration) of incoming queries. This feature protects both the Trickster cache and the downstream origin TSDBs from resource-intensive, oversized queries.

## Overview

When a client sends a query spanning a duration larger than the configured limit, Trickster intercepts the request early in the pipeline. It rejects the query, returns an HTTP `400 Bad Request` status, and increments a metric counter. This helps prevent large queries (e.g., querying 1 year of data at 15s resolution) from causing out-of-memory (OOM) issues or heavy processing spikes.

## Configuration

You can configure the query range limit on any backend by setting the `max_query_range` option. The value is a duration string (e.g., `1h`, `1d`, `14d`).

Here is a configuration example:

```yaml
backends:
  prometheus_dev:
    provider: prometheus
    origin_url: http://prometheus-origin:9090
    max_query_range: 14d
```

Setting `max_query_range: 0` or omitting the field disables the range limit enforcement.

## Supported and Unsupported Backends

Query range limit enforcement is active on backends that implement the `backends.TimeseriesBackend` interface, which includes:

- **Supported Backends:**
  - Prometheus
  - InfluxDB
  - ClickHouse
  - Application Load Balancers (ALBs) using Time Series Merge (TSM)
- **Unsupported Backends:**
  - Standard HTTP Reverse Proxy backends (e.g., `reverseproxycache`) that do not parse the query timespan.

## Rejection Behavior

When an incoming query's duration exceeds the allowed `max_query_range`:
1. **HTTP Status:** Trickster responds immediately with HTTP `400 Bad Request`.
2. **Error Message:** The response body contains an error message, e.g., `query time range exceeds the allowed limit of 336h0m0s`.
3. **Short-circuiting:** Downstream origin TSDB instances are never contacted, conserving database resources.

## Metrics and Logging

Trickster exposes metrics and logs to track rejected queries:

### Metrics
- **Metric Name:** `trickster_proxy_query_range_rejected_total`
- **Labels:** `backend` (name of the backend rejecting the query)
- **Type:** Counter
- **Description:** Tracks the total number of queries rejected due to exceeding the `max_query_range` limit.

### Logs
Upon query rejection, Trickster emits a structured warning log including information about the offending query:
```text
[WARN] query rejected due to max_query_range limit (backendName=prometheus_dev, limit=336h0m0s, duration=360h0m0s, clientIP=127.0.0.1, path=/api/v1/query_range, statement=up)
```

## Use with ALBs

Application Load Balancer (ALB) backends configured with the Time Series Merge (TSM) mechanism can enforce query range limits at the ALB entry point. This ensures that Trickster rejects oversized queries *before* scattering them across downstream pool member backends.

For details on using `max_query_range` with TSM ALBs, refer to the [ALB Documentation](./alb.md).
