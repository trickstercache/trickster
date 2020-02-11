# Trickster 1.0

## What's Improved

1.0 is a major improvement in over 0.1, with thousands of lines of code for new features, bug fixes, and optimizations. Here's the quick rundown of what's new and improved:

- Cache management is improved, with enhancements like a configurable max cache size and better metrics.
- Configuration now allows per-origin cache provider selection.
- Customizable HTTP Path Behaviors
- Built-in TLS Support
- The Time Series Delta Proxy is overhauled to be more efficient and performant.
- Support for [negative caching](./negative-caching.md)
- We now support Redis Cluster and Redis Sentinel (see [example.conf](../cmd/trickster/conf/example.conf))
- We've added a Prometheus data simulator for more robust unit testing.  Any other project that queries Prometheus may use it too as a standalone binary or as a package import for tests. See the [docs](./promsim.md) for more info.
- For Gophers: we've refactored the project into packages with a much more cohesive structure, so it's much easier for you to contribute.
- Also: The Cache Provider and Origin Proxy are exposed as Interfaces for easy extensibility.
- Experimental Support For:
  - [InfluxDB](./influxdb.md)
  - [ClickHouse](./clickhouse.md)
  - Circonus IRONdb
  - Generic HTTP Reverse Proxy Cache
- [Distributed Tracing](./tracing.md) support

And so much more! See the main [README](../readme.md) for more info.

## Breaking Changes from 0.1

### Prometheus Proxy as the Default Is Removed

Since Trickster 1.0 supports multiple Origin Types (instead of just Prometheus), the Prometheus-specific default operating configuration has been removed from the application code. The `example.conf` will, for now, continue to function as the example promtheus configuration.

This means you can't simply run `trickster` and have a functioning proxy to `prometheus:9090` as you could in 0.1. Instead, Trickster will fail out with an error that you have not defined any Origins.

This also means that with Trickster 1.0, you _must_ provide an `origin-type` for each Origin, so Trickster knows how to accelerate requests to it.

So in 1.0, you can run `trickster -origin-type prometheus -origin-url=http://prometheus:9090` or `trickster -config /path/to/example.conf` to achieve the same result as running `trickster` with no arguments in 0.1.

See the section below on migrating a 0.1 configuration for more information.

### Ping, Config, and Upstream Health CHeck URL Endpoints

In Trickster 1.0, non-proxied / administrative endpoints have been moved behind a `/trickster` root path, as follows:

- The previous `/ping` path, for checking if Trickster is up, is now at `/trickster/ping`.

- Origin-specific health check endpoints, previously routed via `/<origin_name>/health`, are now routed via `/trickster/health/<origin_name>`.

- A new endpoint to expose the current running configuration is available `/trickster/config`.

### Origin Selection using Query Parameters

In a multi-origin setup, Trickster 1.0 no longer supports the ability to select an Origin using Query Parameters. Trickster 1.0 continues to support Origin Selection via URL Path or Host Header as in 0.1.

### Configuration Settings

#### Hostname-based routing

In Trickster 0.1, we used the origin config name (e.g., `[origins.'domain.example.com']`) to determine which inbound hostnames map to which origins in the Router.

With 1.0, instead we provide a `hosts` option for Origin Configs that supports a list of routable hostnames. If you are using hostname-based routing in Trickster, you will need to convert it to a hosts entry. Here is an example conversion:

v0.1 hostname-based routing config to convert to work in 1.0:

```toml
[origins]
  [origins.'site1.example.com']
  origin_url = 'site1-origin.example.com'
  ...

  [origins.'www.site1.example.com']
  origin_url = 'site1-origin.example.com'
  ...
```

v1.0 hostname-based routing converted example:

```toml
[origins]
  [origins.site1]
  origin_url = 'site1-origin.example.com'
  hosts = [ 'site1.example.com', 'www.site1.example.com' ]
  ...
```

#### ignore_caching_headers / ignore_no_cache_header

The `ignore_caching_headers` and `ignore_no_cache_header` configuration parameters that evolved in 0.1 and early 1.0 betas has been removed. Trickster 1.0's customizable Path Configurations capability allows for unlimited paths to be defined and managed, including header manipulation; this subsumes the functionality of these configurations.

#### api_path

The `api_path` configuration parameter in 0.1 that defaulted to `/api/v1/` has been removed. Trickster 1.0's customizable Path Configurations capability allows for unlimited paths to be defined and managed; this subsumes the functionality of the `api_path`.

#### timeseries_retention_factor

A new setting called `timeseries_retention_factor` replaces `max_value_age_secs` from 0.1, which is removed.

`max_value_age_secs` provided a maximum relative age on the timestamp of any value retained in Trickster's cache, on a per-origin basis. That methodology works really well for browsers with a dashboard time range set to the last 24 hours (the default for max_value_age_secs) or less. But if your dashboards are set to a 5-day view, Trickster 0.1 will not cache the oldest 4 days of the data set, even though it is likely at a low-enough resolution to be ideal for caching. So each time your last-5-days dashboard reloads, 80% of the needed data is always requested from the origin server, instead of just 1%.

Conversely, while causing some large-timerange-with-low-resolution datasets to be undercached, `max_value_age_secs` also caused small-timerange-with-high-resolution datasets to be overcached. Imagine you have on display 24x7x365 an auto-refreshing 30-minute dashboaard on a large screen in the NOC. In that case, 24 hours' worth of data for each of the dashboard's queries, at the highest resolution of 15 seconds, is cached -- although most of it will never be read again once turning 31 minutes old. So those data sets cache 10x more data than they will ever need to retrieve in 0.1.

Enter `timeseries_retention_factor`. It improves upon `max_value_age_secs` by considering the _number_ of recent elements retained in the cache, rather than the _age_ of the elements' timestamps, when exercising the retention policy. This allows for virtually any chronological data set to be cached, regardless of its resolution or age, instead of just relatively recent datasets. This means Trickster 1.0 will perform flawlessly for the 5-day example, and keep the cache nice and lean in the 30-minute example, too. The eviction methodology of `timeseries_retention_factor` is controlled by an additional new setting called `timeseries_eviction_method` that allows you to choose between a performant methodology (`oldest`) that evicts chronologically oldest datapoints during eviction, or a more compute-intensive eviction methodology (`lru`) that evicts least-recently-used items, regardless of chronology. While the `lru` methodology will run hotter, it could result in a slightly better cache hit rate depending upon your specific use case. See the [retention documentation](./retention.md) for more info.

### Config File

Trickster 1.0 is incompatible with a 0.1 config file. However, it can be made compatible with a few quick migration steps (your mileage may vary):

- Make a backup of your config file.
- Tab-indent the entire `[cache]` configuration block.
- Search/Replace `[cache` with `[caches.default` (no trailing square bracket).
- Unless you are using Redis, copy/paste the `[caches.default.index]` section from the [example.conf](../cmd/trickster/conf/example.conf) into your new config file under `[caches.default]`, as in the example.
- Add a line with `[caches]` (unindented) immediately above the line with `[caches.default]`
- Under each of your `[origins.<name>]` configurations, add the following lines

```toml
    cache_name = 'default'
    origin_type = 'prometheus'
```

- Search and replace `boltdb` with `bbolt`
- Examine each `max_value_age_secs` setting in your config and convert to a `timeseries_retention_factor` setting as per the above section. The recommended value for `timeseries_retention_factor` is `1024`.

- For more information, refer to the [example.conf](../cmd/trickster/conf/example.conf), which is well-documented.
