# GreptimeDB Environment Acceptance

This read-only suite checks the running developer environment and, optionally,
the pgwire and HTTP SQL caches. It does not start containers, seed data, change database
contents, or approve a development phase. Use only the isolated developer
environment: the default credentials and plaintext connections are for it.

## Run

After starting and seeding the developer environment, from the repository root:

```sh
make developer-greptimedb-check
```

To check Trickster, start it with the developer configuration:

```sh
TRICKSTER_GREPTIMEDB_PROXY_ACCEPTANCE=1 make developer-greptimedb-check
```

This additionally writes `proxy-report.json`: pgwire simple/extended/error
recovery, invalid credentials, HTTP SQL/PromQL, provider metrics, and all
twelve SQL dashboard panels compared directly with the same GreptimeDB origin.
Each panel runs on its initial range, repeats it, then requests an overlapping
range. Counter deltas verify the expected object/delta path, a repeated hit,
and a partial hit (or an already warmed hit) for overlapping delta ranges.
SQL rewrite failures fail the check even when a fallback returns correct data.
Every field, row and nanosecond is exact, including panel 6 percentages; the
cross-engine rounding allowance below does not apply to proxy comparisons.
Only HTTP SQL execution duration is excluded from its data comparison.
Provide `GREPTIMEDB_PASSTHROUGH_PG_ADDR` and `GREPTIMEDB_PASSTHROUGH_SQL_UID`
for a second listener/datasource without an authenticator to verify passthrough
as well. Without both, that mode is explicitly UNVERIFIED, not a passed check.

After restarting Prometheus, allow at least four minutes of healthy scrapes
before running the sample-grid check. A startup window with missing samples is
still a failed run; retain that report separately from a later qualified run.

To include HTTP SQL cache comparisons:

```sh
TRICKSTER_GREPTIMEDB_HTTP_ACCEPTANCE=1 make developer-greptimedb-check
```

This writes `http-sql-report.json` and the paired origin/proxy responses for
GET and POST requests. It checks miss, partial hit, hit, empty results, exact
integers above 2^53, timezone identity, alternate formats, and unaligned ranges.
Typed schema, row order and numeric values must agree with the origin; only
execution duration is excluded. Unaligned raw-time bounds and inclusive upper
bounds that cut through a bucket use the original SQL, not delta rewriting.
Authenticated GET object responses are not stored unless the origin marks them
shareable; a repeat miss in that case is the expected HTTP cache policy.

## PromQL Provider And Merge Acceptance

The PromQL suite is separate from the read-only checks above:

```sh
sh hack/greptimedb-check.sh --promql
```

Run from the repository root on Linux with the isolated developer GreptimeDB
running. It uses the public developer `seeder` account to create three uniquely
named `trickster_promql_*` databases, seeds a small exact-value fixture, starts
its own Trickster daemon on reserved ports, then stops that daemon and drops
only the databases it created. It does not reseed or drop `public` tables.
Do not run it against a production or shared database. After a killed process,
inspect its retained config to identify any fixture databases needing cleanup.

Every origin and proxy response is retained in a new `promql-*` directory along
with the generated config and the parent `go-test.jsonl`. The suite checks
GET/POST miss/hit/partial/hit, shifted and 500ms grids, database/header/lookback
isolation, metadata, empty/error responses, URL-over-form precedence, and
two-member merges against the complete dataset. Numeric sample spelling may
differ (`2` versus `2.0`); numeric values and point timestamps must be equal.
Series and metadata sets have no defined order. The shared merger's exact
range-sort advisory is expected separately; other warnings are not discarded.

`count_values` currently loses its numeric grouping label in GreptimeDB's wire
response. The suite preserves that observed origin result through passthrough
and requires TSM to reject it instead of returning an incorrect aggregate.
This is an explicit upstream limitation, not a successful merge case.

## MySQL Provider Acceptance

```sh
sh hack/greptimedb-check.sh --mysql
```

This opt-in suite creates a uniquely named `trickster_mysql_*` table using the
public developer seeder, queries it as a read-only user, and drops that table
after its loopback listener has stopped. Use only an isolated developer database.
The queries compare raw typed results, not rounded floating-point conversions.
They check object and delta misses/hits, an extended-range partial hit, NULL and
case-sensitive label ordering, timestamps with nine fractional digits, integers
above 2^53, error recovery, failed SET, partial buckets, and stateful bypass.
Counters must show the intended cache path; equal results alone are insufficient.
The script retains a unique `go-test.jsonl` even on failure. An unavailable origin
fails explicitly requested acceptance instead of silently skipping it.

## Evidence

The script uses the integration module's Go version and dependencies. Each run
creates a new directory under `integration/greptimedb/reports/` containing:

- `report.json`: individual PASS/FAIL/UNSUPPORTED/UNVERIFIED checks, observed versions,
  timestamps, input hashes and PostgreSQL type/parameter observations.
- `go-test.jsonl`: Go's machine-readable test log, including failures.
- `panel-*.json`, `seed-*.json`, `promql-*.json`: query responses for comparison.

A nonzero exit means the automatic checks failed (or did not finish). A missing
or empty report is not success. Earlier results are never overwritten. Passing
automatic checks does not make UNVERIFIED items pass or finish issue #1150.
UNSUPPORTED records an observed upstream limitation, not a skipped successful
test. Its corresponding capability probe must pass before that label is used.

GreptimeDB v1.2.1 fails Grafana's SQL health check and the comment-only
PostgreSQL query check. The developer environment now pins the official
`nightly-20260923-e91faa9df` image containing the upstream repair. Keep the
old-version failed runs; do not disable assertions or describe a source-built
repair or an official nightly as a tested stable release.

## Assertions

- The Docker lifecycle script's unit tests run with a fake Docker executable,
  not a live daemon. They require Bash on Linux and check that active startup
  seeders finish before shared fixture generation or table reloads. A failed
  wait must prevent all mutations. The real delete/start/reseed lifecycle is
  still a separate, destructive reviewer check.
- Grafana and all three required direct datasource health checks must succeed.
- GreptimeDB and TimescaleDB counts and pickup bounds must match the shared
  `seed-window.env` metadata.
- The six main dashboard panels are read from the checked-in JSON. Both
  datasources use the same fixed 48-hour window ending at UTC midnight on
  `SEED_EPOCH`'s date and a five-minute interval. Compare every field name, type,
  label, value, nanosecond remainder and row order. Plugin-specific metadata
  such as executed SQL and display configuration is intentionally excluded.
- Panel 6's `card_use_rate` alone permits adjacent finite float64 values in
  the range 0-100. PostgreSQL numeric division and DataFusion floating division
  can differ by one rounding step (observed for 37 non-cash trips out of 99).
  The report records the number of such cells and retains both raw responses.
  Larger differences, all other fields, integers and timestamps remain exact.
- Every requested Grafana query ref must have a successful, nonempty result.
  Embedded query errors, missing refs, ragged columns and all-null data fail
  even when the HTTP response is 200. JSON integers retain their precision.
- `up{job="prometheus"}` must contain the same nine healthy samples on a 15-second
  grid through both Prometheus datasources. Its separately recorded window
  ends one minute before the run starts. Allow at least four minutes of healthy
  scraping and remote write before starting the suite; it does not retry an
  empty response until it happens to pass.
- Direct PostgreSQL simple/extended queries and timestamp 0/3/6/9, DATE,
  integer and float OIDs/text are checked. Startup observations come from
  **pgconn, not captured Grafana traffic**. The observed PG text renderer uses
  six fractional digits even for `TIMESTAMP(9)`; this is not evidence of
  end-to-end nanosecond preservation. A provider must match the upstream wire
  contract instead of inferring it from the storage type.
- Direct HTTP SQL and MySQL ping/query must preserve a BIGINT above 2^53.
- PostgreSQL and MySQL bucket queries are compared with raw `pickup_epoch`
  rows grouped independently in Go. The suite checks `extract`, `date_part`,
  `date_bin` with interval/compact widths, and `::interval` casts. Both the
  original window and a window shortened by 37 seconds at each end must use
  the same epoch-aligned five-minute grid and the correct counts. Each SQL
  form and its result are retained, including failures.
- MySQL requires an explicit interval unit: `INTERVAL '5' MINUTE`. Its rejection
  of the PostgreSQL spelling `INTERVAL '5 minutes'` must be a server syntax
  error, not a timeout or lost connection. Literal/identifier quoting is also
  checked separately for each protocol.
- PostgreSQL startup currently supplies zero cancellation credentials, matching
  the upstream no-op cancellation handler. `BEGIN`/`ROLLBACK` are compatibility
  stubs with an explicit no-transactions warning. The suite checks the observed
  `T`, `T`, `E`, `I`, `I` status sequence and query values before/after an error;
  it does not claim storage transactions or isolation. Keep cancellation and
  transaction support disabled until upstream behavior is reevaluated.

## Grafana Wire Evidence

The typed query also runs through Grafana's actual bundled PostgreSQL plugin.
`grafana-type-query.json` is an API response, not a protocol capture. A separate
capture must observe Grafana connecting directly to GreptimeDB without a shim.
Capture a fresh connection and health check, then run this suite. Check startup
parameters, all server ParameterStatus messages, query mode, RowDescription
OIDs/formats, and the typed row's original text. Do not substitute the pgconn
probe for this evidence or infer nanosecond preservation from API timestamps.

Scope the capture to the isolated Grafana peer and PostgreSQL port. Export
selected protocol fields only; do not retain passwords, authentication data,
cancellation secrets or raw PCAP files. Save the capture and its independent
review alongside the report. The suite's own UNVERIFIED entry intentionally
remains unchanged because it neither performs nor validates that capture.

## Overrides

| Variable | Default | Purpose |
| --- | --- | --- |
| `GO` | `go` | Go executable |
| `GRAFANA_URL` | `http://127.0.0.1:3000` | Anonymous developer Grafana API |
| `GREPTIMEDB_PG_ADDR` | `127.0.0.1:4003` | PostgreSQL address |
| `GREPTIMEDB_MYSQL_ADDR` | `127.0.0.1:4002` | MySQL address |
| `GREPTIMEDB_HTTP_URL` | `http://127.0.0.1:4000` | HTTP SQL base URL |
| `GREPTIMEDB_PROXY_HTTP_URL` | `http://127.0.0.1:8480/greptimedb1` | Trickster HTTP base URL |
| `GREPTIMEDB_DATABASE` | `public` | Database |
| `GREPTIMEDB_USER` | `grafana_ro` | Read-only developer account |
| `GREPTIMEDB_PASSWORD` | `trickster-dev-grafana` | Developer password; not written to the report |
| `GREPTIMEDB_SQL_UID` | `ds_greptimedb_direct` | Grafana SQL datasource UID |
| `GREPTIMEDB_PROM_UID` | `ds_greptimedb_prom_direct` | Grafana Prometheus datasource UID |
| `GREPTIMEDB_REPORT_ROOT` | `integration/greptimedb/reports` | Parent for unique run directories |
| `GREPTIMEDB_BUILD_NOTE` | unset | Operator-supplied image/source identification, not independently attested |

For remote Docker validation, run the same command inside the remote checkout
or Go container and set the addresses to that environment. Never expose a
developer database publicly to make these checks reachable.

Ordinary integration test targets run the negative/unit cases without reaching
external services. To run them alone:

```sh
cd integration
go test -count=1 ./greptimedb
go test -race -count=1 ./greptimedb
```

## Human Review

1. Read `report.json` and any FAIL details alongside `go-test.jsonl`. Check the
   observed server version and build note before comparing separate runs.
2. Open the GreptimeDB and TimescaleDB dashboards at exactly `sql_from` through
   `sql_to`, using the direct datasources. Inspect all six data panels on desktop
   and mobile. An HTTP/API assertion is not a rendering assertion.
3. Open the existing Prometheus dashboard using `greptimedb-prom-direct`.
   This is distinct from the GreptimeDB dashboard's Trickster performance
   panels, which may remain empty until the later provider/cache phases.
4. Review the UNVERIFIED list against the issue checklist. Actual Grafana wire
   capture and complete reseed coverage need separate evidence. Check the
   capability probes before accepting UNSUPPORTED entries.
5. Record the human decision and evidence separately. This suite does not
   approve its own results, establish correctness beyond the tested cases, or submit a PR.
