# Integration Tests

End-to-end tests that boot real Trickster instances against the Docker Compose
developer environment (Prometheus, ClickHouse, InfluxDB, devorigin, Redis).

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

`integration-start` seeds the mutable CoreDNS zone directory, then runs
`developer-start` with the `integration` compose profile: every TSDB backend
plus the integration-only containers (currently CoreDNS for the ALB
autodiscovery DNS tests), which are in no other profile so developer
workstations never run them. It also waits for the database seeders to
finish. `make integration-stop` stops the environment. `make developer-start`
alone still works: the autodiscovery DNS tests probe for CoreDNS and skip when
it isn't running (CI sets `TRICKSTER_DNS_TEST=1` to turn that skip into a
failure). `TestVictoriaMetrics` likewise skips when VictoriaMetrics isn't
running, and CI sets `TRICKSTER_VICTORIAMETRICS_TEST=1` to require it. See the [developer environment README](../docs/developer/environment/README.md#compose-profiles)
for the profiles.

The Kubernetes scenarios (every `Test*Kind`) are separate from compose
entirely: they need a kind cluster prepared via `make kind-integration-start`,
`make kind-integration-ingress` and `make kind-integration-gateway` (see
`kind/README.md`) and only run when `TRICKSTER_KIND_TEST=1` is set;
`make kind-integration-test` runs them all. The Gateway API conformance suite
lives in its own module, `conformance/`, and is run with
`make kind-conformance`.

The soaks run only when `TRICKSTER_SOAK_TEST=1` is set, and are **run by
hand rather than by CI**. `make soak` runs the autodiscovery soak
(`TestALBDiscoverySoak`) against nothing but the host, and `make kind-soak`
runs the gateway soak (`TestGatewaySoakKind`) against the kind cluster;
both take `SOAK_DURATION` (default 60m) and `SOAK_TIMEOUT` (default 90m):

```sh
make soak SOAK_DURATION=60m SOAK_TIMEOUT=90m
make kind-soak SOAK_DURATION=60m SOAK_TIMEOUT=90m
```

## Running

```sh
cd integration
make test              # full suite, fail-fast
make data-race-test    # full suite with -race
make cover-race        # CI: coverage and race detection in one suite run
make -C .. integration-test-no-failfast # full suite and race suite, continuing after failures
go test -run TestALB   # single test
TRICKSTER_MYSQL_CLI_TEST=1 go test -run TestMySQLRealServer -v
```

CI uses `cover-race` to run every scenario once with race detection and atomic
coverage instrumentation. The separate `cover` and `data-race-test` targets
remain available for local runs.

## Port assignments

Related HTTP/3, static-file, ClickHouse client, and Geo stream ACL scenarios
share one Trickster instance per group and run as sequential subtests. Cache-sensitive,
reload, lifecycle, and invalid-configuration scenarios retain isolated fixtures.
Each fixture boots on a unique port range to avoid TCP TIME_WAIT races between tests. Tests that need the full
developer config use `configHarness()` to clone it with swapped ports. The
helper reserves random frontend, metrics, management, and MySQL listener ports
until immediately before Trickster starts. Their addresses are exposed on the
returned harness.

## Structure

- `main_test.go` — `TestMain`, shared helpers (`startTrickster`, `waitFor*`,
  `queryTricksterProm`, `parseTricksterResult`)
- `kind_helpers_test.go` — the kind scenarios' shared helpers (`kubectlKind`,
  `applyKind`, `hostGet`, `waitRoute`, `startLoad`, `tlsSecretManifest`)
- `harness_test.go` — `tricksterHarness` boot helper, option-based HTTP client
  (`do`, `queryProm`, `withParams`, `withHeader`, `withBody`),
  `requireTricksterResult`, `runCacheProviderMatrix`, `configHarness`,
  `staticConfigHarness`
- `acme_helpers_test.go` — an in-process Pebble ACME CA and an authoritative DNS server
  that accepts RFC 2136 updates, so the ACME tests need no containers; only
  `TestACME_RedisStorage` uses the environment's Redis
- `testdata/` — static YAML configs for tests that need custom backends
  (ALB, rewriter, engines, rule, auth, purge, reload, TLS)

## Test guidelines

- Tests should be self-contained and independent, with no shared state or reliance on execution order.
- Tests should be deterministic and repeatable, avoiding reliance on external factors or timing.
- Tests should use unique query expressions when sharing a Trickster boot across subtests to avoid OPC cache collisions.
- Tests should be focused on specific features or scenarios, rather than trying to cover multiple features in a single test.
- Follow the projects coding style and conventions, and ensure that tests are well-documented and easy to understand.

### Avoiding flaky tests

A test that fails without a defect in Trickster stops everyone's work, so write
each assertion so that timing can't change its outcome:

- **Step rollovers.** When a time series request's range depends on the current
  time, a step boundary can pass between two identical requests, so the second
  one fetches one more bucket and reports `phit` instead of `hit`. Assert the
  hit with `requireCacheHit`, which retries only a `phit`, at most twice. Run a
  whole status sequence (for example `kmiss`, `hit`, `phit`) inside
  `stepwindow.Retry` so it restarts when a boundary passes.
- **Fresh environments.** CI starts the containers minutes before the tests,
  so never wait for data to accumulate in real time. Prometheus holds history
  only for the backfilled `trips_*` series. Don't query Telegraf's data: an
  InfluxDB test writes its own points first with `seedInfluxDB2` or
  `seedInfluxDB3`, which return the end of the span written, and queries the
  `it_cpu` measurement within it (`recentRange`).
- **Caches and state outlive a test.** The filesystem and Redis caches keep
  entries between the normal and `-race` runs, so a test that expects a miss
  needs a query no earlier run made (for example one carrying
  `time.Now().UnixNano()`). ALB member stats and sticky tables are kept per ALB
  name for the life of the process, so give each test's ALB its own name.
- **Metrics are process-wide.** Measure a counter before and after the action,
  and only on labels no background activity (health checks, other backends)
  also increments.
- **Poll, don't sleep.** Wait for an asynchronous effect with
  `require.Eventually`, allowing for the slower `-race` run, instead of sleeping
  for a fixed time. A pool member meant to stay healthy uses a
  `failure_threshold` above 1, so one slow probe can't take it out. A member
  the test takes down keeps a `failure_threshold` of 1: a higher one delays
  its removal, which default pools spend still routing to it, and a member
  that flaps faster than the threshold never leaves its pool at all.
- **Signals.** Call `guardSIGHUP` before a test's daemon starts, and send a
  reload with `sighupUntilReloaded`: the daemon subscribes to SIGHUP only once
  its startup completes, and an unguarded SIGHUP terminates the test process.
- **Held origins.** A test origin that holds its response until the test
  releases it must be released in a cleanup that runs before the server closes,
  or a failed assertion hangs the suite.

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
