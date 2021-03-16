# Trickster Caching Retention Policies

## Basic HTTP Backends

Trickster will respect HTTP 1.0, 1.1 and 2.0 caching directives from both the downstream client and the upstream origin when determining object cacheability and TTL. You can override the TTL by setting a custom `Cache-Control` header on a per-[Path Config](./paths.md) basis.

### Cache Object Evictions

If you use a Trickster-managed cache (Memory, Filesystem, bbolt), then a maximum cache size is maintained by Trickster. You can configure the maximum size in number of bytes, number of objects, or both. See the example configuration for more information.

Once the cache has reached its configured maximum size of objects or bytes, Trickster will undergo an eviction routine that removes cache objects until the size has fallen below the configured maximums. Trickster-managed caches maintain a last access time for each cache object, and utilizes a Least Recently Used (LRU) methodology when selecting objects for eviction.

Caches whose object lifetimes are not managed internally by Trickster (Redis, BadgerDB) will use their own policies and methodologies for evicting cache records.

## Time Series Backends

For non-time series responses from a TSDB, Trickster will adhere to HTTP caching rules as directed by the downstream client and upstream origin.

For time series data responses, Trickster will cache as follows:

### TTL Settings

TTL settings for each Backend configured in Trickster can be customized independently of each other, and separate TTL configurations are available for timeseries objects, and fast forward data. See [examples/conf/example.full.yaml](../examples/conf/example.full.yaml) for more info on configuring default TTLs.

### Time Series Data Retention

Separately from the TTL of a time series cache object, Trickster allows you to control the size of each timeseries object, represented as a count of maximum timestamps in the cache object, on a _per origin_ basis. This configuration is known as the `timeseries_retention_factor` (TRF), and has a default of 1024. Most dashboards for most users request and display approximately 300-to-400 timestamps, so the default TRF allows users to still recall recently-displayed data from the Trickster cache for a period of time after the data has aged off of real-time views.

If you have users with a high-resolution dashboard configuration (e.g., a 24-hour view with a 1-minute step, amounting to 1440 data points per graph), then you may benefit from increasing the `timeseries_retention_factor` accordingly. If you use a managed cache (see [caches](./caches.md)) and increase the `timeseries_retention_factor`, the overall size of your cache will not change; the result will be fewer objects in cache, with the timeseries objects having a larger share of the overall cache size with more aged data.

#### Time Series Data Evictions

Once the TRF is reached for a time series cache object, Trickster will undergo a timestamp eviction process for the record in question. Unlike the Cache Object Eviction, which removes an object from cache completely, TRF evictions examine the data set contained in a cache object and remove timestamped data in order to reduce the object size down to the TRF.

Time Series Data Evictions apply to all cached time series data sets, regardless of whether or not the cache object lifecycle is managed by Trickster.

Trickster provides two eviction methodologies (`timeseries_eviction_method`) for time series data eviction: `oldest` (default) and `lru`, and is configurable per-origin.

When `timeseries_eviction_method` is set to `oldest`, Trickster maintains time series data by calculating the "oldest cacheable timestamp" value upon each request, using `time.Now().Add(step * timeseries_retention_factor * -1)`. Any queries for data older than the oldest cacheable timestamp are intelligently offloaded to the proxy since they will never be cached, and no data that is older than the oldest cacheable timestamp will be stored in the query's cache record.

When `timeseries_eviction_method` is set to `lru`, Trickster will not calculate an oldest cacheable timestamp, but rather maintain a last-accessed time for _each timestamp_ in the cache object, and evict the Least-Recently-Used items in order to maintain the cache size.

The advantage of the `oldest` methodology better cache performance, at the cost of not caching very old data. Thus, Trickster will be more performant computationally while providing a slightly lower cache hit rate.  The `lru` methodology, since it requires accessing the cache on _every request_ and maintaining access times for every timestamp, is computationally more expensive, but can achieve a higher cache hit rate since it permits caching data of any age, so long as it is accessed frequently enough to avoid eviction.

Most users will find the `oldest` methodology to meet their needs, so it is recommended to use `lru` only if you have a specific use case (e.g., dashboards with data from a diverse set of time ranges, where caching only relatively young data does not suffice).
