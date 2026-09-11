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
| `SIZE` | `M` | Load test size preset: S, M, L, XL (see `sizes.env`) |
| `RATE` | | Target iterations/sec — enables constant-arrival-rate mode (see below) |
| `DURATION` | `2m` | Test duration in rate mode |
| `MAX_VUS` | `500` | Maximum VUs k6 can allocate in rate mode |
| `RANGE_SECONDS` | `21600` | Time range in seconds (query-range only, default 6h) |
| `STEP_SECONDS` | `15` | Query step in seconds (query-range only) |
| `REFRESH_INTERVAL` | `5` | Seconds between dashboard refreshes (query-range only) |
| `PANEL_QUERIES` | | JSON array of PromQL queries for multiple panels (query-range only) |

Override any variable on the command line:

```sh
make query-range TRICKSTER_BASE_ENDPOINT=http://localhost:8480/myprom RANGE_SECONDS=3600
```

Simulate a multi-panel dashboard:

```sh
make query-range PANEL_QUERIES='["rate(node_cpu_seconds_total{mode=\"idle\"}[1m])","node_memory_MemAvailable_bytes","rate(node_disk_io_time_seconds_total[5m])"]'
```

### Rate mode

By default, tests use a fixed VU count and iteration budget (`SIZE` presets).
Set `RATE` to switch to a `constant-arrival-rate` executor, which targets a
fixed request rate using fewer VUs:

```sh
# 200 req/s for 5 minutes, up to 500 VUs
make query-range RATE=200 DURATION=5m MAX_VUS=500
```

This is useful for high-throughput testing on machines that can't sustain
thousands of VUs (e.g., macOS with default file descriptor limits).

## macOS tuning for larger sizes

Sizes L and XL require raising the default file descriptor and kernel limits:

```sh
# Check current limits
ulimit -n
sysctl kern.maxfiles kern.maxfilesperproc

# Raise for the current shell session
ulimit -n 65536
sudo sysctl -w kern.maxfiles=131072
sudo sysctl -w kern.maxfilesperproc=65536
```

The `ulimit` change applies only to the current shell. The `sysctl` changes reset on reboot.

## Test scripts

**instant-query.js** — Runs a PromQL instant query (`/api/v1/query`). Passes if 95% of requests complete under 500 ms and >99% return a successful response.

**query-range.js** — Simulates a Grafana dashboard: each VU iteration fires `query_range` requests for all panel queries with a floating time window (default: last 6 hours), then sleeps for the refresh interval. The start/end timestamps advance with wall-clock time, just like a live dashboard.
