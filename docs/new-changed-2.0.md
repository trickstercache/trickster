# Trickster 2.0

## What's Improved

2.0 continues to improve the Trickster project, with a ton of new features, bug fixes, and optimizations. Here's the quick rundown of what's new and improved:

- We now use YAML for configuration, and provide [tooling](http://github.com/tricksterproxy/tricktool) to migrate a 1.x TOML configuration
- example configurations are relocated to the [examples](../examples/conf) directory
- we now use a common time series format internally for all supported TSDB's, rather than implementing each one separately
- health checking now uses a common package for all backend provdiers, rather than implementing separately in each backend
- we offer a brand new Application Load Balancer feature with unique and powerful options, like merging data from multiple backends into a single response.
- We've updated to Go 1.16
- We've re-organized many packages in the codebase to be more easily importable by other projects. Over the course of the beta, we'll be publishing new patterns to the [examples](../examples/) folder for using Trickster packages in your own projects, including caching, acceleration and load balancing.

## Installing

You can build the 2.0 binary from the `main` branch, download binaries from the Releases page, or use the `tricksterproxy/trickster:2` Docker image tag in containerized environments. Helm Charts version `2.0.0` is the chart release associated with Trickster v2.0.

## Breaking Changes from 1.x

### Configuration

Using [tricktool](http://github.com/tricksterproxy/tricktool) to migrate your configurations is the recommended approach. However, if you choose to convert your configuration by hand, here is what you need to know:

- The `[origins]` section of the Trickster 1.x TOML config is named `backends:` in the 2.0 YAML config.
- All duration-based values are now represented in milliseconds. 1.x values ending in `_secs` are the same in 2.0 but end in `_ms`. Be sure to multipy by 1000.
- `origin_type`, `cache_type` and `tracing_type` are now called `provider`.
- Health checking configurations now reside in their own `healthcheck` subsection under `backends` and use simplified config names like `method`, `path`, etc.

See the [example configuration](../examples/conf/example.full.yaml) for more information.
