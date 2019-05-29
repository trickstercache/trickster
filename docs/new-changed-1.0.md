# Trickster 1.0 Beta

## What's Improved?

1.0 is a major improvement in over 0.1.x. Here's the quick rundown of what has been improved:

- Cache management is improved, with enhancements like a configurable max cache size and better metrics.
- Configuration now allows per-origin cache provider selection.
- The Delta Proxy is overhauled to be more efficient and performant.
- We now support Redis Cluster and Redis Sentinel (see [example.conf](../cmd/trickster/conf/example.conf))
- We've added a Prometheus data simulator for more robust unit testing.  Any other project that queries prometheus may use it too. See the [docs](https://github.com/Comcast/trickster/blob/next/docs/promsim.md) for more info.
- For Gophers: we've refactored the project into packages with a much more cohesive structure, so it's much easier for you to contribute.
- Also: The Cache Provider and Origin Proxy are exposed as Interfaces for easy extensibility.
- InfluxDB support (very experimental)

### Still to Come in 1.0 (keep checking back!)
- Distributed Tracing support

## How to Try Trickster 1.0

The Docker image is available at `tricksterio/trickster:1.0-beta`, or see the Releases for downloadable binaries. We will push to this label each time a new beta release is ready, so you will need to `docker pull` to update to the latest beta as they are released. Additionally, we push to a monotonically incrementing beta label (e.g., `tricksterio/trickster:1.0-beta1`) to distinguish between beta builds.

We'd love your help testing Trickster 1.0, as well as contributing any improvements or bug reports and fixes. Thank you!

## Breaking Changes from 0.1.x

### Origin Selection using Query Parameters

In a multi-origin setup, Trickster 1.0 no longer supports the ability to select an Origin using Query Parameters. Trickster 1.0 continues to support Origin Selection via URL Path or Host Header as in 0.1.x.

### Configuration Settings

#### value_retention_factor

A new setting called `value_retention_factor` replaces `max_value_age_secs` from 0.1.x, which is removed.

`max_value_age_secs` provided a maximum relative age on the timestamp of any value retained in Trickster's cache, on a per-origin basis. That methodology works really well for browsers with a dashboard time range set to the last 24 hours (the default for max_value_age_secs) or less. But if your dashboards are set to a 5-day view, Trickster 0.1.x will not cache the oldest 4 days of the data set, even though it is likely at a low-enough resolution to be ideal for caching. So each time your last-5-days dashboard reloads, 80% of the needed data is always requested from the origin server, instead of just 1%. 

Conversely, while causing some large-timerange-with-low-resolution datasets to be undercached, `max_value_age_secs` also caused small-timerange-with-high-resolution datasets to be overcached. Imagine you have on display 24x7x365 an auto-refreshing 30-minute dashboaard on a large screen in the NOC. In that case, 24 hours' worth of data for each of the dashboard's queries, at the highest resolution of 15 seconds, is cached -- although most of it will never be read again once turning 31 minutes old. So those data sets cache 10x more data than they will ever need to retrieve in 0.1.x.

Enter `value_retention_factor`. It improves upon `max_value_age_secs` by considering the _number_ of recent elements retained in the cache, rather than the _age_ of the elements' timestamps, when exercising the retention policy. This allows for virtually any chronological data set to be cached, regardless of its resolution or age, instead of just relatively recent datasets. This means Trickster 1.0 will perform flawlessly for the 5-day example, and keep the cache nice and lean in the 30-minute example, too.

### Config File

Trickster 1.0 is incompatible with a 0.1.x config file. However, it can be made compatible with a few quick migration steps:

- Make a backup of your config file.
- Tab-indent the entire `[cache]` configuration block.
- Search/Replace `[cache` with `[caches.default` (no trailing square bracket).
- Unless you are using Redis, copy/paste the `[caches.default.index]` section from the [example.conf](../cmd/trickster/conf/example.conf) into your new config file under `[caches.default]`, as in the example.
- Add a line with `[caches]` (unindented) immediately above the line with `[caches.default]`
- Under each of your `[origins.<name>]` configurations, add the following line 

    `cache_name = 'default'`
- Search and replace `boltdb` with `bbolt`
- Examine each `max_value_age_secs` setting in your config and convert to a `value_retention_factor` setting as per the above section. The recommended value for `value_retention_factor` is `1024`.

- For more information, refer to the [example.conf](../cmd/trickster/conf/example.conf), which is well-documented.

