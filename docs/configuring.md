# Configuring Trickster

There are 3 ways to configure Trickster, listed here in the order of evaluation.

* Configuration File
* Environment Variables
* Command Line Arguments

Note that while the Configuration file provides a very robust number of knobs you can adjust, the ENV and CLI Args options support only basic use cases.

## Internal Defaults

Internal Defaults are set for all configuration values, and are overridden by the configuration methods described below. All Internal Defaults are described in [examples/conf/example.full.yaml](../examples/conf/example.full.yaml) comments.

## Configuration File

Trickster accepts a `-config /path/to/trickster.yaml` command line argument to specify a custom path to a Trickster configuration file. If the provided path cannot be accessed by Trickster, it will exit with a fatal error.

When a `-config` parameter is not provided, Trickster will check for the presence of a config file at `/etc/trickster/trickster.yaml` and load it if present, or proceed with the Internal Defaults if not present.

Refer to [examples/conf/example.full.yaml](../examples/conf/example.full.yaml) for full documentation on format of a configuration file.

## Environment Variables

Trickster will then check for and evaluate the following Environment Variables:

* `TRK_ORIGIN_URL=http://prometheus.example.com:9090` - The default origin URL for proxying all http requests
* `TRK_ORIGIN_TYPE=prometheus` - The type of [supported backend server](./supported-origin-types.md)
* `TRK_LOG_LEVEL=INFO` - Level of Logging that Trickster will output
* `TRK_PROXY_PORT=8480` -Listener port for the HTTP Proxy Endpoint
* `TRK_METRICS_PORT=8481` - Listener port for the Metrics and pprof debugging HTTP Endpoint

## Command Line Arguments

Finally, Trickster will check for and evaluate the following Command Line Arguments:

* `-log-level INFO` - Level of Logging that Trickster will output
* `-config /path/to/trickster.yaml` - See [Configuration File](#configuration-file) section above
* `-origin-url http://prometheus.example.com:9090` - The default origin URL for proxying all http requests
* `-provider prometheus` - The type of [supported backend server](./supported-origin-types.md)
* `-proxy-port 8480` - Listener port for the HTTP Proxy Endpoint
* `-metrics-port 8481` - Listener port for the Metrics and pprof debugging HTTP Endpoint

## Configuration Validation

Trickster can validate a configuration file by running `trickster -validate-config -config /path/to/config`. Trickster will load the configuration and exit with the validation result, without running the configuration.

## Reloading the Configuration

Trickster can gracefully reload the configuration file from disk without impacting the uptime and responsiveness of the application.

Trickster provides 2 ways to reload the Trickster configuration: by requesting an HTTP endpoint, or by sending a SIGHUP (e.g., `kill -1 $TRICKSTER_PID`) to the Trickster process. In both cases, the underlying running Configuration File must have been modified such that the last modified time of the file is different than from when it was previously loaded.

### Config Reload via SIGHUP

Once you have made the desired modifications to your config file, send a SIGHUP to the Trickster process by running `kill -1 $TRICKSTER_PID`. The Trickster log will indicate whether the reload attempt was successful or not.

### Config Reload via HTTP Endpoint

Trickster provides an HTTP Endpoint for viewing the running Configuration, as well as requesting a configuration reload.

The reload endpoint is configured by default to listen on address `127.0.0.1` and port `8484`, at `/trickster/config/reload`. These values can be customized, as demonstrated in the example.full.yaml The examples in this section will assume the defaults. Set the port to `-1` to disable the reload HTTP interface altogether.

To reload the config, simply make a `GET` request to the reload endpoint. If the underlying configuration file has changed, the configuration will be reloaded, and the caller will receive a success response. If the underlying file has not changed, the caller will receive an unsuccessful response, and reloading will be disabled for the duration of the Reload Rate Limiter. By default, this is 3 seconds, but can be customized as demonstrated in the example config file. The Reload Rate Limiter applies to the HTTP interface only, and not SIGHUP.

If an HTTP listener must spin down (e.g., the listen port is changed in the refreshed config), the old listener will remain alive for a period of time to allow existing connections to organically finish. This period is called the Drain Timeout and is configurable. Trickster uses 30 seconds by default. The Drain Timeout also applies to old log files, in the event that a new log filename has been provided.

### View the Running Configuration

Trickster also provides a `http://127.0.0.1:8484/trickster/config` endpoint, which returns the yaml output of the currently-running Trickster configuration. The YAML-formatted configuration will include all defaults populated, overlaid with any configuration file settings, command-line arguments and or applicable environment variables. This read-only interface is also available via the metrics endpoint, in the event that the reload endpoint has been disabled. This path is configurable as demonstrated in the example config file.
