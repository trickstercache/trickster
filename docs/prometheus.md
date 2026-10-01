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

### Range Query Notes

- A `step` may be fractional seconds (for example `1.5` or `0.5`), as Prometheus accepts; Trickster keeps the fraction and sends sub-second `start` and `end` values with millisecond precision.
- A `query_range` whose expression uses the `@ start()` or `@ end()` modifier is proxied without caching. Those modifiers resolve against each request's own range, so results fetched for part of a range could not be combined. A fixed `@ <timestamp>` is cached normally.

### Step Alignment

Range queries default to the `partial_end` [step alignment](./step-alignment.md) mode: points on the step grid from the grid point at or before `start`, plus Trickster's [Fast Forward](../README.md#3-fast-forward) point at the current time when the range reaches it. Fast Forward's point is fetched through the object cache for `partial_bucket_ttl`, and skipped when the step is no longer than that.

- `truncate` is the same without Fast Forward. It is the default with `fast_forward_disable: true`, which can't be set alongside `step_alignment`.
- `drop` starts at the grid point at or after `start`, so no point falls before the requested range.
- `off` sends each range query as the client sent it, and caches the response as an object for one minute.
- `partial` and `partial_start` aren't supported: PromQL evaluates at instants, so there is no partial bucket at the start.

A query can choose its own mode, or turn Fast Forward off, with a comment: `up # trickster-step-align:drop` or `up # trickster-fast-forward:off`. See [Per-Query Instructions](./per-query-instructions.md).

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

## Max Query Range Limitation

Trickster supports enforcing a `max_query_range` limit on Prometheus backends. For details on how to configure and use query range limits, see the [Query Range Limits](./query-range-limits.md) documentation.
