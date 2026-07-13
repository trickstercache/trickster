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
- exactly one mandatory `range` over the configured timestamp field, using `gte` with either `lt` or `lte`
- exactly one top-level `date_histogram` aggregation over that same timestamp field
- a positive `fixed_interval` that evenly divides a UTC day, or a UTC calendar interval of one minute, hour, or day
- complete, interval-aligned buckets; an inclusive `lte` must identify the final millisecond of the last bucket, while an exclusive `lt` identifies the next bucket boundary
- array-form buckets (`keyed` is absent or false), ascending key order, no bucket `offset`, and either no `time_zone` or a UTC time zone
- no pipeline aggregations, because their values depend on buckets outside an independently fetched cache extent

Elasticsearch returns empty buckets between the first and last matching document by default. Trickster supports `min_doc_count` values of zero or one. To preserve empty buckets when Trickster fetches missing extents independently, a histogram with `min_doc_count: 0` (or no `min_doc_count`) must include both `extended_bounds.min` and `extended_bounds.max` covering the requested bucket range. Any `extended_bounds` or `hard_bounds` values must resolve to the same first and last buckets as the timestamp range.

The range can use RFC3339 timestamps or epoch milliseconds. Numeric dates use Elasticsearch's default epoch-millisecond interpretation unless the range `format` selects `epoch_second`; epoch-second ranges must use boundaries that can represent every bucket exactly. Trickster normalizes the range values in the cache key and rewrites only the range, `extended_bounds`, and `hard_bounds` values when fetching missing cache extents. Each returned bucket is retained as Elasticsearch JSON, including ordinary nested bucket and metric aggregations.

Variable calendar intervals, legacy `interval`, partial edge buckets, shifted or non-UTC bucket boundaries, multiple timestamp ranges, multiple top-level aggregations, pipeline aggregations, descending bucket order, response-shaping search options, and searches that request document hits fall back to Object Proxy Cache. This avoids changing query semantics or caching a partial bucket as a complete extent.

## Multi Search

For `_msearch`, Trickster supports the standard newline-delimited header/body pair format. Every search body in the request must match the cacheable shape above and describe the same time range and histogram interval so a single cache extent can be applied safely.

## Fallback Behavior

Requests that do not match the supported time-series shape are not forced through the time-series modeler. Elasticsearch `GET` requests use Object Proxy Cache when possible, unsupported search requests fall back to Object Proxy Cache using the exact request body in the cache key, and write-oriented or unsafe methods are proxied without caching.

Delta-cached responses preserve the histogram buckets but reconstruct query-level metadata. `took` is reported as zero and `hits.total` is calculated from bucket `doc_count` values. For an exact total, the configured timestamp field must be a single-valued millisecond-resolution `date` field and documents must use normal document counts.

For narrowly scoped reverse-proxy caching of custom Elasticsearch endpoints, you can still use `provider: reverseproxycache` and configure path-level cache key behavior explicitly.
