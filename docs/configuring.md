# Configuring Trickster

There are 3 ways to configure Trickster, listed here in the order of evaluation.

* Configuration File
* Environment Variables
* Command Line Arguments

Note that while the Confifguration file provides a very robust number of knobs you can adjust, the ENV and CLI Args options support only basic use cases.

## Internal Defaults

Internal Defaults are set for all configuration values, and are overridden by the configuration methods described below. All Internal Defaults are described in [cmd/trickster/conf/example.conf](../cmd/trickster/conf/example.conf) comments.

## Configuration File

Trickster accepts a `-config /path/to/trickster.conf` command line argument to specify a custom path to a Trickster configuration file. If the provided path cannot be accessed by Trickster, it will exit with a fatal error.

When a `-config` parameter is not provided, Trickster will check for the presence of a config file at `/etc/trickster/trickster.conf` and load it if present, or proceed with the Internal Defaults if not present.

Refer to [cmd/trickster/conf/example.conf](../cmd/trickster/conf/example.conf) for full documentation on format of a configuration file.

## Environment Variables

Trickster will then check for and evaluate the following Environment Variables:

* `TRK_ORIGIN=http://prometheus.example.com:9090` - The default origin for proxying all http requests
* `TRK_ORIGIN_TYPE=prometheus` - The type of [supported origin server](./supported-origin-types.md)
* `TRK_LOG_LEVEL=INFO` - Level of Logging that Trickster will output
* `TRK_PROXY_PORT=8480` -Listener port for the HTTP Proxy Endpoint
* `TRK_METRICS_PORT=8481` - Listener port for the Metrics and pprof debugging HTTP Endpoint

## Command Line Arguments

Finally, Trickster will check for and evaluate the following Command Line Arguments:

* `-log-level INFO` - Level of Logging that Trickster will output
* `-config /path/to/trickster.conf` - See [Configuration File](#configuration-file) section above
* `-origin http://prometheus.example.com:9090` - The default origin for proxying all http requests
* `-origin-type prometheus` - The type of [supported origin server](./supported-origin-types.md)
* `-proxy-port 8480` - Listener port for the HTTP Proxy Endpoint
* `-metrics-port 8481` - Listener port for the Metrics and pprof debugging HTTP Endpoint
