# Integration Tests

End-to-end tests that boot real Trickster instances against the Docker Compose
developer environment (Prometheus, ClickHouse, InfluxDB, Mockster, Redis).

All Trickster capabilities should be covered by at least one integration test, but the suite is not expected to be exhaustive. The focus is on testing real-world scenarios and edge cases that are difficult to simulate with unit tests, rather than achieving 100% code coverage. Tests should be added as new features are developed, and existing tests should be updated as needed to cover changes in functionality or to aid in resolving bugs or preventing regressions.

## Prerequisites

```sh
make developer-start developer-seed-data  # from repo root — starts Docker Compose env
```

## Running

```sh
cd integration
make test              # full suite, fail-fast
make data-race-test    # full suite with -race
go test -run TestALB   # single test
```

## Port assignments

Each top-level test boots its own Trickster instance on a unique port range to
avoid TCP TIME_WAIT races between sequential tests. Tests that need the full
developer config use `writeTestConfig()` to clone it with swapped ports.

## Structure

- `main_test.go` — `TestMain`, shared helpers (`startTrickster`, `waitFor*`,
  `queryTricksterProm`, `parseTricksterResult`)
- `harness_test.go` — `tricksterHarness` boot helper, option-based HTTP client
  (`do`, `queryProm`, `withParams`, `withHeader`, `withBody`),
  `requireTricksterResult`, `runCacheProviderMatrix`, `writeTestConfig`
- `testdata/` — static YAML configs for tests that need custom backends
  (ALB, rewriter, engines, rule, auth, purge, reload, TLS)

## Test guidelines

- Tests should be self-contained and independent, with no shared state or reliance on execution order.
- Tests should be deterministic and repeatable, avoiding reliance on external factors or timing.
- Tests should use unique query expressions when sharing a Trickster boot across subtests to avoid OPC cache collisions.
- Tests should be focused on specific features or scenarios, rather than trying to cover multiple features in a single test.
- Follow the projects coding style and conventions, and ensure that tests are well-documented and easy to understand.

## Adding a new test

1. Pick a free port triple (frontend, metrics, mgmt) — check the existing tests.
2. If you only need standard backends (prom, clickhouse, etc.), use
   `writeTestConfig(t, front, metrics, mgmt)` to clone the developer config.
3. If you need custom backends (ALB pools, rule routing, etc.), add a static
   YAML under `testdata/configs/`.
4. Use `tricksterHarness{ConfigPath, BaseAddr, MetricsAddr}.start(t)` to boot.
5. Use unique query expressions (`fmt.Sprintf("up + 0*%d", time.Now().UnixNano())`)
   when sharing a Trickster boot across subtests to avoid OPC cache collisions.
