# MySQL Performance Acceptance

This document defines the repeatable performance record for the native MySQL
backend. Run benchmarks on an otherwise idle machine with a fixed power mode,
record the exact CPU, operating system, Go version, commit, and `GOMAXPROCS`,
and keep the raw output with the pull request or release evidence.

## Acceptance gates

The initial-release gates are frozen in `mysql-release-contract.md`:

- every compatibility-corpus analyzer case must be at most 250 microseconds
  and 64 KiB allocated per operation;
- full-extent and partial-hit rendering must be at most 25 microseconds and
  16 KiB allocated per operation;
- an in-memory OPC hit may add at most 1 millisecond p95 over direct result
  encoding and at most 1.5 times the encoded result size in transient memory;
- proxy-only and cache-miss paths may add at most 2 milliseconds p95 or 10%
  over the direct origin path, whichever is larger;
- a retained cache result may use the encoded payload plus 25% and 1 MiB of
  fixed overhead; and
- the stripped Trickster binary may grow by at most 35 MiB relative to the
  designated pre-Vitess baseline.

`make benchmark-mysql-acceptance` enforces the corpus analyzer and renderer
limits. The protocol, memory, and binary comparisons depend on the benchmark
host and designated baseline, so their raw comparative results are required
release evidence.

## Commands

CI compiles the complete benchmark package and runs the deterministic smoke
case once:

```sh
make benchmark-mysql-smoke
```

Run the complete benchmark set five times and preserve its output:

```sh
mkdir -p artifacts
go version | tee artifacts/mysql-benchmark-environment.txt
go env GOOS GOARCH GOMAXPROCS | tee -a artifacts/mysql-benchmark-environment.txt
git rev-parse HEAD | tee -a artifacts/mysql-benchmark-environment.txt
make benchmark-mysql | tee artifacts/mysql-benchmark.txt
make benchmark-mysql-acceptance | tee artifacts/mysql-acceptance.txt
```

The suite reports nanoseconds, bytes, and allocations for compatibility-corpus
analysis, parse-format-parse validation, full and partial rendering, concurrent
rendering, result and envelope codecs, merge/crop/sort, retention, sharding,
handshake/authentication, streaming proxy, OPC, DPC, and User Router lookup.
Result handling uses 10, 100, 1,000, and 10,000 mixed numeric, string, NULL,
datetime, and binary rows. Concurrent sessions additionally report
`p95-ns/session`.

For allocation and retained-heap profiles:

```sh
go test ./pkg/backends/mysql -run '^$' \
  -bench 'BenchmarkMySQL(ResultHandling|LargeResultRetention|Protocol)$' \
  -benchmem -benchtime=2s -count=5 \
  -memprofile artifacts/mysql.mem.pprof \
  -cpuprofile artifacts/mysql.cpu.pprof
go tool pprof -top artifacts/mysql.mem.pprof \
  > artifacts/mysql-memory-top.txt
go tool pprof -top artifacts/mysql.cpu.pprof \
  > artifacts/mysql-cpu-top.txt
```

Run the loopback protocol benchmarks once with `-race` and a short fixed count
when checking connection, goroutine, or buffer retention:

```sh
go test -race ./pkg/backends/mysql -run '^$' \
  -bench 'BenchmarkMySQL(Protocol|ConcurrentSessions)$' \
  -benchtime=20x -count=1
```

The normal MySQL lifecycle tests must also pass after the load run; they assert
that active sessions and upstream connections return to zero after shutdown.
Configured result-row, result-byte, and cache-object limits are covered by unit
tests, and the 10,000-row envelope test enforces the retained-memory contract.

## Binary, dependency, and startup comparison

Choose `MYSQL_BASELINE_REF` as the last comparable commit before Vitess entered
the release artifact. Build both revisions with identical reproducible flags:

```sh
export MYSQL_BASELINE_REF=<pre-vitess-commit>
export MYSQL_BASELINE_DIR=/tmp/trickster-mysql-baseline
git worktree add "$MYSQL_BASELINE_DIR" "$MYSQL_BASELINE_REF"
go build -trimpath -ldflags='-s -w -buildid=' \
  -o artifacts/trickster-with-vitess ./cmd/trickster
(cd "$MYSQL_BASELINE_DIR" && go build -trimpath -ldflags='-s -w -buildid=' \
  -o /tmp/trickster-without-vitess ./cmd/trickster)
wc -c artifacts/trickster-with-vitess /tmp/trickster-without-vitess \
  | tee artifacts/mysql-binary-size.txt
```

Record dependency contribution and cold-start maximum resident memory:

```sh
go list -deps ./cmd/trickster | sort > artifacts/deps-with-vitess.txt
(cd "$MYSQL_BASELINE_DIR" && go list -deps ./cmd/trickster | sort) \
  > artifacts/deps-without-vitess.txt
comm -13 artifacts/deps-without-vitess.txt artifacts/deps-with-vitess.txt \
  > artifacts/deps-vitess-added.txt
/usr/bin/time -l artifacts/trickster-with-vitess -version \
  2> artifacts/startup-with-vitess.txt
/usr/bin/time -l /tmp/trickster-without-vitess -version \
  2> artifacts/startup-without-vitess.txt
```

Remove the temporary worktree after the evidence has been captured.

## Result record and waivers

The pull request or release record must include this table and links to the raw
files:

| Gate | Baseline | Candidate | Limit | Result |
| --- | ---: | ---: | ---: | --- |
| Analyzer worst case | | | 250 µs / 64 KiB | |
| Renderer worst case | | | 25 µs / 16 KiB | |
| OPC-hit p95 overhead | | | 1 ms / 1.5x bytes | |
| Proxy/miss p95 overhead | | | 2 ms or 10% | |
| Retained cache overhead | | | payload +25% +1 MiB | |
| Stripped binary delta | | | 35 MiB | |
| Startup maximum RSS delta | | | recorded | |
| Added dependencies | | | recorded | |

A failed or missing gate blocks acceptance. A maintainer may override it only
with a checked-in or pull-request waiver naming the failed measurement, raw
evidence, user impact, chosen tradeoff, owner, and removal milestone.
