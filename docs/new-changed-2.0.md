# Trickster 2.0

## An All-New Bag of Tricks

Trickster 2.0 is a near-complete rewrite of the project with performance, durability and extensibility in mind. We've made major architectural improvements, added new features and improved performance. Here's a comprehensive overview of it all:

### Features
- **Application Load Balancer (ALB)**: We have a brand new [Application Load Balancer](./alb.md) available as a backend provider type, with unique and powerful options, including:
  - Fanout and merge data from multiple time series backends into a single response
    - currently supports Prometheus backends
  - Fanout and return the first response received from any backend pool member
  - Fanout and return the response with the newest 'Last-Modified' header from all backend pool members
  - Routing a request to a given backend based on Basic Auth username
  - General Round Robin
- **Enhanced Health Checking**: [Health checking](./health.md) now supports automated health check polling for any backend, and provides a global health status endpoint. Automated health checks for an ALB pool member backend determines whether the ALB will route requests to it or not.
- **Cache Object Chunking**: We now support [Cache Object Chunking](./chunked_caching.md). This optional configuration allows a Time Series dataset to be chunked into multiple cache entries based on a configurable chunk size
    - Also supported by standard Reverse Proxy Cache for cache-chunking objects by a Byte Range size
  - Only the chunked cache entries needed to span the request range are inspected, rather than the entire time series
  - Significantly improves Redis and Filesystem performance of large timeseries records
- **Time Series Backend Request Sharding**: We now support [Time Series Backend Request Sharding](./timeseries_sharding.md). This optional feature allows a client request destined for a Time Series Backend to be cloned into multiple concurrent requests with different time ranges constituting the full uncached range
  - Backend shard responses + already-cached data are merged into a single downstream response
  - Cache Chunking and TS Backend Request Sharding work together seamlessly and can be used in any combination (on/on, on/off, off/on, off/off, including different cache chunk and request shard sizes).
- **Authenticator**: We've added a new [Authenticator](./authenticator.md) feature so you can guard backends with Basic Auth or ClickHouse Auth
- **Enhanced Rules Engine**: The [Rules Engine](./rule.md) now supports `rmatch` operations to permit regular expression-based routing against any part of the HTTP request
- **Request Rewriters**: You can now chain a collection of [request rewriters](./request_rewriters.md) for more robust request transformation possibilities
- **Cache Purging**: We now support purging specific cache items by Key (on the public ports) or Path (on the mgmt port). Read more in the [cache documentation](./caches.md)
- **Simulated Latency**: You can use Trickster for [Simulated Latency](./simulated-latency.md) in lab environments
- We've added support for InfluxDB 2.0 and Flux Query Language and are targeting support for InfluxDB 3.0 in Trickster v2.1.x.

### Configuration & Security
- **YAML Configuration**: Trickster 2.0 uses YAML for configuration instead of TOML
- **Environment Variable Substitution**: Environment Variable substitution is now possible in configuration files where sensitive information is expected
  - Supported via the following fields:
    - `caches[*].redis.password`, `backends[*].healthcheck.headers`, `backends[*].paths[*].request_headers`, `backends[*].paths[*].request_params`, `backends[*].paths[*].response_headers`, `authenticators[*].users`
  - Usage: `password: ${MY_SECRET_VAR}`
- **Request Body Size Limit**: A configurable Request Body Size limit has been added for `POST`, `PUT` and `PATCH` requests, with a default of 10MB. Requests with a body size exceeding the limit will receive a `413 Content Too Large` response. See [Request Body Handling Customizations](./body.md) for more info

### Under the Hood & Performance
- **Common Time Series Format**: We now use a common time series format internally for caching all supported TSDB's, rather than implementing delta + merge algorithms per-provider.
- **New HTTP Request Router**: We've switched to an all-new, made-for-proxies HTTP Request Router, which is up to 10X faster than the previous one
- **SQL Parser**: We've switched from Regular Expression matches for SQL-based Time Series Backends to an extensible lexer/parser solution, providing better performance and accuracy
  - ClickHouse backend providers now use the new SQL Parser
- **Race Condition Fixes**: We've eliminated nearly 100 race conditions and random panics
- **Expanded Compression Support**: Compression support now includes options for Brotli and Zstd

### Developer & Environment Improvements
- **Package Reorganization**: We've re-organized many packages in the codebase to be more easily importable by other projects. A future maintenance release will add documentation to the [examples](../examples/) folder for using Trickster packages in your own projects, including caching, acceleration and load balancing
- For Trickster contributors, we have a new [Docker Compose for developer environments](./developer/environment/docker-compose.yml).
- **CI/CD Enhancements**: We have added new CI tools, including better linters and race condition checkers to enforce and ensure ongoing project quality
- **Docker Automation**: We've updated our Docker automation:
  - The trickster-docker-images repo is now retired and image publishing is handled in the trickster repo
  - All merges to main will now push an image to Docker Hub at `trickstercache/trickster:main` as well as to `trickstercache/trickster:<COMMIT_ID>`
    - We no longer push images to the legacy `tricksterio` and `tricksterproxy` orgs on DockerHub. Everything is now and only `trickstercache` 🎉!
  - Images are also now pushed to the GitHub Container Repository as `ghcr.io/trickstercache/trickster`
- **Helm Charts**: The Helm Charts repository is now updated for Trickster 2.0
- **Vendor Directory**: We no longer include the `vendor` directory in the project repository and `vendor` is now in `.gitignore`. `vendor` will continue to be included in Release source tarballs

### Documentation & Examples

- **Example Configurations**: Example configurations are relocated to the [examples](../examples/conf) directory
- **Docker Compose Demo**: The [Trickster docker-compose demo](../examples/docker-compose) has been relocated to the examples directory and updated to use the latest version tags. This is the easiest way to try out Trickster 2.0!

## Still to Come

A future Trickster 2.0.x will include the following additional features that didn't quite make it to the finish line:
- incorporate ALB examples into the docker-compose demo
- the `lru` Time Series Cache Eviction Method is disabled, but will be added back in a future release. This feature has not yet been ported into the Common Time Series format

## Installing

You can build the 2.0 binary from the `main` branch, download binaries from the [Releases](http://github.com/trickstercache/trickster/releases) page, or use the `trickstercache/trickster` Docker image tag in containerized environments
