# Elasticsearch Support

Trickster can accelerate Elasticsearch search requests that return date histogram time series, such as Grafana Elasticsearch panels. Acceleration uses the Time Series Delta Proxy Cache to avoid re-querying histogram buckets that are already cached.

## Configuration

Use `provider: elasticsearch` and point `origin_url` at the Elasticsearch HTTP endpoint:

```yaml
backends:
  elasticsearch:
    provider: elasticsearch
    origin_url: http://elasticsearch:9200
```

Elasticsearch deployments commonly use `@timestamp` as the event time field. Trickster uses that field by default. If your index uses a different date field, set `elasticsearch.timestamp_field`:

```yaml
backends:
  elasticsearch:
    provider: elasticsearch
    origin_url: http://elasticsearch:9200
    elasticsearch:
      timestamp_field: event_time
```

## Cacheable Search Requests

Trickster applies Delta Proxy Cache acceleration to `GET` or `POST` requests whose path is `/_search`, `/<index>/_search`, `/_msearch`, or `/<index>/_msearch` when each search body contains:

- `size: 0`, so the response is aggregation-only
- exactly one `range` filter over the configured timestamp field, using inclusive `gte` and `lte` bounds
- exactly one top-level `date_histogram` aggregation over that same timestamp field
- a positive `fixed_interval`, or a fixed-duration legacy `interval`, that can be interpreted as the time step
- array-form buckets (`keyed` is absent or false), with no bucket `offset` and either no `time_zone` or a UTC time zone

The range can use RFC3339 timestamps, epoch seconds, or epoch milliseconds. Trickster normalizes the range values in the cache key and rewrites only those range, `extended_bounds`, and `hard_bounds` values when fetching missing cache extents. Each returned bucket is retained as Elasticsearch JSON, including nested metric aggregations.

Calendar intervals, shifted or non-UTC bucket boundaries, multiple timestamp ranges, multiple top-level aggregations, and searches that request document hits fall back to Object Proxy Cache. This avoids changing query semantics or dropping response fields that cannot be merged safely as fixed time-series extents.

## Multi Search

For `_msearch`, Trickster supports the standard newline-delimited header/body pair format. Every search body in the request must match the cacheable shape above and describe the same time range and histogram interval so a single cache extent can be applied safely.

## Fallback Behavior

Requests that do not match the supported time-series shape are not forced through the time-series modeler. Elasticsearch `GET` requests use Object Proxy Cache when possible, unsupported search requests fall back to Object Proxy Cache using the exact request body in the cache key, and write-oriented or unsafe methods are proxied without caching.

For narrowly scoped reverse-proxy caching of custom Elasticsearch endpoints, you can still use `provider: reverseproxycache` and configure path-level cache key behavior explicitly.
