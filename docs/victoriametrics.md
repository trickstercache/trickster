# VictoriaMetrics Provider

Use `provider: victoriametrics` for VictoriaMetrics: single-node, or one tenant of a cluster's
vmselect. Trickster accelerates MetricsQL range queries through VictoriaMetrics' Prometheus
querying API, caches settled instant and metadata queries, and serves its Graphite render, find
and tags APIs. VictoriaMetrics is its own provider rather than a flavor of `prometheus`:
MetricsQL is a different language, and native VictoriaMetrics, not PromQL, decides every answer.

## Surfaces

| Surface | Routes | Behavior |
| --- | --- | --- |
| MetricsQL range queries | `/api/v1/query_range` | Delta proxy cache, or exact-request object cache for expressions that depend on the whole range |
| MetricsQL instant queries | `/api/v1/query` | Object cache once the evaluation time has settled; relayed near the live edge |
| Metadata | `/api/v1/series`, `/api/v1/labels`, `/api/v1/label/<name>/values` | Object cache, 30 seconds |
| Other read APIs | `/api/v1/status/...`, `/api/v1/metadata` and the rest of the Prometheus catalog | Object cache, 30 seconds, keyed on every parameter |
| Graphite render | `/render` | Exact-request object cache for settled absolute ranges; relayed otherwise |
| Graphite discovery | `/metrics/find`, `/metrics/expand`, `/tags...`, `/functions` | Object cache, 30 seconds |
| Graphite tag registration | `/tags/tagSeries`, `/tags/tagMultiSeries`, `/tags/delSeries` | Relayed uncached |
| Everything else | writes, imports, exports, `/api/v1/admin/...`, `/internal/...`, `/federate`, `/vmui` | Relayed uncached |

Every Graphite route is also served under `/graphite`, as single-node VictoriaMetrics serves it.

## Configuration

`origin_url` is the base of the Prometheus querying API: the server for single-node
VictoriaMetrics, or a tenant's `/select/<tenant>/prometheus` path on vmselect. The Graphite
APIs are served from the same server, with a trailing `/prometheus` replaced by `/graphite`, as
vmselect's tenant paths require.

```yaml
backends:
  vm:
    provider: victoriametrics
    origin_url: http://victoriametrics:8428

  vm-tenant:
    provider: victoriametrics
    # Graphite requests go to http://vmselect:8481/select/0/graphite
    origin_url: http://vmselect:8481/select/0/prometheus
    victoriametrics:
      # replaces the derived Graphite path, e.g. for a gateway with its own routes
      graphite_path: /select/0/graphite
      # set when VictoriaMetrics runs with -search.disableCache (see Range Queries)
      search_disable_cache: false
```

| Option | Default | Description |
| --- | --- | --- |
| `victoriametrics.graphite_path` | derived | The upstream path of the Graphite APIs: an absolute path, without a query |
| `victoriametrics.search_disable_cache` | `false` | Mirrors the origin's `-search.disableCache` flag |
| `volatile_window` | `60s` | How long recent points stay provisional; keep it longer than the origin's `-search.latencyOffset` (30s) plus its ingestion delay |

Authentication headers, TLS and request rewriting work as for any HTTP backend; requests that
carry `Authorization` are cached under keys that include it. The default health check runs
`query=1` against the query API, so it also checks a tenant's path.

## Range Queries

Trickster answers a range query on the grid VictoriaMetrics evaluates it on. With its response
cache on, VictoriaMetrics aligns a range of 50 or more points to start on a step boundary and
keeps the number of points; a shorter range keeps the requested start. Trickster rewrites the
range the same way before looking in its cache, so cached and native answers have the same
timestamps. Grafana aligns its ranges itself, so its requests are unchanged. With
`search_disable_cache: true`, every range keeps the requested start, as VictoriaMetrics then does.

### Cache Eligibility

Each MetricsQL statement is parsed once with VictoriaMetrics' own parser
([metricsql](https://github.com/VictoriaMetrics/metricsql), pinned to the version the provider
was validated with); `WITH` templates are expanded before classification.

| Statement | Path |
| --- | --- |
| Selectors, rollups (`rate`, `increase`, `*_over_time`, `rollup`, with or without a window), aggregations, `topk`/`bottomk`, binary operators, label and math functions, `offset`, subqueries, `@` with a timestamp | Delta proxy cache |
| `range_*`, `running_*`, `keep_last_value`, `keep_next_value`, `interpolate`, `remove_resets`, `smooth_exponential`, `sort*`, `limit_offset`, `drop_common_labels`, `drop_empty_series`, `union`, `buckets_limit`, `start()`, `end()`; `topk_*`/`bottomk_*` variants, `limitk`, `any`, `outliers*`; the `limit` modifier; `@` with anything but a timestamp | Exact-request object cache (step alignment `off`) |
| `now()`, `rand()`, `rand_normal()`, `rand_exponential()`, and statements that don't parse | Relayed uncached |

A statement that doesn't parse is relayed, so VictoriaMetrics returns its own error. A
request is also relayed uncached when it:

- sets `nocache` or `trace`;
- has a parameter the route doesn't read, or a parameter repeated, or the same parameter in
  both the URL and a POST form;
- uses a time VictoriaMetrics would read differently than Trickster: anything but Unix seconds
  (with at most millisecond precision) or RFC 3339 with a zone, such as `-1h`, `now`, a
  zoneless time, or milliseconds;
- is an instant query within the volatile window of now, or without a `time`;
- is a POST that isn't `application/x-www-form-urlencoded`.

`round_digits`, `latency_offset`, `max_lookback`, `extra_label`, `extra_filters[]`, `limit` and
`optimize_repeated_binary_op_subexprs` are part of a response's cache identity; `timeout` and
`denyPartialResponse` can only fail a request and are not. The classification of each request
is counted in `trickster_victoriametrics_query_analysis_total`.

### Fidelity

VictoriaMetrics evaluates a point from the samples it fetched for the whole request: an implicit
window, and whether `rate` and similar functions use the sample before their window, depend on a
scrape interval estimated from the last 20 sample intervals it read. Where a series' scrape
interval changes, a range assembled from separate fetches can differ slightly from one
`nocache=1` evaluation of it. VictoriaMetrics' own response cache, which is on by default,
assembles ranges from separate evaluations the same way, so the delta proxy cache's answers match
what VictoriaMetrics serves by default. For a single exact evaluation, set
`step_alignment: off` on the backend, or write `# trickster-step-align:off` in the query.

Points within the volatile window of now, which VictoriaMetrics replaces within its
`-search.latencyOffset`, are fetched on every request and never stored. A request's own
`latency_offset` widens that window.

### Partial Responses

When some vmstorage nodes don't answer, vmselect marks its response `"isPartial":true`.
Trickster never caches such a response: a range query is relayed to the origin, and other API
responses are marked `Cache-Control: no-store`. Responses served from cache for an origin that
reports `isPartial` include `"isPartial":false`, as vmselect writes it. The `stats` object
VictoriaMetrics adds to query responses describes one evaluation and is not kept in cached
responses.

## Graphite

The Graphite APIs use the `Storage-Step` request header, which sets the grid the render API
returns; it is part of a render response's cache identity, with every query parameter. A render
is consolidated to `maxDataPoints` over its whole range, so renders are cached only as exact
requests, never assembled from separate ranges. A render is cached only when `from` and `until`
are Unix timestamps and `until` is outside the volatile window; Grafana sends both that way.

## Time Series Merging

The `tsm` ALB mechanism doesn't support VictoriaMetrics backends yet: the provider offers no merge
planning, so MetricsQL is never merged with PromQL's semantics, and requests through a `tsm` pool
of VictoriaMetrics backends fail. Other ALB mechanisms work as for any backend.

## Known Limits

- Delta caching is not offered for the Graphite render API.
- `partial_end` (Fast Forward) is not offered.
- An unfiltered `/tags` or `/tags/<tag>` returns nothing for series VictoriaMetrics stored as
  labels; `/tags/autoComplete/tags?expr=...`, `/tags/findSeries` and `seriesByTag` find them.
  This is VictoriaMetrics' behavior and is relayed as is.
- Requests through the frontend's `/graphite` alias are mapped to the Graphite path for vmselect
  origins too, where vmselect has no such alias.
- Raw-series export (`/api/v1/export`) is relayed uncached.

## Developer Environment

The developer environment runs single-node VictoriaMetrics v1.153.0 with seeded trips history
and fixtures, the `victoriametrics1` backend, and the `Trips (VictoriaMetrics)` and
`VictoriaMetrics Graphite` dashboards. See
[VictoriaMetrics Details](./developer/environment/README.md#victoriametrics-details).
