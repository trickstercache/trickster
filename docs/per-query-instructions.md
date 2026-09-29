# Per-Query Time Series Instructions

A query can change how Trickster caches and serves it with directives written in
its own comments, so a query's author can adjust it without changing the
backend's configuration. For example, in PromQL:

```promql
sum(rate(http_requests_total[5m])) # trickster-step-align:drop
```

- Directives are read from comments only. The same text inside a string literal,
  or anywhere else in the query, is not a directive.
- Several directives can share one comment, separated by spaces. When a
  directive appears more than once, the last one wins, and an invalid value is
  ignored.
- Directives change how a query is cached and served, never what a complete
  bucket holds, so they are not part of the cache key: the same query with and
  without them shares one cache entry.
- The query is sent to the origin as written; the origin ignores the comment.
- Directives apply to queries Trickster caches as time series. A query served
  from the object cache, or proxied, is unaffected.

## Directives

| Directive | Values | Effect |
|---|---|---|
| `trickster-step-align` | `truncate`, `drop`, `partial`, `partial_start`, `partial_end`, `off` | Selects the query's [step alignment](./step-alignment.md) mode, overriding the backend's. A mode the query doesn't support falls back to its default, counted in `trickster_step_alignment_fallbacks_total`. |
| `trickster-volatile-window` | whole seconds, or a duration such as `90s` or `5m` | Replaces the backend's `volatile_window` for this query. |
| `trickster-fast-forward` | `off` | Turns [Fast Forward](../README.md#3-fast-forward) off for this Prometheus query. A `trickster-step-align` directive in the same query takes precedence. |

Under an [ALB](./alb.md) that applies a step alignment mode to its pool, a
`trickster-step-align` directive wins when every pool member supports the mode it
names; otherwise the ALB's mode applies.

## Comment Syntax by Backend

| Backend and query language | Comments |
|---|---|
| Prometheus, GreptimeDB PromQL | `# ...` to the end of the line |
| InfluxQL | `-- ...` to the end of the line, `/* ... */` |
| Flux | `// ...` to the end of the line |
| InfluxDB SQL, Flight SQL, PostgreSQL, TimescaleDB, Druid SQL, GreptimeDB SQL and PostgreSQL | `-- ...` to the end of the line, `/* ... */` |
| MySQL, GreptimeDB MySQL | `-- ...` (with a space or tab after the dashes), `# ...`, `/* ... */` |
| ClickHouse | `-- ...` to the end of the line, `/* ... */` |
| Druid native queries | the query's `context` map, keyed by the directive's full name |
| Graphite, InfluxDB Prometheus remote read | none |

Notes:

- ClickHouse accepts `#` comments too, but Trickster's ClickHouse SQL parser
  doesn't, so a statement using one is served from the object cache, or proxied,
  and its directives don't apply.
- A PostgreSQL escape string (`E'...'`) is a string literal, so directive text
  inside one is not a directive.
- A Druid native query has no comments, so it carries directives as context
  keys, which Trickster leaves out of the cache key and Druid ignores:

  ```json
  "context": { "trickster-step-align": "partial_end", "trickster-volatile-window": "2m" }
  ```

  A Druid SQL query can use both. For the same directive, its SQL comment wins
  over its `context`.
