# Load Tests

Load tests for Trickster using [k6](https://k6.io/).

## Prerequisites

Install k6: https://grafana.com/docs/k6/latest/set-up/install-k6/

## Running

```sh
# Run all test scripts
make run

# Run only the query-range (dashboard simulation) test
make query-range

# Run only the instant-query test
make instant-query
```

### Variables

| Variable | Default | Description |
|---|---|---|
| `TRICKSTER_BASE_ENDPOINT` | `http://localhost:8480/prom1` | Base URL of the Trickster backend to test |
| `PROMQL_QUERY` | `rate(process_cpu_seconds_total[5m])` | PromQL query to execute |
| `VIRTUAL_USERS` | `50` | Number of concurrent virtual users |
| `BATCH_SIZE` | `100` | Number of parallel HTTP requests per VU |
| `ITERATIONS` | `500000` | Total number of iterations across all VUs (instant-query only) |
| `RANGE_SECONDS` | `10800` | Time range in seconds (query-range only, default 3h) |
| `STEP_SECONDS` | `15` | Query step in seconds (query-range only) |
| `REFRESH_INTERVAL` | `10` | Seconds between dashboard refreshes (query-range only) |
| `PANEL_QUERIES` | | JSON array of PromQL queries for multiple panels (query-range only) |

Override any variable on the command line:

```sh
make query-range TRICKSTER_BASE_ENDPOINT=http://localhost:8480/myprom VIRTUAL_USERS=20 RANGE_SECONDS=3600
```

Simulate a multi-panel dashboard:

```sh
make query-range PANEL_QUERIES='["rate(node_cpu_seconds_total{mode=\"idle\"}[1m])","node_memory_MemAvailable_bytes","rate(node_disk_io_time_seconds_total[5m])"]'
```

## Test scripts

**instant-query.js** — Runs a PromQL instant query (`/api/v1/query`) in a 30-second ramp to the configured VU count. Passes if 95% of requests complete under 500 ms and >99% return a successful response.

**query-range.js** — Simulates a Grafana dashboard: each VU iteration fires `query_range` requests for all panel queries with a floating time window (default: last 3 hours), then sleeps for the refresh interval. The start/end timestamps advance with wall-clock time, just like a live dashboard. The test runs a 15s ramp-up, 60s sustained load, and 10s ramp-down.
