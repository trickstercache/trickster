# Prometheus Support

Trickster fully supports accelerating Prometheus, which we consider our First Class backend provider. They work great together, so you should give it a try!

Most configuration options that affect Prometheus reside in the main Backend config, since they generally apply to all TSDB providers alike.

## Supported API Endpoints

Trickster supports the full [Prometheus HTTP API (v1)](https://prometheus.io/docs/prometheus/latest/querying/api/), including features introduced in Prometheus 3.x.

### Cached Endpoints

| Endpoint | Cache Strategy | Scatter/Gather Merge |
|---|---|---|
| `/api/v1/query_range` | Delta Proxy Cache | Yes |
| `/api/v1/query` | Object Proxy Cache | Yes |
| `/api/v1/series` | Object Proxy Cache | Yes |
| `/api/v1/labels` | Object Proxy Cache | Yes |
| `/api/v1/label/<name>/values` | Object Proxy Cache | Yes |
| `/api/v1/alerts` | Proxy + Merge | Yes |
| `/api/v1/targets` | Object Proxy Cache | No |
| `/api/v1/targets/metadata` | Object Proxy Cache | No |
| `/api/v1/rules` | Object Proxy Cache | No |
| `/api/v1/alertmanagers` | Object Proxy Cache | No |
| `/api/v1/status/*` | Object Proxy Cache | No |
| `/api/v1/query_exemplars` | Object Proxy Cache | No |
| `/api/v1/metadata` | Object Proxy Cache | No |
| `/api/v1/format_query` | Object Proxy Cache | No |
| `/api/v1/parse_query` | Object Proxy Cache | No |
| `/api/v1/scrape_pools` | Object Proxy Cache | No |
| `/api/v1/features` | Object Proxy Cache | No |

### Proxied Endpoints (not cached)

| Endpoint | Notes |
|---|---|
| `/api/v1/notifications/live` | SSE streaming |
| `/api/v1/write` | Remote write (v1 and v2) |
| `/api/v1/otlp/v1/metrics` | OTLP ingestion |
| `/api/v1/admin/*` | Explicitly unsupported (returns error) |

All other `/api/v1/*` paths are reverse-proxied to the origin without caching.

### Prometheus 3.x Features

- **Native histograms** are fully supported in query and query_range responses, including mixed series with both float samples and histogram samples.
- **UTF-8 metric and label names** (e.g., `{"metric.name"}`) are supported in queries and cache keys.
- **Query stats** (`stats=all` parameter) are cache-key differentiated, so responses with and without stats are cached separately.

## Injecting Labels

Trickster can inject labels on a per-backend basis into Prometheus responses before returning them to the caller.

Here is the basic configuration for adding labels:

```yaml
backends:
  prom-1a:
    provider: prometheus
    origin_url: http://prometheus-us-east-1a:9090
    prometheus:
      labels:
        datacenter: us-east-1a

  prom-1b:
    provider: prometheus
    origin_url: http://prometheus-us-east-1b:9090
    prometheus:
      labels:
        datacenter: us-east-1b
```

### Interaction with ALB Merge Strategy

When using label injection with an ALB configured for [Time Series Merge](./alb.md#time-series-merge), injected labels are automatically stripped from responses before merging. This ensures that series from different backends are aggregated correctly, and the injected labels do not appear in the final response to the caller. See the [ALB Merge Strategy documentation](./alb.md#merge-strategy) for details.
