# PostgreSQL / TimescaleDB Compatibility Corpus

`v1.json` is the executable compatibility specification for the PostgreSQL
SQL analyzer (`pkg/backends/postgres`). A corpus version is immutable once
released; incompatible schema or expectation changes require a new versioned
file. `compatibility_test.go` runs it.

Cases with a `macro_source` record the exact SQL Grafana's PostgreSQL data
source sent after macro expansion, captured from `executedQueryString` with
the `grafana_version` named in the case, over a deliberately unaligned,
millisecond-precision range. The remaining cases are production-shaped
statements: every supported bucket form and width, the bound-semantics
matrix, qualified/quoted/aliased identifiers, comments, fail-closed time zone
cases, adversarial boolean expressions, statements the parser cannot re-spell
faithfully, and volatile statements.

Analysis depends on the session: `session_time_zone` selects the rules for a
UTC session (`UTC`) or for any other zone. `date_trunc` and zone-less bound
literals qualify for the delta cache only under UTC.

Every delta case records its cadence, phase, units, bounds, output column and
group columns. The test also renders one extent and checks that the rendered
statement has no placeholders, keeps the same canonical identity, and reads
back as exactly the extent that was asked for.

The documented minimum `$__interval` is **1 minute**.

Recorded omissions:

- `builder-mode-captures`: Builder mode emits the same macros as Code mode,
  so paired captures are not kept.
- `extended-protocol-statements`: statements sent with Parse/Bind/Execute are
  relayed and never analyzed, so they have no classification to record.
