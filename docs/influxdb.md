# InfluxDB Support

Trickster provides support for accelerating InfluxDB queries that return time series data normally visualized on a dashboard. Acceleration works by using the Time Series Delta Proxy Cache to minimize the number and time range of queries to the upstream InfluxDB server.

## Scope of Support

Trickster is tested with the built-in [InfluxDB DataSource Plugin for Grafana](https://grafana.com/grafana/plugins/influxdb) v5.0.0.

Trickster uses InfluxDB-provided packages to parse and normalize queries for caching and acceleration. If you find query or response structures that are not yet supported, or providing inconsistent or unexpected results, we'd love for you to report those so we can further improve our InfluxDB support.

Trickster supports integrations with InfluxDB 1.x and 2.0.

### Prometheus Remote Read

For InfluxDB 1.x, Trickster accelerates Prometheus remote-read requests sent to
`POST /api/v1/prom/read`. Point Prometheus at the same endpoint on Trickster and
retain the InfluxDB query parameters, for example:

```yaml
remote_read:
  - url: http://trickster:8480/api/v1/prom/read?db=metrics&rp=autogen
```

The InfluxDB endpoint supports one query per request and the Prometheus
`SAMPLES` response type. Trickster delta-caches requests with that shape. Other
request shapes continue through the normal proxy path so that InfluxDB remains
responsible for its response and error behavior.

Raw remote-read samples do not advertise a guaranteed interval, so point-count
sharding cannot split their extents without risking gaps. Requests use the
normal proxy path when `shard_max_size_points` is enabled; time-based sharding
remains supported. Point-based backfill tolerance uses a positive
`hints.step_ms`; a request without that hint also uses the normal proxy path.

The `db`, `rp`, `u`, and `p` query parameters and the `Authorization` header are
part of the cache identity. They are forwarded unchanged unless a path-level
request rewrite or header configuration overrides them.

### A note on Flux Language Support:

Trickster supports the Flux Query Language for general/basic usage. Trickster does not support advanced union-style queries (e.g., with multiple `from` clauses). In this rare use case, these responses will currently provide invalid data, however, a subsequent beta will proxy unsupported requests.

Trickster currently does not properly handle schema changes within a response CSV body (e.g., multiple CSVs in the same document with their own #annotation and header rows). We will fully support this use case in a future beta.

## Max Query Range Limitation

Trickster supports enforcing a `max_query_range` limit on InfluxDB backends. For details on how to configure and use query range limits, see the [Query Range Limits](./query-range-limits.md) documentation.
