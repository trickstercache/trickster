# Health Checks

## Trickster Service Health - Ping Endpoint

Trickster provides a `/trickster/ping` endpoint that returns a response of `200 OK` and the word `pong` if Trickster is up and running.  The `/trickster/ping` endpoint does not check any proxy configurations or upstream origins. The path to the Ping endpoint is configurable, see the configuration documentation for more information.

## Upstream Connection Health - Origin Health Endpoints

Trickster offers `health` endpoints for monitoring the health of the Trickster service with respect to its upstream connection to origin servers.

Each configured origin's health check path is `/trickster/health/ORIGIN_NAME`. For example, if your origin is named `foo`, you can perform a health check of the upstream URL at `http://<trickster_address:port>/trickster/health/foo`.

 The behavior of a `health` request will vary based on the Origin Type, as each Origin Type implements a custom default health check behavior. For example, with Prometheus, Trickster makes a request to `/query?query=up` and (hopefully) return a `200 OK`, while for InfluxDB the request is to `/ping` and will return a `204 No Content`. You can customize the behavior in the Trickster configuration. See the example.conf for guidance.

The Origin-Specific default health check configurations should return a 200-range status code to indicate that the end-to-end health check to the origin was successful. Note that this behavior is not guaranteed when operating under user-provided health check configurations.

## Other Ways to Monitor Health

In addition to the out-of-the-box health checks to determine up-or-down status, you may want to setup alarms and thresholds based on the metrics instrumented by Trickster. See [metrics.md](metrics.md) for collecting performance metrics about Trickster.

## Config Endpoint

Trickster also provides a `/trickster/config` endpoint, that returns the toml output of the currently-running Trickster configuration. The TOML-formatted configuration will include all defaults populated, and any command-line arguments and or applicable environment variables interpolated into the TOML. The path to the Config endpoint is configurable, see the configuration documentation for more information.
