# Integration Tests

End-to-end tests that boot real Trickster instances against the Docker Compose
developer environment (Prometheus, ClickHouse, InfluxDB, Mockster, Redis).

The MySQL matrix uses the pinned MySQL 8.4 and Grafana containers. It validates
the maintained Go `database/sql` driver, the MySQL command-line client,
Grafana's built-in MySQL datasource, direct/OPC/DPC result agreement, reset and
authentication behavior, and a two-target native User Router. Set
`TRICKSTER_MYSQL_CLI_TEST=1` to include the CLI case; CI always enables it.

All Trickster capabilities should be covered by at least one integration test, but the suite is not expected to be exhaustive. The focus is on testing real-world scenarios and edge cases that are difficult to simulate with unit tests, rather than achieving 100% code coverage. Tests should be added as new features are developed, and existing tests should be updated as needed to cover changes in functionality or to aid in resolving bugs or preventing regressions.

## Prerequisites

```sh
make integration-start developer-seed-data  # from repo root — starts Docker Compose env
```

`integration-start` is `developer-start` plus the integration-only
containers (currently CoreDNS for the ALB autodiscovery DNS tests), which
live commented out below the `-- INTEGRATION CONTAINERS BELOW --` marker in
the compose file so developer workstations never run them. The target
uncomments them and seeds the mutable CoreDNS zone directory before
compose-up; `make integration-stop` stops the environment and comments them
back out. `make developer-start` alone still works: the autodiscovery DNS
tests probe for CoreDNS and skip when it isn't running (CI sets
`TRICKSTER_DNS_TEST=1` to turn that skip into a failure).

The Kubernetes autodiscovery scenario (`TestALBDiscoveryKind`) is separate
from compose entirely: it needs a kind cluster prepared via
`make kind-integration-start` (see `kind/README.md`) and only runs when
`TRICKSTER_KIND_TEST=1` is set. The autodiscovery soak
(`TestALBDiscoverySoak`) runs only when `TRICKSTER_SOAK_TEST=1` is set and
is exercised by the nightly workflow.

## Running

```sh
cd integration
make test              # full suite, fail-fast
make data-race-test    # full suite with -race
go test -run TestALB   # single test
TRICKSTER_MYSQL_CLI_TEST=1 go test -run TestMySQLRealServer -v
```

## Port assignments

Each top-level test boots its own Trickster instance on a unique port range to
avoid TCP TIME_WAIT races between sequential tests. Tests that need the full
developer config use `configHarness()` to clone it with swapped ports. The
helper reserves random frontend, metrics, management, and MySQL listener ports
until immediately before Trickster starts. Their addresses are exposed on the
returned harness.

## Structure

- `main_test.go` — `TestMain`, shared helpers (`startTrickster`, `waitFor*`,
  `queryTricksterProm`, `parseTricksterResult`)
- `harness_test.go` — `tricksterHarness` boot helper, option-based HTTP client
  (`do`, `queryProm`, `withParams`, `withHeader`, `withBody`),
  `requireTricksterResult`, `runCacheProviderMatrix`, `configHarness`,
  `staticConfigHarness`
- `testdata/` — static YAML configs for tests that need custom backends
  (ALB, rewriter, engines, rule, auth, purge, reload, TLS)

## Test guidelines

- Tests should be self-contained and independent, with no shared state or reliance on execution order.
- Tests should be deterministic and repeatable, avoiding reliance on external factors or timing.
- Tests should use unique query expressions when sharing a Trickster boot across subtests to avoid OPC cache collisions.
- Tests should be focused on specific features or scenarios, rather than trying to cover multiple features in a single test.
- Follow the projects coding style and conventions, and ensure that tests are well-documented and easy to understand.

## Adding a new test

1. If you only need standard backends (prom, clickhouse, etc.), use
   `configHarness(t)` to clone the developer config and reserve all four of its
   listener ports.
2. If you need custom backends (ALB pools, rule routing, etc.), add a static
   YAML under `testdata/configs/` and load it with `staticConfigHarness(t,
   path)` so its listener ports are reserved.
3. Call the returned harness's `start(t)` method to boot Trickster.
4. Use unique query expressions (`fmt.Sprintf("up + 0*%d", time.Now().UnixNano())`)
   when sharing a Trickster boot across subtests to avoid OPC cache collisions.
