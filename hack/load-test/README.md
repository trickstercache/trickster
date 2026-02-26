# Load Tests

Load tests for Trickster using [k6](https://k6.io/).

## Prerequisites

Install k6: https://grafana.com/docs/k6/latest/set-up/install-k6/

## Running

```sh
make run
```

This runs all `*.js` test scripts against the default endpoint (`http://localhost:8480/prom1`).

### Variables

| Variable | Default | Description |
|---|---|---|
| `TRICKSTER_BASE_ENDPOINT` | `http://localhost:8480/prom1` | Base URL of the Trickster backend to test |
| `PROMQL_QUERY` | `rate(process_cpu_seconds_total[5m])` | PromQL query to execute |
| `VIRTUAL_USERS` | `50` | Number of concurrent virtual users |
| `BATCH_SIZE` | `100` | Number of parallel HTTP requests per VU |
| `ITERATIONS` | `5000` | Total number of iterations across all VUs |

Override any variable on the command line:

```sh
make run TRICKSTER_BASE_ENDPOINT=http://localhost:8480/myprom PROMQL_QUERY='up' VIRTUAL_USERS=10 ITERATIONS=1000
```

## Test scripts

**basic.js** — Runs a PromQL instant query (controlled by `PROMQL_QUERY`) in a 30-second ramp-up
to the configured number of VUs. Passes if 95% of requests complete under 500 ms and >99% return a successful response.
