# Health Checks

## Trickster Service Health - Ping Endpoint

Trickster provides a `/trickster/ping` endpoint that returns a response of `200 OK` and the word `pong` if Trickster is up and running.  The `/trickster/ping` endpoint does not check any proxy configurations or upstream origins. The path to the Ping endpoint is configurable, see the configuration documentation for more information.

## Upstream Connection Health - Backend Health Endpoints

Trickster offers `health` endpoints for monitoring the health of the Trickster service with respect to its upstream connection to origin servers.

Each configured backend's health check path is `/trickster/health/BACKEND_NAME`. For example, if your backend is named `foo`, you can perform a health check of the upstream server at `http://<trickster_address:port>/trickster/health/foo`.

The backend health path prefix `/trickster/health/` is customizable. See the [example.full.yaml](../examples/conf/example.full.yaml) for more info about setting the `health_handler_path` configuration, or refer to this example:

```yaml
frontend:
  # this overrides the default '/trickster/health' to '/-/trickster/health'
  health_handler_path: /-/trickster/health
```

The behavior of a `health` request will vary based on the Backend provider, as each has their own health check protocol. For example, with Prometheus, Trickster makes a request to `/query?query=up` and (hopefully) receives a `200 OK`, while for InfluxDB the request is to `/ping` which returns a `204 No Content`.

Supported TSDB Providers are pre-configured in Trickster to perform a suitable health check operation, however these can be overridden in the configuration file.

For non-TSDB Backends, the default behavior is to make a `GET` request to `http://origin_url:port/` and expect a 2xx response. However, all aspects of the Health Check request and expected response are configurable per-Backend.

### Basic Health Check Configuration Example

```yaml
backends:
  server1:
    provider: reverseproxycache
    origin_url: http://server1
    healthcheck: # all values below are optional
      verb: HEAD
      path: /health
```

### Health Check With Exhaustive Request/Response Options

```yaml
backends:
  server1:
    provider: reverseproxy
    origin_url: http://server1
    healthcheck: # all values below are optional
      # 
      ## customizing the health check request
      #
      verb: HEAD
      scheme: https
      host: alternate-hostname.example.com
      path: /health
      query: param1=value1&param2=value2
      headers:
        User-Agent: health-check-agent
      # if using a POST or PUT method, you can provide a string body
      # body: "my health check body"
      #
      ## customizing the expected response
      #
      # hc fails if a response takes longer than 1s
      timeout_ms: 1000
      # hc fails if the response code is not in the list
      expected_codes: [ 200, 204, 206, 301, 302, 304 ]
      #
      # hc fails if these response headers are not present and have the expected value
      expected_headers:
        X-Health-Check-Status: success
      # hc fails if the stringified response body does not match the expected value
      expected_body: "pass"

```

See more examples in [example.full.yaml](../examples/conf/example.full.yaml).

## Health Check Integrations with Application Load Balancers

By default, a Backend will only initiate a health check on-demand, upon receiving a request to its health endpoint.

To facilitate integrations with the Trickster Application Load Balancer provider, additional options provide for 1) timed interval health checks and 2) thresholding for consecutive successful or unsuccessful health checks that determine the backend's overall health status.

### Example Health Check Configuration for use in ALB

```yaml
backends:
  server1:
    provider: reverseproxy
    origin_url: http://server1
    healthcheck:
      path: /health
      timeout_ms: 1000      # timeout_ms should be <= interval_ms
      # for ALB integration:
      interval_ms: 1000     # auto-poll health every 1s
      failure_threshold: 3  # backend is unhealthy after 3 consecutive failures
      recovery_threshold: 3 # backend is healthy after 3 consecutive successes
```

## Other Ways to Monitor Health

In addition to the out-of-the-box health checks to determine up-or-down status, you may want to setup alarms and thresholds based on the metrics instrumented by Trickster. See [metrics.md](metrics.md) for collecting performance metrics about Trickster.
