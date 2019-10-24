# Health Checks

## Ping Endpoint

Trickster provides a `/trickster/ping` endpoint that returns a response of `200 OK` and the word `pong` if Trickster is up and running.  The `/trickster/ping` endpoint does not check any proxy configurations or upstream origins. The path to the Ping endpoint is configurable, see the configuration documentation for more information.

## Health Check Endpoint

Trickster offers `/health` endpoints for monitoring the health of each origin independently. The behavior of a `/health` request will vary based on the Origin Type. For example, with Prometheus, Trickster makes a request to `/api/v1/query?query=up`, while for InfluxDB the request is to `/ping`.

An HTTP response of `200 OK` or `204 No Content` indicates that the end-to-end health check to the origin was successful.

In a multi-origin setup, requesting against `/health` will test the default origin. You can indicate a specific origin to test by crafting requests in the same way a normal multi-origin request is structured. For example, `/origin_name/health`. See [multi-origin.md](multi-origin.md) for more information.

## Other Ways to Monitor Health

In addition to the out-of-the-box health checks to determine up-or-down status, you may want to setup alarms and thresholds based on the metrics instrumented by Trickster. See [metrics.md](metrics.md) for collecting performance metrics about Trickster.

## Config Endpoint

Trickster also provides a `/trickster/config` endpoint, that returns the toml output of the currently-running Trickster configuration. The TOML-formatted configuration will include all defaults populated, and any command-line arguments and or applicable environment variables interpolated into the TOML. The path to the Config endpoint is configurable, see the configuration documentation for more information.
