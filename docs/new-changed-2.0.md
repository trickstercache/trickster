# Trickster 2.0

## What's Improved

2.0 continues to improve the Trickster project, with a ton of new features, bug fixes, and optimizations. Here's the quick rundown of what's new and improved:

- Trickster 2.0 uses YAML for configuration instead of TOML.
- example configurations are relocated to the [examples](../examples/conf) directory
- the [Trickster docker-compose demo](../examples/docker-compose) has been relocated to the examples directory and updated to use latest version tags
- we now use a common time series format internally for caching all supported TSDB's, rather than implementing each one separately
- [health checking](./health.md) now uses a common package for all backend providers, rather than implementing separately in each backend, and we now support automated health check polling for any backend, and provide a global health status endpoint
- we offer a brand new [Application Load Balancer](./alb.md) feature with unique and powerful options, like merging data from multiple backends into a single response.
- We've updated to Go 1.24
- We've re-organized many packages in the codebase to be more easily importable by other projects. Over the course of the beta, we'll be publishing new patterns to the [examples](../examples/) folder for using Trickster packages in your own projects, including caching, acceleration and load balancing.
- InfluxDB and ClickHouse now support additional output formats like CSV. More documentation will be provided over the course of the beta
- Expanded Compression support now includes options for Broti and Zstd
- The [Rules Engine](./rule.md) now supports `rmatch` operations to permit regular expression-based routing against any part of the HTTP request.
- You can now chain a collection of [request rewriters](./request_rewriters.md) for more robust possibilities.


## New in Beta 3
- We've switched to our all-new, made-for-proxies HTTP Request Router, which is up to 10X faster than the previous router
- We now support InfluxData's Flux query language for InfluxDB query acceleration
  - We resolved tons of issues around POST requests for InfluxQL too!
- The Helm Charts repository is now updated for Trickster 2.0
- We now support [Cache Object Chunking](./chunked_caching.md)
  - Allows a Time Series to be chunked into multiple cache entries based on a configurable chunk size.
  - Only the chunk entries needed to span the request range are inspected, rather than the entire time series.
  - Significantly improves Redis and Filesystem performance of large timeseries records
  - Also supported by standard Reverse Proxy Cache for chunking objects by Byte Range
  - Disabled by default
- We now support [Time Series Backend Request Sharding](./timeseries_sharding.md)
  - Allows requests proxied to Time Series Backends to be chunked into multiple concurrent based on a configurable chunk size in arbitrary time units (milliseconds, seconds, etc) or by data points.
  - Backend Responses are merged into a single response before caching
  - Disabled by default
  - You can use Cache Chunking and TS Backend Request Sharding in any combination (on/on, on/off, off/on, off/off) as theywork together seamlessly. They can be configured with the same or different chunk sizes.
- We have added new CI tools, including better linters and race condition checkers
- We've updated our Docker automation:
  - The trickster-docker-images repo is now retired and image publishing is handled in the trickster repo.
  - All merges to main will now push an image to Docker Hub at `trickstercache/trickster:main` as well as to `trickstercache/trickster:<COMMIT_ID>`
    - We no longer push images to the legacy `tricksterio` and `tricksterproxy` orgs on DockeHub. Everything is now `trickstercache`!
  - Images are now pushed to the GitHub Container Repository:
    - `ghcr.io/trickstercache/trickster`
- We've eliminated over 70 race conditions and random panics
- We've switched from Regular Expression matches for SQL-based Time Series Backends to an extensible lexer/parser solution
  - ClickHouse backend providers now use the new SQL Parser
- We now support [Simulated Latency](./simulated-latency.md) if you want to use Trickster for that purpose in a test harness.
- We've added a new [Authenticator](authenticator.md) feature so you can guard backends with Basic Auth or ClickHouse Auth
- We've added a new [User Router Mechanism](alb.md) to the ALB.
- We now support Environment variable substitution in configuration files where sensitive information is expected.
  - Supported via the following fields:
    - `caches[*].redis.password`, `backends[*].healthcheck.headers`, `backends[*].paths[*].request_headers`, `backends[*].paths[*].request_params`, `backends[*].paths[*].response_headers`, `authenticators[*].users`
  - Usage: `password: ${MY_SECRET_VAR}`
- Previous Trickster 2.0 Betas used millisecond config values like `timeout_ms: 1500`. These have changed to `timeout: 1500ms`. See more details in the Configuration section below.
- We no longer include the `vendor` directory in the project repository and `vendor` is now in `.gitignore`. `vendor` will continue to be included in Release source tarballs.
- The listener port formerly called `reload` is now called `mgmt` in documentation and the YAML config specification. Several management-specific configs from the `main` section have been relocated to `mgmt` (e.g., Ping and Pprof handler settings) while the pre-existing reload `handler_path`, `drain_timeout`, and `rate_limit` configs are now prefixed with `reload_` (e.g., `reload_rate_limit`) for clarity. See the Full Example YAML's for more information.
- We now support Purging specific cache items by Key (on the public ports) or Path (on the mgmt port). Read more in the [cache documentation](./caches.md).
- The [tricktool](https://github.com/trickstercache/tricktool) utility is deprecated and archived.

## Still to Come

Trickster 2.0 is not yet feature complete, and we anticipate including the following additional features in Beta 4 before the GA Release:
- an up-to-date Grafana dashboard template for monitoring Trickster
- incorporate ALB examples into the docker-compose demo
- support for Auto-Discovery of Backend Targets (e.g., Kubernetes Pod Annotations)
- support MySQL as a Backend Time Series
- Support for InfluxDB 3.x

## Known Issues With the Latest Beta

The current Trickster 2.0 beta has the following known issues:

- the `lru` Time Series Eviction Method currently does not function, but will be added back in a future beta. This feature has not yet been ported into the Common Time Series format. Comment out this setting in your configuration to use the default eviction method.

- certain Path configs that should modify the client request or response (e.g., `response_headers`) are [not working reliably](https://github.com/trickstercache/trickster/issues/671). This will be fixed up in Beta 4.

- You may see warnings like the following on application startup for memory caches and other cache types that are instantiating for the first time. These are fine and are just complaining that newly-created caches don't yet have an index record. We will address in Beta 4.

```
time=2025-05-20T00:00:00.000000Z app=trickster level=warn event="cache index was not loaded" cacheName=default error="key not found in cache"
```

## Installing

You can build the 2.0 binary from the `main` branch, download binaries from the [Releases](http://github.com/trickstercache/trickster/releases) page, or use the `trickstercache/trickster:2` Docker image tag in containerized environments.

## Breaking Changes from 1.x

### Metrics

- In metrics related to Trickster's operation, all label names of `origin_name` are changed to `backend_name`
