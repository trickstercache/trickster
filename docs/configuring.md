# Configuring Trickster

There are 3 ways to configure Trickster, listed here in the order of evaluation.

* Configuration File
* Environment Variables
* Command Line Arguments

Note that while the Configuration file provides a very robust number of knobs you can adjust, the ENV and CLI Args options support only basic use cases.

## Internal Defaults

Internal Defaults are set for all configuration values, and are overridden by the configuration methods described below. All Internal Defaults are described in [examples/conf/example.full.yaml](../examples/conf/example.full.yaml) comments.

## Configuration Files

Trickster accepts a `-config /path/to/trickster.yaml` command line argument to specify a custom configuration file. The path can also name a directory containing configuration files. If a provided path cannot be accessed by Trickster, it will exit with a fatal error.

When a `-config` parameter is not provided, Trickster will check for the presence of a config file at `/etc/trickster/trickster.yaml` and load it if present, or proceed with the Internal Defaults if not present.

Refer to [examples/conf/example.full.yaml](../examples/conf/example.full.yaml) for full documentation on format of a configuration file.

### Multiple Configuration Files

When `-config` names a file, Trickster loads that file first and then loads supported files from a sibling `conf.d` directory, if the directory exists. The primary file can select a different include directory:

```yaml
main:
  config_include_directory: config-parts
```

A relative `config_include_directory` is resolved from the directory containing the primary file. An explicitly configured include directory must exist. Only the primary file can set this option; included files cannot redirect configuration discovery.

When `-config` names a directory, Trickster loads supported files directly from that directory. The directory must contain at least one supported file, and files in this mode cannot set `main.config_include_directory`.

In both modes, Trickster:

* Loads only direct regular files whose names do not start with `.`, with `.conf`, `.yaml`, or `.yml` extensions matched case-insensitively.
* Loads directory entries in ascending lexical filename order. The primary file, when present, always comes first.
* Recursively merges mappings. A later scalar or sequence replaces the earlier value, while a later mapping adds to or overrides individual keys in the earlier mapping.
* Requires each file participating in a multi-source configuration to have a mapping root, one YAML document, and no duplicate keys.

For example, a fragment containing only `backends.prometheus.origin_url` can change that field without removing the other fields under the `prometheus` backend. Use `null`, rather than an empty mapping, when a later file must clear an earlier mapping value.

### Configuring Secrets or Sensitive Information

Trickster supports Environment variable substitution in its configuration file where sensitive information is expected.
- Supported via the following fields:
  - `caches[*].redis.password`, `backends[*].healthcheck.headers`, `backends[*].cors.headers`, `backends[*].paths[*].cors.headers`, `backends[*].paths[*].request_headers`, `backends[*].paths[*].request_params`, `backends[*].paths[*].response_headers`

Usage `${ENV_VAR_NAME}`, example:
```yaml
caches:
  default:
    redis:
      password: "${MY_REDIS_PW}"
```

## Environment Variables

Trickster will then check for and evaluate the following Environment Variables:

* `TRK_ORIGIN_URL=http://prometheus.example.com:9090` - The default origin URL for proxying all http requests
* `TRK_ORIGIN_TYPE=prometheus` - The type of [supported backend server](./supported-backend-providers.md)
* `TRK_LOG_LEVEL=INFO` - Level of Logging that Trickster will output
* `TRK_PROXY_PORT=8480` -Listener port for the HTTP Proxy Endpoint
* `TRK_METRICS_PORT=8481` - Listener port for the Metrics and pprof debugging HTTP Endpoint

## Command Line Arguments

Finally, Trickster will check for and evaluate the following Command Line Arguments:

* `-log-level INFO` - Level of Logging that Trickster will output
* `-config /path/to/trickster.yaml` - See [Configuration Files](#configuration-files) section above
* `-origin-url http://prometheus.example.com:9090` - The default origin URL for proxying all http requests
* `-provider prometheus` - The type of [supported backend server](./supported-origin-types.md)
* `-proxy-port 8480` - Listener port for the HTTP Proxy Endpoint
* `-metrics-port 8481` - Listener port for the Metrics and pprof debugging HTTP Endpoint

## Inbound Listeners

The top-level `listeners` map configures inbound listeners. Trickster always auto-defines three entries using the existing defaults: `default`, `metrics`, and `mgmt`.

Native MySQL listeners have additional protocol, authentication, TLS, and
session-lifecycle requirements. See the [MySQL Provider Guide](mysql.md) before
configuring `protocol: mysql`.
ClickHouse Native listeners use `protocol: clickhouse`; see the
[ClickHouse Support Guide](clickhouse.md) for ingress, origin, and TLS options.

```yaml
listeners:
  default:
    address: ""
    port: 8480
    tls_address: ""
    tls_port: 8483
    connections_limit: 0
    read_header_timeout: 10s
  private_api:
    protocol: http
    address: 127.0.0.1
    port: 9080

backends:
  default:
    listener_names: [default, private_api]
    provider: prometheus
    origin_url: http://prometheus:9090
  private:
    listener_names: [private_api]
    provider: reverseproxy
    origin_url: http://private-origin
```

`listener_names` binds a backend to one or more compatible listeners. An ordinary unbound backend uses `default`; internal routing targets remain unexposed. A backend cannot select the reserved `mgmt` or `metrics` listeners, and validation fails for undefined or provider-incompatible listeners.

Each native listener maps to exactly one backend. Multiple HTTP listeners can share a backend, and ClickHouse can bind the same backend to HTTP and ClickHouse Native listeners.

A user-defined listener with no mapped backend is not started and produces a warning. A configured TLS port is enabled only when at least one backend mapped to that listener provides a valid frontend certificate and key in its `tls` section; otherwise Trickster disables that TLS port and logs a warning.

The top-level `frontend` section and listener address/port fields under `metrics` and `mgmt` remain supported during the compatibility period. Trickster logs deprecation warnings when those legacy listener settings are used. When the same built-in listener is present in `listeners`, its new configuration takes precedence.

## Configuration Validation

Trickster can validate configuration files by running `trickster -validate-config -config /path/to/config`. Trickster will load the file or directory and exit with the validation result, without running the configuration.

## Reloading the Configuration

Trickster can gracefully reload its configuration sources from disk without impacting the uptime and responsiveness of the application.

Trickster supports manual reloads by requesting an HTTP endpoint or sending a SIGHUP (e.g., `kill -1 $TRICKSTER_PID`) to the Trickster process. It can also poll the effective configuration sources automatically. In all cases, at least one effective configuration source must have changed since the configuration was loaded.

### Automatic Config Reload

Trickster can poll its effective configuration sources and reload after a change. This is disabled by default. Set `mgmt.auto_reload_interval` to a positive duration to enable it:

```yaml
mgmt:
  auto_reload_interval: 10s
```

Polling uses the same validation and graceful reload path as SIGHUP and the management endpoint. The interval itself is reloadable, so a successful configuration update can change or disable automatic reloads. Polling is suitable for Kubernetes ConfigMap projected volumes, whose atomic symlink updates are not reliably represented as writes to the mounted file by filesystem notification APIs.

### Config Reload via SIGHUP

Once you have made the desired modifications to your config file, send a SIGHUP to the Trickster process by running `kill -1 $TRICKSTER_PID`. The Trickster log will indicate whether the reload attempt was successful or not.

### Config Reload via HTTP Endpoint

Trickster provides an HTTP Endpoint for viewing the running Configuration, as well as requesting a configuration reload.

The reload endpoint is configured by default to listen on address `127.0.0.1` and port `8484`, at `/trickster/config/reload`. These values can be customized, as demonstrated in the example.full.yaml The examples in this section will assume the defaults. Set the port to `-1` to disable the reload HTTP interface altogether.

To reload the config, simply make a `GET` request to the reload endpoint. If an underlying configuration source has changed, or a supported file has been added to or removed from a configured directory, the configuration will be reloaded and the caller will receive a success response. If the configuration sources have not changed, the caller will receive an unsuccessful response, and reloading will be disabled for the duration of the Reload Rate Limiter. By default, this is 3 seconds, but can be customized as demonstrated in the example config file.

If a listener address or port changes, Trickster drains the old listener before starting its replacement. Listeners whose network settings do not change retain their open sockets and receive the refreshed router in place. Removed or newly unused listeners are drained and stopped, while newly mapped listeners are started. The drain period is configurable and defaults to 30 seconds. The Drain Timeout also applies to old log files when a new log filename is provided.

### View the Running Configuration

Trickster also provides a `http://127.0.0.1:8484/trickster/config` endpoint, which returns the yaml output of the currently-running Trickster configuration. The YAML-formatted configuration will include all defaults populated, overlaid with any configuration file settings, command-line arguments and or applicable environment variables. By default, this interface is available only on the management listener. Set `mgmt.config_handler_listener` to `metrics`, `both`, or `off` to change where it is exposed. This path is configurable as demonstrated in the example config file.

Trickster also provides a sanitized view of the running configuration at `http://127.0.0.1:8484/trickster/config/sanitized`. If the `config_handler_path` is customized, append `/sanitized` to the configured path. The sanitized output deep-copies the running configuration, renames cache, backend, listener, and tracing resources by provider and sequence number (for example, `prom-1`, `prom-2`, `alb-1`, `memory-1`, `listener-1`, `otlp-1`), renames authenticators as `auth1`, `auth2`, etc., updates references to those resources in backend, path, ALB, rule, cache, tracing, listener, and authenticator mappings, replaces backend `origin_url`, Redis `endpoint` and `endpoints`, tracing `endpoint`, and Host-related request rewriter values with `example.com`, redacts per-path request and response header values, and replaces embedded authenticator users with `user1: redacted`, `user2: redacted`, etc. This endpoint is intended for sharing running configuration details in support requests without exposing private infrastructure names, origin endpoints, or user credentials.
