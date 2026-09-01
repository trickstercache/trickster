# Apache Druid Support

Trickster accelerates eligible Apache Druid native JSON queries with the Time
Series Delta Proxy Cache (DPC). Configure `druid` as the backend provider and
point `origin_url` at a Druid Broker or Router.

```yaml
backends:
  druid1:
    provider: druid
    origin_url: http://druid-router:8888
    cache_name: default
    backfill_tolerance: 60s
    timeseries_retention_factor: 2048
```

## Native query acceleration

`POST /druid/v2` uses delta caching when all of these conditions hold:

- `queryType` is `timeseries`, `groupBy`, or `topN`.
- `intervals` contains exactly one ISO-8601 half-open interval.
- `granularity` has a fixed width:
  - a simple granularity from `second` through `day`;
  - a positive `duration` granularity in milliseconds; or
  - a fixed ISO-8601 `period` granularity in UTC, such as `PT15M` or `P1D`.
- The selected context does not request an alternate native response shape.

Trickster removes the interval from the logical cache identity and rewrites
only missing extents into Druid's `[start,end)` form. Druid's end is exclusive,
so the final cached bucket is rendered as `extent.End + granularity`.

The response model preserves native `timeseries`, `groupBy`, and `topN` JSON
shapes. Grouping dimensions become DataSet tags internally. Hidden typed values
and per-bucket positions preserve non-string dimensions and native row/ranking
order when a response passes through the cache.

## Object-cache fallback

A valid read query that is unsafe for delta merging automatically uses the
Object Proxy Cache (OPC) with a one-minute fallback TTL when Druid does not
provide explicit freshness headers. This includes:

- other native query types such as `scan`, `search`, `segmentMetadata`,
  `datasourceMetadata`, and `timeBoundary`;
- multiple intervals;
- `all`, `none`, `week`, `month`, `quarter`, and `year` simple granularities;
- calendar-width periods or period granularities in a non-UTC time zone; and
- response-changing contexts such as `bySegment`, `serializeDateTimeAsLong`,
  timeseries `grandTotal`, or groupBy `resultAsArray`.

The following native context keys are transport controls and are omitted from
the cache identity: `queryId`, `sqlQueryId`, `priority`, `timeout`, and
`queryDeadline`. They remain unchanged in the request sent to Druid. Semantic
context keys, including `skipEmptyBuckets`, remain part of the cache identity.

## Route policy

| Route | Method | Policy |
| --- | --- | --- |
| `/druid/v2` | POST | DPC when eligible, otherwise OPC or proxy |
| `/druid/v2/sql` | POST | OPC; the complete JSON body is part of the key |
| `/druid/v2/sql/task` | POST | Proxy only |
| `/druid/v2/datasources...` | GET | OPC |
| `/status/health` | GET | Health probe; expects `true` |
| all other routes | any | Proxy only |

Druid SQL acceleration, ingestion and management caching, ALB time-series
merging, `scan`/`search` delta caching, and Fast Forward are not supported.
Fast Forward is disabled for every Druid backend. A 60-second backfill
tolerance is used when the backend does not configure one, so recently ingested
buckets can be refreshed before segments settle.

## Observability

- `trickster_druid_query_analysis_total` counts classifications by backend,
  cache mode (`delta`, `object`, or `proxy`), and stable reason code.
- `trickster_druid_query_rewrite_failures_total` counts failed missing-extent
  rewrites by backend and fixed failure category.

Neither metric includes query text, datasource names, or request IDs.
