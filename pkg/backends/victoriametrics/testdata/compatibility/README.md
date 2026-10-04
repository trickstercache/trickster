# VictoriaMetrics Compatibility Corpus

`v1.json` is the executable specification for the MetricsQL cache contract. Each
case names a MetricsQL statement, where it came from, the cache path the provider
must give it, and what native VictoriaMetrics returned for it. `requests` holds the
request shapes Grafana 13.1.3 sent to VictoriaMetrics, captured through a logging
proxy, with the path the provider must give each.

The `upstream` block pins the VictoriaMetrics image and the metricsql parser the
contract was validated with, and the server flags that change results. The
`native` fields were recorded from that image, over a settled six-hour range of
the developer environment's seed data with `nocache=1`: result type, label keys,
whether the metric name is kept, non-finite values, and errors. They document the
origin's behavior; the unit tests check the classifications and request paths,
and `TestCorpusCoversDashboard` requires every MetricsQL panel query of the
`Trips (VictoriaMetrics)` dashboard, as Grafana expands it, to be a case.

Cases with `range_function`, `range_aggregate` or `limit_modifier` reasons
differed between one range query and the same range fetched in two halves, or
depend on the whole range by definition, so they are cached only as exact
requests. Statements that don't parse, or that call volatile functions, are
relayed so VictoriaMetrics answers them itself.

Changing the pinned image or parser requires recording the corpus again and
rerunning the developer environment's differential checks.
