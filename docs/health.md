# Health Checks

## Ping Endpoint
Trickster provides a `/ping` endpoint that returns a response of `200 OK` and the word `pong` if Trickster is up and running.  The `/ping` endpoint does not check any proxy configurations or upstream origins.

## Health Check Endpoint
Trickster offers a `/health` endpoint for monitoring the health of the Trickster service with respect to its upstream connection to an origin. The behavior of a `/health` request will vary based on the Origin Type. For example, with Prometheus, Trickster makes a request to the origin's labels endpoint (`/label/__name__/values`), while for InfluxDB the request is to the `/ping` endpoint.

An HTTP response of `200 OK` or `204 No Content` indicates that the end-to-end health check to the origin was successful.

In a multi-origin setup, requesting against `/health` will test the default origin. You can indicate a specific origin to test by crafting requests in the same way a normal multi-origin request is structured. For example, `/origin_name/health`. See [multi-origin.md](multi-origin.md) for more information.

## Other Ways to Monitor Health

In addition to the out-of-the-box health checks to determine up-or-down status, you may want to setup alarms and thresholds based on the metrics instrumented by Trickster. See [metrics.md](metrics.md) for collecting performance metrics about Trickster.
