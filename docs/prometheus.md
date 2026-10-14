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

## Flavors

`prometheus.flavor` adapts a backend to a Prometheus-compatible managed
service, applying its supported routes, limits and defaults.

| flavor | service |
| ----- | ----- |
| `cloudwatch` | Amazon CloudWatch PromQL |
| `amp` | Amazon Managed Service for Prometheus |
| `mimir` | Grafana Mimir |
| `thanos` | Thanos |

```yaml
backends:
  cloudwatch:
    provider: prometheus
    prometheus:
      flavor: cloudwatch
    sigv4:
      region: us-east-1
```

The `cloudwatch` and `amp` flavors limit the backend to the routes the service supports and
answer every other path themselves. See [AWS Integration](./aws.md) for what
each one sets. The `mimir` and `thanos` flavors change no routes; they select
the [series source](#series-sources) that TSM global evaluation reads, and that
evaluation reads their newest 5 minutes of samples again each time, since
their replicas and ingesters may still be receiving them.

### Series Sources

An ALB using [TSM global evaluation](./alb.md#global-evaluation) reads raw
series from its members rather than query results. `series_api` selects how
a backend serves them, and `series_endpoint` where:

| `series_api` | protocol | default for flavor | `series_endpoint` |
| ----- | ----- | ----- | ----- |
| `remote_read` | [Prometheus remote read](https://prometheus.io/docs/prometheus/latest/querying/remote_read_api/) over HTTP, as streamed chunks or sampled responses | `mimir` | optional; an absolute `http` or `https` URL. The default is the `origin_url` path plus `/api/v1/read` |
| `storeapi` | Thanos StoreAPI `Series` over gRPC | `thanos` | required; `grpc://host:port` for plaintext, or `grpcs://host:port` for TLS with the backend's `tls` client settings |

Any Prometheus backend can set `series_api`, since Prometheus itself serves
remote read. A StoreAPI endpoint can be a Thanos Sidecar, Store Gateway,
Receive or Querier. The `cloudwatch` and `amp` flavors serve no raw series, and
reject both options.

```yaml
backends:
  prom-a:
    provider: prometheus
    origin_url: http://prom-a:9090
    prometheus:
      series_api: remote_read
  mimir:
    provider: prometheus
    origin_url: http://mimir:8080/prometheus # remote read at /prometheus/api/v1/read
    prometheus:
      flavor: mimir
  thanos-a:
    provider: prometheus
    origin_url: http://prom-a:9090
    prometheus:
      flavor: thanos
      series_endpoint: grpc://prom-a-sidecar:10901
```

Remote read uses the backend's HTTP client settings, and sends the client's
end-to-end request headers plus the `request_headers` the backend's matching
path sets, as a proxied request would; a tenant header such as
`X-Scope-OrgID`, or an `Authorization` header, reaches the source this way.
StoreAPI calls carry the same headers as gRPC metadata.

Both transports ask for compressed answers, which mostly shrinks the label sets
the chunks travel with. Remote read asks for gzip, which Mimir answers with;
Prometheus answers remote read uncompressed. StoreAPI calls ask for snappy, as
Thanos components support; a store that rejects it is read again at once
without compression. After that read succeeds, compression stays disabled
until Trickster restarts or reloads.

A backend opens its StoreAPI connection on its first read, and closes it once a
reload, shutdown, or autodiscovery replaces the backend and its requests have
drained, as described for [global evaluation](./alb.md#global-evaluation).

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
