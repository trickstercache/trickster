# Graphite Provider

Trickster accelerates Graphite's render API with the Delta Proxy Cache: it
caches each metric at the origin's native resolution and, on subsequent
requests, fetches only the time ranges it does not already hold.

Graphite makes that harder than most time series databases, because a render
request does not say what resolution it will be answered at. Whisper chooses an
archive based on how old the query's `from` is, and no API exposes a metric's
archive ladder. Trickster therefore *learns* each ladder by probing the origin,
caches what it learns, and accelerates only the requests whose resolution it can
predict. Everything else is served correctly through ordinary object caching.

Specify `graphite` as the provider:

```yaml
backends:
  graphite1:
    provider: graphite
    origin_url: 'http://graphite.example.com:80'
    cache_name: default
```

That is the whole minimum configuration. Graphite's cache sizing differs from
every other provider's, so the provider supplies its own defaults rather than
asking operators to discover them; see [Sizing](#sizing) for what they are and
when to change them.

## Compatibility

Tested against graphite-web 1.1.10 with Whisper storage, and written against
its source. It should work with any Graphite-protocol origin that serves
`/render` and `/metrics/expand`, including go-carbon's carbonserver,
graphite-clickhouse and carbonapi, but only graphite-web is verified.

| Client | Status |
|---|---|
| Grafana's Graphite data source | Verified, GET and POST form bodies |
| Direct `/render` API clients | Verified |
| Graphite Composer / dashboard UI | Proxied, not accelerated (image formats) |

| Response format | Behavior |
|---|---|
| `json` | Accelerated. Also what Trickster requests upstream. |
| `raw`, `csv`, `msgpack` | Accelerated; rendered from the cached series |
| `png`, `svg`, `pdf`, `pickle`, `dygraph`, `rickshaw` | Proxied and object-cached |

Trickster always requests `format=json` from the origin regardless of what the
client asked for, and renders the client's format from the cached series. JSON
is the only render format that carries the `tags` object, which Grafana relies
on, and it is what makes one cached series able to serve every output format.

## Configuration

Every common backend option applies (`cache_name`, `timeseries_ttl`,
`timeseries_retention_factor`, `backfill_tolerance`, `max_object_size_bytes`,
`timeout`, `healthcheck`, `paths`, TLS, authenticators); two of them,
`max_object_size_bytes` and `timeseries_retention_factor`, take
Graphite-specific defaults, as [Sizing](#sizing) explains.

Per-path `request_headers` and `request_params` apply to the synthetic
requests resolution makes (probes, wildcard expansion) as well as to proxied
traffic, so an origin behind a static API key or one that needs `local=1`
works for learning too. What cannot work is **per-client upstream identity**:
resolution state (learned ladders, wildcard expansion tokens, the registry
generation) is backend-wide, and synthetic requests carry only the backend's
configured identity. Trickster therefore **declines acceleration** — the
request is served correctly through the object cache, which keys on the
client's `Authorization` and every declared result-affecting input —
whenever a render request carries an `Authorization` header, a header named
in the render path's `cache_key_headers`, or a parameter or form field
named in its `cache_key_params`/`cache_key_form_fields` (such as `local`,
which selects the clustered origin view) that the path's
`request_headers`/`request_params` does not statically override, and
whenever the render path or the backend itself has a request rewriter
(`req_rewriter_name`) attached, since a rewriter can change the upstream
host, path, headers, or parameters in ways synthetic requests do not see. The fallback is counted as
`trickster_graphite_fallbacks_total{reason="client_identity"}`.

If every client shares one upstream identity — Grafana with a configured
datasource credential is the common case — supply it in the `graphite`
block to re-enable acceleration:

```yaml
backends:
  graphite1:
    provider: graphite
    origin_url: 'http://graphite.example.com:80'
    graphite:
      origin_username: 'metrics'
      origin_password: '<the origin password>'
```

For origins using a non-Basic scheme, set `origin_authorization` to the
verbatim header value (e.g., `'Bearer <token>'`) instead of the
username/password pair.

The credential becomes the `Authorization` request header of every path —
proxied client traffic, synthetic resolution requests (probes, wildcard
expansion) and the default health check alike — so the origin sees one
static identity, a client-supplied `Authorization` header is replaced
rather than forwarded, and requests accelerate regardless of what the
client sent. Cached objects are keyed on the configured credential, so
rotating it invalidates previously cached responses and learned resolution
state rather than serving the old tenant's data. To authenticate
Trickster's own clients as well, attach an
[authenticator](./authenticator.md) via `authenticator_name`: the
validated client credential is stripped before proxying and the origin
credential is applied in its place.

A path config that sets its own `Authorization` in `request_headers` (or
removes it with `-Authorization`) overrides the origin credential for that
path. Appending one with `+Authorization` alongside an origin credential is
rejected at startup, since the effective header would be ambiguous. If
per-path identities then diverge across the paths resolution
uses — `/render` and `/metrics/expand` selecting different
`request_headers`/`request_params` for synthetic GETs, or `GET` and `POST`
`/render` pinned to different credentials — Trickster declines
acceleration rather than mix upstream namespaces, counted as
`trickster_graphite_fallbacks_total{reason="resolution_identity"}`.

The `graphite` block adds provider-specific options:

```yaml
backends:
  graphite1:
    provider: graphite
    origin_url: 'http://graphite.example.com:80'
    graphite:
      time_zone: UTC
      passthrough_max_data_points: false
      find_cache_ttl: 1m
      resolution_registry:
        ttl: 24h
        negative_ttl: 30s
        max_entries: 100000
        persist: true
        probe_concurrency: 2
        probe_budget: 96
      static_retentions:
        - pattern: '^collectd\.'
          retentions: '10s:6h,1m:7d,10m:5y'
```

| Option | Default | Purpose |
|---|---|---|
| `time_zone` | `UTC` | Interprets date-anchored `from`/`until` values (`midnight`, `today`, `MM/DD/YY`) when the request has no `tz` parameter. Set it to the origin's graphite-web `TIME_ZONE`, or those queries resolve to different instants than the origin uses. |
| `origin_username`, `origin_password` | unset | Static HTTP Basic credential sent as the `Authorization` header of every upstream request — proxied, synthetic and health check alike. See [Configuration](#configuration) above. |
| `origin_authorization` | unset | Verbatim `Authorization` header value for non-Basic schemes (e.g., `Bearer <token>`); mutually exclusive with `origin_username`/`origin_password`. |
| `passthrough_max_data_points` | `false` | Send the client's `maxDataPoints` upstream instead of consolidating in Trickster. See [maxDataPoints](#maxdatapoints-and-consolidation). |
| `find_cache_ttl` | `1m` | How long a wildcard target's expansion to concrete metric paths is reused. Lower it where metrics appear and disappear frequently. |
| `max_targets_per_request` | `128` | Renders carrying more targets than this are served through the object lane, untouched, before any parsing or per-target fan-out. |
| `max_target_length` | `16384` | A render with a longer single target expression (in bytes) is likewise served through the object lane untouched. |
| `max_expanded_leaves` | `4096` | A wildcard whose expansion matches more leaf paths than this is served through the object lane; the refusal is remembered for `find_cache_ttl`. |
| `max_expansion_bytes` | `2097152` | Bounds one expansion's aggregate decoded leaf-name bytes the same way. |
| `resolution_registry.ttl` | `24h` | How long a learned ladder is trusted. Whisper ladders change only when an operator runs `whisper-resize.py`, so this is deliberately long. |
| `resolution_registry.negative_ttl` | `30s` | Initial backoff after a failed resolution. Doubles per consecutive failure, capped at 10m. |
| `resolution_registry.max_entries` | `100000` | Bounds each registry layer; least-recently-used entries are evicted. |
| `resolution_registry.persist` | `true` | Write learned ladders through to this backend's cache so a restart does not relearn them. |
| `resolution_registry.probe_concurrency` | `2` | Simultaneous ladder-learning runs. |
| `resolution_registry.probe_budget` | `96` | Probes one learning run may issue before giving up. |
| `static_retentions` | empty | Seed and override, from your `storage-schemas.conf`. See [Static retentions](#static-retentions). |

### Health checks

The default health check is `GET /metrics/find?query=*`, which every
Graphite-protocol implementation serves. `/version` is not used because
carbonapi and graphite-clickhouse answer it differently.

A configured origin credential rides on the probe even when the
`healthcheck` block declares custom `headers`. To override it, set your own
`Authorization` value there; to send the probe unauthenticated, set
`Authorization: ''`.

### Routed paths

| Path | Handling |
|---|---|
| `/render` (GET, POST) | The render handler: delta cache, or object cache when the request is not accelerable |
| `/metrics/find`, `/metrics/expand`, `/metrics/index.json`, `/tags`, `/tags/*` | Object-cached, 30s. These also back resolution's own lookups, so caching them lowers probe cost. |
| `/functions`, `/version` | Object-cached, 1h |
| everything else | Proxied |

## How resolution prediction works

### Why it is necessary

Whisper picks an archive from the age of the query's left edge, not from the
width of the window:

```python
diff = now - fromTime
for archive in header['archives']:
    if archive['retention'] >= diff:
        break
step = archive['secondsPerPoint']
```

So `from=-7d&until=-6d` and `from=-7d&until=now` are answered at the *same*
step, and a query one second either side of a rung boundary is answered at
*different* steps. The delta cache must know the step before it can compose a
cache key or reason about which ranges it already holds — and the step is a
property of the metric's on-disk file, which nothing in the request reveals.

Static configuration is not sufficient on its own, because a Whisper file keeps
whatever ladder it was created with. Editing `storage-schemas.conf` affects only
newly created files, so in any long-lived installation the config and the files
routinely disagree.

### Probe and learn

On the first request for a metric Trickster does not know the ladder, so the
request is served through the object cache — correct, just not delta-cached —
and the response itself teaches the step at that age. In the background,
Trickster then discovers the metric's full ladder with a short series of
synthetic `/render` probes: a geometric sweep outward in time to find where the
step changes, then a binary search for each rung boundary, and a pair of probes
to establish `maxRetention`. Each probe asks for a one-second window and pins
the origin's reference time with `now`, so it is cheap and exact.

Ladders are shared. They originate from `storage-schemas.conf` patterns, so a
deployment with thousands of metrics typically has only a handful of distinct
ladders; a new metric is first *confirmed* against the ladders already known
(about 7 probes) and only discovered from scratch (about 40) if none matches.
In the developer environment, 24 metrics across 4 ladders converge in about 380
probes total, after which probing stops entirely until the TTL expires.

Once a ladder is known, prediction is arithmetic and costs nothing.

### Confidence levels

Every resolved target carries a confidence, exported as the `confidence` label
on `trickster_graphite_resolution_lookups_total`:

| Confidence | Meaning | Behavior |
|---|---|---|
| `exact` | The step was read from an origin response for this metric at this age | Delta cached |
| `derived` | Computed from known ladders — the LCM across a wildcard's leaves, or a step-altering function Trickster understands | Delta cached |
| `configured` | From `static_retentions` only, not yet confirmed by probe | Delta cached, and a confirming probe is scheduled |
| `unknown` | No usable step | Object cached |

A request with several targets reports the weakest confidence among them.

### Verification, and what happens when a prediction is wrong

The step is not merely predicted; it is checked. Every response Trickster
models is compared against the predicted step. A mismatch means the cached
ladder was wrong — a `whisper-resize.py`, a metric moved between schemas — and
Trickster:

1. discards the response rather than caching it under the predicted key,
2. increments `trickster_graphite_step_mispredictions_total` and logs a warning,
3. invalidates the registry generation, so every entry learned under the old
   assumption misses rather than colliding,
4. relearns the affected ladders, and
5. re-serves the request through the object cache, so the client still gets the
   correct answer.

One response shape cannot self-verify: JSON carries no explicit step, so a
fetch that returns fewer than two points is consistent with any prediction.
Trickster never trusts one. A delta fetch that would cover a single bucket —
the tip fetch every steady-state refresh performs — is widened one bucket
into the past, which cannot change the archive whisper selects (the pinned
`now` keeps `now - from` identical) but makes the response two points, and
therefore self-verifying, at no extra request cost. Any fetched response
that still cannot prove its step — one point, or no series where the
prediction promised data inside retention — is refused outright: nothing is
cached, and the request is served through the object cache instead, while
the background learner re-establishes the ladder.

`trickster_graphite_step_mispredictions_total` should be flat at zero. A
non-zero value is not a client-visible error, but it does mean something the
registry believed was wrong.

### Static retentions

`static_retentions` mirrors `storage-schemas.conf`: an ordered list, first
match wins, patterns matched anywhere in the metric path exactly as carbon
applies them.

```yaml
    graphite:
      static_retentions:
        - pattern: '^collectd\.'
          retentions: '10s:6h,1m:7d,10m:5y'
        - pattern: '\.count$'
          retentions: '1m:2d,5m:30d'
        - pattern: '.*'
          retentions: '1m:1d'
```

Retention syntax is Whisper's: `precision:retention` pairs, where each part is
a number with an optional unit (`s`, `m` for minutes, `h`, `d`, `w`, `y`), and a
bare number in the second position is a point count. `1m:7d` is a one-minute
step kept for seven days.

This is a **seed and an override, never the sole source of truth**. A static
match yields `configured` confidence and schedules a confirming probe; when the
probe disagrees with the configuration, the probe wins and the ladder is
relearned. That is deliberate: it means a stale `static_retentions` block
degrades to a few extra probes rather than to incorrect data.

Use it to skip the warmup cost on a known-stable deployment, not as a
substitute for learning.

Changing `static_retentions` invalidates the persisted registry on the next
start, so a corrected block takes effect immediately rather than waiting out
the TTL.

## What is accelerated

A target is delta-cached only if **every** function in it satisfies two
independent properties:

1. **Its step is predictable** — the function does not change the resolution in
   a way Trickster cannot compute.
2. **It is range-decomposable** — each output point is a function of the input
   points at that same timestamp, so the values for `[t1,t2]` are identical
   whether that window was fetched alone or as part of a wider one.

The second property is what delta caching actually requires, and it is stricter
than it first appears. The v1 allowlist is deliberately small:

**Cross-series aggregation** — one output point per input timestamp:
`sumSeries` (`sum`), `averageSeries` (`avg`), `minSeries`, `maxSeries`,
`diffSeries`, `multiplySeries`, `divideSeries`, `divideSeriesLists`,
`stddevSeries`, `rangeOfSeries`, `countSeries`, `percentileOfSeries`,
`aggregate`, `aggregateWithWildcards`, `aggregateSeriesLists`, `group`,
`groupByNode`, `groupByNodes`, `asPercent` (`pct`), `weightedAverage`,
`powSeries`, `unique`, and the `*SeriesLists` and `*WithWildcards` variants.

**Per-point transforms**: `scale`, `scaleToSeconds`, `offset`, `add`, `pow`,
`exp`, `absolute`, `invert`, `squareRoot`, `sigmoid`, `logit`, `log`, `round`,
`transformNull`, `isNonNull`, `removeAboveValue`, `removeBelowValue`,
`consolidateBy`, `setXFilesFactor` (`xFilesFactor`).

**Naming, name-based filtering and cosmetics**: `alias`, `aliasSub`,
`aliasByNode`, `aliasByMetric`, `aliasByTags`, `upper`, `lower`, `substr`,
`exclude`, `grep`, `color`, `alpha`, `lineWidth`, `dashed`, `drawAsInfinite`,
`secondYAxis`, `stacked`, `areaBetween`.

Bare metric paths, wildcards (`*`, `?`, `[a-z]`, `{a,b}`) and pipe syntax are
all accelerated.

### What falls back, and why

Anything not on the list above is served through the object cache. The reason
is recorded on `trickster_graphite_fallbacks_total`:

| `reason` | Cause |
|---|---|
| `function_not_allowlisted` | A function in the target is not on the allowlist |
| `unknown_step` | The step could not be resolved (metric not yet learned, probe failing, or the window is wholly beyond `maxRetention`) |
| `missing_target` | No `target` parameter, or a wildcard that matches nothing |
| `parse_error` | The target expression, `from`/`until`, or `now` did not parse |
| `non_series_format` | An image or pickle format, or `graphType=pie` |
| `multi_target_step_mismatch` | Targets resolve to different steps and could not be split |
| `passthrough_max_data_points` | `passthrough_max_data_points` is on and the request carries `maxDataPoints` |
| `misprediction` | A response contradicted the predicted step |
| `tz_unavailable` | The request names a `tz` whose validity could not be verified within the timezone cold-load budget (a burst of unique hostile `tz` values can spend it); the request is served unaccelerated with the original `tz` forwarded, rather than being reinterpreted in the configured zone |
| `client_identity` | The request carries a result-affecting identity or view selector — an `Authorization` header, a header named in the render path's `cache_key_headers`, or a parameter/form field it names in `cache_key_params`/`cache_key_form_fields` (`local` above all) — that the path's `request_headers`/`request_params` does not statically override, or the render path or backend has a request rewriter — see below |
| `resolution_identity` | The configured path identities are mixed: `/render` and `/metrics/expand` select different `request_headers`/`request_params` for synthetic GETs, or the request's path config carries a different identity than the synthetic one (e.g., `GET` and `POST` `/render` pinned to different credentials) |

Notable functions that are **not** accelerated, and why:

- **Time-windowed**: `movingAverage`, `movingSum`, `movingMin`, `movingMax`,
  `movingMedian`, `exponentialMovingAverage`, `derivative`,
  `nonNegativeDerivative`, `perSecond`, `integral`, `stdev`,
  `holtWinters*`, `linearRegression`. Each output point depends on input points
  *before* it, so a cached range cannot be reused at its edges.
- **Whole-range selecting or ranking**: `highest*`, `lowest*`, `sortBy*`,
  `limit`, `mostDeviant`, `*Percentile`, `currentAbove`/`Below`,
  `averageAbove`/`Below`, `maximumAbove`/`Below`, `filterSeries`. Which series
  come back depends on the entire requested range.
- **Bucketing**: `summarize`, `hitcount`, `smartSummarize`. These look
  decomposable — with `alignToFrom=false`, `summarize`'s buckets align to
  absolute interval boundaries — but the bucket covering either *edge* of the
  requested window is summarized from only the points inside that window.
  Measured on graphite-web 1.1.10, the same absolute one-hour bucket reported
  `134885.899`, `302921.880` and `307388.038` over three different windows.
  Caching such a value and reusing it for a different window would return data
  the origin never produced, so these take the object lane.
- **Time-shifting**: `timeShift`, `timeStack`, `timeSlice`, `delay`.
- **Generators and tag queries**: `constantLine`, `threshold`, `timeFunction`,
  `randomWalk`, `sinFunction`, `identity`, `seriesByTag`, `groupByTags`,
  `events`, and `template()`.

The object lane is still a cache: the whole response is cached by request URL
with a TTL, keyed on every render parameter. It is always correct because it
makes no claim about the response's internal structure.

### Multiple targets

Grafana sends every target of a panel in one `/render` call. Trickster splits a
multi-target request into one delta-cached fetch per target and merges the
results in target order, so targets on *different* ladders are still
accelerated. If any single target cannot be accelerated, the whole request is
served unaccelerated, because consolidation at the origin spans all series in
the response and must be applied consistently.

## maxDataPoints and consolidation

Grafana sends `maxDataPoints` on every panel, sized to the panel's pixel width.
Two panels of different widths showing the same query would otherwise be two
different cache entries.

Trickster **strips `maxDataPoints` from upstream requests**, caches at the
origin's native resolution, and applies consolidation when rendering the
response to each client. One cached series therefore serves every panel width,
every output format, and `noNullPoints`, `jsonp`, `pretty` and `tz` variations.

The consolidation reproduces graphite-web's `renderViewJson` exactly, including
its start "nudge", its treatment of `xFilesFactor`, the `consolidateBy`
function, and the `maxDataPoints=1` special case — verified byte-for-byte
against a live origin.

Set `passthrough_max_data_points: true` if you need the origin's own
consolidation byte-for-byte instead. Requests carrying `maxDataPoints` are then
served unaccelerated, which for Grafana means most requests, so this largely
disables acceleration. It exists for operators who must guarantee identical
bytes to a pre-existing consumer.

## Sizing

Two of the common backend settings default differently for Graphite, because
Graphite is fetched at its native resolution: `maxDataPoints` is stripped
upstream (see [maxDataPoints](#maxdatapoints-and-consolidation)), so Trickster
buffers and caches every point Whisper holds for the window, not the few
hundred a dashboard draws. The generic defaults are sized for origins that
consolidate before responding.

| Setting | Generic default | Graphite default |
|---|---|---|
| `max_object_size_bytes` | 512 KB | 64 MB |
| `timeseries_retention_factor` | 1024 points | 524288 points |

The Graphite values come from
[pkg/backends/graphite/options/defaults.go](../pkg/backends/graphite/options/defaults.go)
and are applied only where the configuration is silent, so setting either one
in the file always wins.

### `max_object_size_bytes`

The delta cache must buffer an entire response to model it. At native
resolution a wide window on a fine archive is large: in the developer
environment, a 120-day panel over two series on a 5-minute archive is 34,560
points per series and about 1.6 MB of JSON — three times the generic 512 KB
limit.

A response over the limit cannot be delta-cached. Trickster logs
`upstream response exceeded MaxObjectSizeBytes` and serves the request through
the object lane instead, which streams rather than buffers — so clients still
get their data, but that panel is never accelerated. Raise the limit if you
serve panels wider than the 64 MB default holds:

```yaml
    max_object_size_bytes: 134217728   # 128MB
```

### `timeseries_retention_factor`

The cache keeps this many points per series, cropping older ones. The generic
default of 1024 points is only about 3.5 days at a 5-minute step, so a 90-day
dashboard panel would have almost its entire range cropped after each fetch
and refetched on the next — a partial hit that re-fetches nearly everything.
The Graphite default of 524288 covers 5 years at a 5-minute step, or 60 days
at a 10-second one.

Raise it if you serve a wider window at a finer step than that, and lower it
to cap what one series may occupy:

```yaml
    timeseries_retention_factor: 1048576
```

### Cache storage

Trickster does not roll up between rungs: a 10-second cached chunk is never
reused to answer a query that resolves to 60 seconds, because the rollup
Trickster computed would not match what Whisper's lower archive holds (which
depends on the metric's `aggregationMethod` and `xFilesFactor`). A different
step is a different cache key.

The practical implication is that **a metric is cached once per rung it is
queried at**. A dashboard with a 1-hour, a 24-hour and a 30-day view of the
same metric holds three cached series for it, not one. Size the cache for the
distinct `(metric, step)` pairs your dashboards produce, not for the number of
distinct metrics.

## Metrics, logs, and tracing

Graphite-specific metrics, in addition to the standard
`trickster_proxy_requests_total{provider="graphite"}` and `trickster_cache_*`
families:

| Metric | Type | Notes |
|---|---|---|
| `trickster_graphite_resolution_lookups_total{backend_name,confidence,source}` | counter | One per resolved request |
| `trickster_graphite_probes_total{backend_name,kind,result}` | counter | Should spike at start and fall to zero |
| `trickster_graphite_ladders{backend_name}` | gauge | Distinct ladders known; should flatten at a small number |
| `trickster_graphite_registry_entries{backend_name,layer}` | gauge | `leaf`, `ladder`, `target`, `negative` |
| `trickster_graphite_step_mispredictions_total{backend_name}` | counter | **Should be flat at zero** |
| `trickster_graphite_fallbacks_total{backend_name,reason}` | counter | Why requests were not accelerated |

No metric path, target expression or query text ever appears in a label; every
label value comes from a closed set. See [metrics.md](./metrics.md).

Logs: a `Debug` line per resolution decision carrying the target, a bucketed
age, the lane, confidence, source and step (or reason when declined); `Warn` on
every misprediction and on every negative-cache entry.

Traces: `GraphiteExpand` (a child of the request span), `GraphiteProbe`
(kind, result, step) and `GraphiteLearnLadder` (ladder, probe count).

## Operations and troubleshooting

### Everything is falling back

Check `trickster_graphite_fallbacks_total` by `reason`.

- `unknown_step` dominating right after a start is normal; it should fall as
  ladders are learned. If it does not, resolution is failing — look for
  `graphite ladder learning failed; negative-cached` warnings.
- `function_not_allowlisted` means the dashboard uses functions outside the
  allowlist. That is expected and correct, not a defect.
- `non_series_format` means the client is asking for images; those panels
  cannot be delta-cached.

### Probing never quiets down

`trickster_graphite_probes_total` should approach zero after warmup and
`trickster_graphite_ladders` should flatten. If probing continues:

- Metric names may be high-cardinality or ephemeral, so new leaves appear
  constantly. Raise `resolution_registry.max_entries`, or accept the cost.
- The registry may be evicting under `max_entries` pressure and relearning.
  Compare `trickster_graphite_registry_entries{layer="leaf"}` against the
  configured maximum.
- Learning may be failing and retrying under backoff. Check the warnings.

### Step mispredictions are non-zero

Something changed a metric's on-disk ladder — usually `whisper-resize.py`, a
schema edit followed by new file creation, or a metric that moved between
schema patterns. Trickster recovers on its own (it relearns and re-serves the
request unaccelerated), so this is not a client-visible error, but the counter
should return to flat once the new ladders are learned. Persistent
mispredictions on one namespace suggest an origin whose resolution is not a
function of `now - from` at all — a clustered graphite-web fronting stores with
different schemas, for example.

### A panel is never accelerated but should be

- Look for `upstream response exceeded MaxObjectSizeBytes` in the logs and
  raise `max_object_size_bytes`.
- Check whether the panel's window is wholly beyond the metric's
  `maxRetention`; those requests have nothing to cache.
- A single non-allowlisted target in a multi-target panel makes the whole panel
  unaccelerated.

### Repeated partial hits on wide panels

Raise `timeseries_retention_factor`. A partial hit that re-fetches nearly the
whole range on every request is the signature of cropping.

### Verifying correctness against the origin

Because the object lane and the delta lane must both be byte-identical to the
origin, the simplest check is to configure two data sources in Grafana — one
direct to Graphite, one through Trickster — and compare panels. The developer
environment (`docs/developer/environment/`) is set up this way, with a
dashboard whose panels deliberately exercise archive boundaries, the retention
edge, schema drift, non-allowlisted functions and mixed-ladder multi-target
requests.

## Known gaps

- **Fast Forward is not implemented.** Graphite's coarsest-rung behavior and
  its `until > now` clamp make the "one extra instant datapoint" approach
  ill-defined. `fast_forward_disable` is effectively always on.
- **No ALB / Time Series Merge support.** Merging across replicas requires the
  replicas' *ladders* to agree, an invariant not yet established. Graphite
  backends can still be members of non-TSM ALB mechanisms.
- **Non-decomposable functions are not accelerated.** A future release may
  evaluate them inside Trickster over cached native-resolution leaves, which
  would move `movingAverage`, `derivative`, `summarize`, `highest*` and friends
  into the accelerated path.
- **No cross-resolution rollup**, as described under
  [Cache storage](#cache-storage).
- **Tag-based queries (`seriesByTag`, `groupByTags`) are not accelerated.**
  They are proxied and object-cached.
- **Non-ASCII metric names** (graphite-web's `UTF8_METRICS`) are not supported
  by the target parser; such targets fail to parse and take the object lane.
