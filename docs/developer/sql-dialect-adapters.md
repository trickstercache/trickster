# Adding a SQL Dialect Adapter

Trickster accelerates SQL-based time series backends (currently ClickHouse and
MySQL) by
parsing each query into a dialect-native abstract syntax tree, analyzing it
for delta-cache eligibility, and rendering cache-miss origin requests from an
immutable query plan. This document describes the architecture and the
requirements a new SQL dialect adapter must satisfy. The ClickHouse and MySQL
implementations in `pkg/backends/clickhouse` and `pkg/backends/mysql` are the
references for HTTP and native-protocol backends, respectively.

The initial MySQL compatibility and safety boundaries are normative in
[`mysql-release-contract.md`](mysql-release-contract.md). Changes that broaden
MySQL protocol, authentication, TLS, session-state, caching, or routing behavior
must update that contract and its test evidence.

## The Parser–Adapter–Plan–Renderer Lifecycle

```text
Dialect SQL -> dialect AST parser -> dialect adapter (analysis)
                                          |
                                          v
                              sqlanalyzer.QueryPlan  ->  delta cache engine
                                          |
                                          v
                              ExtentRenderer -> origin cache-miss SQL
```

1. **Parse.** The adapter parses the complete statement using a mature,
   dialect-specific AST parser. Trickster does not maintain a universal SQL
   grammar; each dialect owns its parser dependency.
2. **Analyze.** The adapter implements `sqlanalyzer.DialectAnalyzer`,
   inspecting the AST to determine cache eligibility, bucket cadence and
   phase, timestamp columns and units, time bounds, and grouping tags. The
   result is a `sqlanalyzer.Analysis` containing a cache mode, a stable
   reason code, and (for delta-eligible queries) a `QueryPlan`.
3. **Plan.** The `QueryPlan` carries only database-independent facts. The
   caching engine consumes the plan without knowing the dialect; vendor AST
   types must never appear in `pkg/parsing/sqlanalyzer` or any shared
   package.
4. **Render.** The plan embeds an `ExtentRenderer` that produces
   dialect-valid origin SQL for any cache-miss extent, preserving the
   original boolean topology and every non-time predicate.

All dialect-specific knowledge — the parser, the AST types, bucket-expression
matchers, bound evaluation, and rendering — lives inside the backend package.
The shared contract is intentionally small.

## The Shared Contract

Defined in `pkg/parsing/sqlanalyzer`:

- `DialectAnalyzer.Analyze(statement string, now time.Time) Analysis` — the
  single entry point. It must never panic on arbitrary input and must never
  return an approximate plan: valid but unverifiable SQL fails closed.
- `Analysis` — `Mode` (`CacheModeNone`, `CacheModeObject`, `CacheModeDelta`),
  `Reason` (a stable, low-cardinality `AnalysisReason`), `Plan`, and `Err`.
- `QueryPlan` — canonical SQL, time and output columns, step, phase, input
  and output timestamp units, lower and upper `Bound`s (value plus
  inclusivity), group columns, output format, and the embedded renderer.
- `ExtentRenderer.RenderExtent(extent timeseries.Extent) (string, error)`.

Rendering is an embedded interface value on the plan rather than a method on
`DialectAnalyzer` so that a plan is self-contained: the engine can hold and
render a plan long after analysis without retaining the analyzer, and each
plan's renderer can privately close over its own immutable template state.

`timeseries.Extent` values passed to `RenderExtent` follow Trickster's
internal extent convention: `Start` and `End` are the first and final
*included* bucket timestamps. An adapter whose SQL uses an exclusive upper
bound must add one step during rendering (and subtract one step during
analysis). Document the convention handling in the adapter.

## Eligibility and Reason-Code Expectations

Analysis must distinguish, at minimum: invalid SQL; valid but unsupported
statement types; valid queries that are not time range queries; unsupported
or dynamic bucket expressions; unsafe or ambiguous time predicates
(including any `OR`/`NOT` topology touching the time axis); ambiguous or
multiple time axes; unsupported grouping or result shapes; and successful
delta-cache plans. Use the existing `AnalysisReason` constants where they
apply; add new constants sparingly and keep them low-cardinality — they are
metric label values.

Fail-closed rules that apply to every dialect:

- Predicates on the **raw timestamp column** use an inclusive lower bound and
  an exclusive upper bound. Aligned bounds describe complete buckets directly.
  An adapter may accelerate unaligned half-open ranges by rounding the lower
  bound up and the upper bound down to the query cadence when the client
  consumes only complete buckets, or by proving equivalent partial-edge
  handling. When no complete bucket remains, both bounds normalize to the
  rounded-up lower boundary. Strict lower bounds, inclusive upper bounds, and
  `BETWEEN` remain object-cache fallbacks unless an adapter proves equivalent
  handling.
- Predicates on the **bucket output** are discrete and may be normalized
  from any comparator to the first and last included buckets.
- A query that cannot be delta-cached should remain object-cacheable
  whenever it is a well-formed read query.

## Canonical Identity Requirements

The delta cache key must not vary with the requested time range:

- Derive canonical SQL from the formatted AST with the time-bound value
  nodes replaced by fixed placeholders (`<$TS1$>` lower, `<$TS2$>` upper).
- Preserve every other result-affecting input — non-time predicates,
  settings, database qualifiers, formats — in the canonical statement.
- Assign `CacheKeyElements["query"]` (via `QueryPlan.CanonicalSQL`) only
  after normalization, never from the original ranged statement.
- Extract Trickster comment directives from the original statement text
  before canonicalization; formatters may drop or relocate comments.
- Formatting-only differences intentionally converge to one key; two
  requests differing only in whitespace or time range must share a cache
  entry, and any change to a non-time predicate must produce a new key.
  Cover both properties with a test through `DeriveCacheKey`.

## Renderer Immutability and Concurrency Requirements

Plans are cached and shared; a single plan may render many extents
concurrently (partial hits, sharded requests). The renderer must therefore
be immutable after analysis:

- The ClickHouse pattern: during analysis, mutate the AST **once** to insert
  private, collision-resistant placeholder tokens, format it into a template
  string, and render by substituting bound expressions into that immutable
  template. Rendering never touches the AST again.
- Placeholder tokens must be provably absent from the user's statement
  (probe and re-generate on collision) so substitution can never modify
  user literals.
- Alternatively, an adapter may clone or reparse a private AST per render;
  measure the cost before choosing this.
- Required tests: a collision test (user literals containing placeholder
  text survive rendering) and a concurrency test (many goroutines rendering
  distinct extents from one plan produce correct, independent output).

Rewrite failures must be observable: increment
`trickster_sql_query_rewrite_failures_total` with a fixed reason category,
and never silently send an un-rewritten (original-range) query to the
origin on a cache miss.

## Parser Selection and Pinning Requirements

- Choose a maintained, dialect-native AST parser with typed nodes,
  traversal, mutation, and SQL rendering. Do not extend a generic
  cross-dialect grammar.
- Keep all parser types private to the backend package.
- Pin an exact version in go.mod and vendor it. Upgrades must re-run the
  compatibility corpus and the performance and license review.
- Verify parse -> format -> parse structural stability over the supported
  corpus before adoption.
- Record the selection in a bakeoff/acceptance document covering: supported
  and unsupported syntax, known parser gaps and their handling (upstream
  contribution, local shim, scoped fork, or remaining unsupported),
  supported server versions, latency and allocation benchmarks against the
  prior implementation, and binary-size impact. The ClickHouse record is the
  template.

## Compatibility-Corpus Requirements

Maintain a corpus of statements with expected classifications (delta, OPC,
none) and, for delta-eligible entries, expected plan facts. The ClickHouse
reference corpus is maintained in
`pkg/parsing/sqlanalyzer/aftership/compatibility_corpus_test.go` and runs against the
AfterShip parser version pinned in `go.mod`.

Include:

- Every supported bucket-expression form and unit.
- Every comparator on both raw-column and bucket-alias predicates, at
  full-miss, partial-hit, and shard boundaries (a bound-semantics matrix).
- Qualified, quoted, and aliased identifiers; subqueries and CTEs as
  applicable; dialect comment forms and Trickster directives.
- Timezone-aware expressions with proven fail-closed classification.
- Representative dashboard-generated (e.g., Grafana) and production-shaped
  queries.
- Regression cases for known defects of prior implementations (for
  ClickHouse: OR-rewriting, comparator loss, cross-predicate BETWEEN state,
  global string replacement, ranged cache keys, token-based GROUP BY).
- Deliberately adversarial boolean expressions that must fail closed.

## Observability Requirements

Every adapter must emit, without logging query text by default:

- `trickster_sql_query_analysis_total{backend_name, dialect, cache_mode,
  reason}` on every analysis.
- `trickster_sql_query_rewrite_failures_total{backend_name, dialect,
  reason}` on every failed extent render.
- Debug-level structured logs using the same reason codes.

## Licensing and Dependency-Review Requirements

Before adopting or upgrading a parser dependency:

1. Confirm the license is compatible with Apache-2.0 distribution (ASF
   Category A, e.g. MIT, BSD, Apache-2.0).
2. Review the complete **production** dependency closure
   (`go list -deps <pkg>`); test-only module requirements do not ship, but
   any new transitive production dependency needs its own review.
3. Scan the vendored sources for divergent copyright headers or bundled
   third-party code.
4. Retain the dependency's LICENSE file in the vendor tree (the vendored
   module tree is Trickster's third-party license inventory).
5. Determine whether NOTICE requires an entry (MIT and BSD generally do
   not; Apache-2.0 dependencies with their own NOTICE files do).
6. Record the review in the dialect's parser-selection record so it can be
   repeated on upgrade.

## Rollout and Rollback Requirements

New dialect adapters (and parser replacements within a dialect) follow the
rollout policy proven by the ClickHouse migration:

- The AST implementation becomes authoritative at merge; no in-process
  legacy parser toggle is maintained.
- CI, the compatibility corpus, integration coverage, and benchmarks are
  the acceptance gate.
- Rollback is by reverting the migration merge from the release branch and
  publishing a corrective patch release. Preserve the development branch,
  fixtures, and benchmark evidence for diagnosis.
- The analysis and rewrite-failure metrics are the operational signals for
  deciding whether a rollback is required.
