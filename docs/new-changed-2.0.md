# Trickster 2.0

## What's Improved

2.0 continues to improve the Trickster project, with a ton of new features, bug fixes, and optimizations. Here's the quick rundown of what's new and improved:

- we now use YAML for configuration, and provide [tooling](http://github.com/tricksterproxy/tricktool) to migrate a 1.x TOML configuration
- example configurations are relocated to the [examples](../examples/conf) directory
- the [Trickster docker-compose demo](../examples/docker-compose) has been relocated to the examples directory and updated to use latest version tags
- we now use a common time series format internally for caching all supported TSDB's, rather than implementing each one separately
- [health checking](./health.md) now uses a common package for all backend provdiers, rather than implementing separately in each backend, and we now support automated health check polling for any backend, and provide a global health status endpoint
- we offer a brand new [Application Load Balancer](./alb.md) feature with unique and powerful options, like merging data from multiple backends into a single response.
- We've updated to Go 1.16
- We've re-organized many packages in the codebase to be more easily importable by other projects. Over the course of the beta, we'll be publishing new patterns to the [examples](../examples/) folder for using Trickster packages in your own projects, including caching, acceleration and load balancing.
- InfluxDB and ClickHouse now support additional formats like csv. More documentation will be provided over the course of the beta

## Still to Come

Trickster 2.0 is not yet feature complete, and we anticipate including the following additional features before the GA Release:

- support for InfluxDB 2.0 and the flux query language, and for queries sourced by Chronograf
- cache object purge via API
- brotli compression support (wire and backend cache)
- additional logging, metrics and tracing spans covering 2.0's new features
- an up-to-date Grafana dashboard template for monitoring Trickster
- support for additional Time Series Database providers
- incorporate ALB examples into the docker-compose demo

## Known Issues With the Latest Beta

The current Trickster 2.0 beta has the following known issues:

- the `lru` Time Series Eviction Method currently does not function, but will be added back in a future beta. This feature has not yet been ported into the Common Time Series format. Comment out this setting in your configuration to use the default eviction method.

- Helm Charts are not yet available for Trickster 2.0 and will be provided in a subsequent beta release.

## Installing

You can build the 2.0 binary from the `main` branch, download binaries from the [Releases](http://github.com/tricksterproxy/trickster/releases) page, or use the `tricksterproxy/trickster:2` Docker image tag in containerized environments.

## Breaking Changes from 1.x

### Metrics

- In metrics related to Trickster's operation, all label names of `origin_name` are changed to `backend_name`

### Configuration

Using [tricktool](http://github.com/tricksterproxy/tricktool) to migrate your configurations is the recommended approach. However, if you choose to convert your configuration by hand, here is what you need to know:

- <https://www.convertsimple.com/convert-toml-to-yaml/> is a good starting point
- The `[origins]` section of the Trickster 1.x TOML config is named `backends:` in the 2.0 YAML config
- All duration-based values are now represented in milliseconds. 1.x values ending in `_secs` are the same in 2.0 but end in `_ms`. Be sure to multiply by 1000
- `origin_type`, `cache_type` and `tracing_type` are now called `provider`.
- Health checking configurations now reside in their own `healthcheck` subsection under `backends` and use simplified config names like `method`, `path`, etc.

See the [example configuration](../examples/conf/example.full.yaml) for more information.
