# GreptimeDB Compatibility Corpus

`v1.json` is run by `pkg/testutil/sqlcompat`, the same schema, macro coverage,
plan assertions and render/read-back checks used for PostgreSQL. Do not change
a released corpus's expectations without versioning the incompatible change.

The eight Grafana cases were captured from `executedQueryString` using the
bundled PostgreSQL datasource in Grafana 13.1.3 against GreptimeDB's official
`nightly-20260923-e91faa9df` image. `timescaledb` was false, the query interval
was five minutes, and the fixed request window was
`2026-09-19T00:00:03.123Z` through `2026-09-19T03:00:07.456Z`. Each query succeeded
directly. `macro_source` records the submitted SQL, not a reconstructed macro.
All fourteen PostgreSQL macro families are represented. Minimum dashboard
interval remains one minute.

The hand-written cases cover DataFusion's interval and compact `date_bin`,
`date_part`, calendar/submicrosecond fallbacks, session timezone, `RANGE/ALIGN`,
TQL, volatility and writes. A delta case must keep its canonical identity and
read back exactly the inclusive cache extent requested by the renderer.

These are pgwire analyzer expectations. HTTP SQL additionally refuses delta
rewrites that drop partial buckets. MySQL uses a different grammar and its
own analyzer and live typed-result tests. Extended pgwire queries are relayed,
not cached; Builder-mode duplicate captures and advanced clause rewriting
are explicitly omitted.
