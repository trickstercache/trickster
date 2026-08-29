# Flight SQL Cache Tier Benchmarks

This document records the per-request serving cost of the Flight SQL
statement cache tiers and the analysis of when the delta tier's response
rebuild is justified. Run benchmarks on an otherwise idle machine, record the
CPU, operating system, Go version, and commit, and keep the raw output with
the pull request or release evidence.

## Commands

The deterministic single-iteration smoke case, suitable for CI:

```sh
make benchmark-flightsql-smoke
```

The full tier comparison, five runs:

```sh
make benchmark-flightsql
```

## What is measured

`BenchmarkFlightSQLStatementTiers` serves one statement per iteration through
`DoGetStatement` against a warm cache and a fake in-process upstream, so
upstream execution and network time are excluded. Row counts are minute
buckets x 2 tag series.

- `delta_hit` — a full delta-tier cache hit: the cached dataset is cropped to
  the request window and rebuilt into Arrow record batches conforming to the
  response's original schema. This is the price of extent-based caching.
- `object_hit` — a full object-tier cache hit: the cached Arrow IPC bytes are
  streamed back verbatim.
- `passthrough` — an uncacheable (nondeterministic) statement re-fetched from
  the fake upstream on every request. Real deployments add the origin's query
  execution and network transfer on top of this number.

## Recorded results

Apple M3 Max, macOS (darwin/arm64), Go 1.27, `-benchtime=200ms`, 2026-08-29,
`influx-3` branch:

| rows   | delta_hit             | object_hit           | passthrough (fake origin) |
| ------ | --------------------- | -------------------- | ------------------------- |
| 100    | 76 µs, 101 KiB, 945 allocs | 8.3 µs, 9 KiB, 83 | 10.6 µs, 10 KiB, 101   |
| 1,000  | 269 µs, 664 KiB, 5,465     | 11.6 µs, 29 KiB, 83 | 13.6 µs, 30 KiB, 101 |
| 10,000 | 2.05 ms, 6.5 MiB, 50,627   | 26.4 µs, 220 KiB, 83 | 28.9 µs, 221 KiB, 101 |

## Analysis and crossover

The object tier is effectively a buffer copy: constant ~83 allocations and
single-digit-to-tens of microseconds regardless of size. The delta tier's
rebuild costs roughly **200 ns and ~650 bytes of transient allocation per
row**, scaling linearly (76 µs at 100 rows to 2 ms at 10,000 rows).

The delta rebuild is justified whenever it replaces an upstream fetch that
would cost more than the rebuild. Against a real InfluxDB 3 origin, a
`date_bin` aggregation over raw data plus gRPC transfer costs milliseconds to
seconds — far above the rebuild cost at any realistic dashboard size. The
crossover where the object tier is genuinely cheaper only exists when both
responses are already cached and fresh, and there the tiers are not
alternatives: the object tier requires an exact statement match within its
short TTL (`flight_cache_ttl`, default 60s), while the delta tier serves any
overlapping window, keeps history warm for the backend's `timeseries_ttl`,
and refetches only missing sub-ranges. The delta tier trades microseconds of
CPU per hit for eliminating origin round trips on the sliding-window query
patterns dashboards actually emit.

Practical guidance:

- Responses above roughly 100k rows pay ~20 ms of rebuild per hit; if such
  queries repeat with *identical* text inside the object TTL, the object tier
  already absorbs them, and raising `flight_cache_ttl` is cheaper than
  disabling anything.
- The per-row allocation cost is the dataset-to-Arrow rebuild
  (`pkg/timeseries/dataset/arrow`); batch-builder reuse there is the first
  target if profile pressure ever shows up.
