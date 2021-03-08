# Cache Options

## Supported Caches

There are several cache types supported by Trickster

* In-Memory (default)
* Filesystem
* bbolt
* BadgerDB
* Redis (basic, cluster, and sentinel)

The sample configuration ([examples/conf/example.full.yaml](../examples/conf/example.full.yaml)) demonstrates how to select and configure a particular cache type, as well as how to configure generic cache configurations such as Retention Policy.

## In-Memory

In-Memory Cache is the default type that Trickster will implement if none of the other cache types are configured. The In-Memory cache utilizes a Golang [sync.Map](https://godoc.org/sync#Map) object for caching, which ensures atomic reads/writes against the cache with no possibility of data collisions. This option is good for both development environments and most smaller dashboard deployments.

When running Trickster in a Docker container, ensure your node hosting the container has enough memory available to accommodate the cache size of your footprint, or your container may be shut down by Docker with an Out of Memory error (#137). Similarly, when orchestrating with Kubernetes, set resource allocations accordingly.

## Filesystem

The Filesystem Cache is a popular option when you have larger dashboard setup (e.g., many different dashboards with many varying queries, Dashboard as a Service for several teams running their own Prometheus instances, etc.) that requires more storage space than you wish to accommodate in RAM. A Filesystem Cache configuration keeps the Trickster RAM footprint small, and is generally comparable in performance to In-Memory. Trickster performance can be degraded when using the Filesystem Cache if disk i/o becomes a bottleneck (e.g., many concurrent dashboard users).

The default Filesystem Cache path is `/tmp/trickster`. The sample configuration demonstrates how to specify a custom cache path. Ensure that the user account running Trickster has read/write access to the custom directory or the application will exit on startup upon testing filesystem access. All users generally have access to /tmp so there is no concern about permissions in the default case.

## bbolt

The BoltDB Cache is a popular key/value store, created by [Ben Johnson](https://github.com/benbjohnson). [CoreOS's bbolt fork](https://github.com/etcd-io/bbolt) is the version implemented in Trickster. A bbolt store is a filesystem-based solution that stores the entire database in a single file. Trickster, by default, creates the database at `trickster.db` and uses a bucket name of 'trickster' for storing key/value data. See the example config file for details on customizing this aspect of your Trickster deployment. The same guidance about filesystem permissions described in the Filesystem Cache section above apply to a bbolt Cache.

## BadgerDB

[BadgerDB](https://github.com/dgraph-io/badger) works similarly to bbolt, in that it is a filesystem-based key/value datastore. BadgerDB provides its own native object lifecycle management (TTL) and other additional features that distinguish it from bbolt. See the configuration for more info on using BadgerDB with Trickster.

## Redis

Note: Trickster does not come with a Redis server. You must provide a pre-existing Redis endpoint for Trickster to use.

Redis is a good option for larger dashboard setups that also have heavy user traffic, where you might see degraded performance with a Filesystem Cache. This allows Trickster to scale better than a Filesystem Cache, but you will need to provide your own Redis instance at which to point your Trickster instance. The default Redis endpoint is `redis:6379`, and should work for most docker and kube deployments with containers or services named `redis`. The sample configuration demonstrates how to customize the Redis endpoint. In addition to supporting TCP endpoints, Trickster supports Unix sockets for Trickster and Redis running on the same VM or bare-metal host.

Ensure that your Redis instance is located close to your Trickster instance in order to minimize additional roundtrip latency.

In addition to basic Redis, Trickster also supports Redis Cluster and Redis Sentinel. Refer to the sample configuration for customizing the Redis client type.

## Purging the Cache

Cache purges should not be necessary, but in the event that you wish to do so, the following steps should be followed based upon your selected Cache Type.

A future release will provide a mechanism to fully purge the cache (regardless of the underlying cache type) without stopping a running Trickster instance.

### Purging In-Memory Cache

Since this cache type runs inside the virtual memory allocated to the Trickster process, bouncing the Trickster process or container will effectively purge the cache.

### Purging Filesystem Cache

To completely purge a Filesystem-based Cache, you will need to:

* Docker/Kube: delete the Trickster container (or mounted volume) and run a new one
* Metal/VM: Stop the Trickster process and manually run `rm -rf /tmp/trickster` (or your custom-configured directory).

### Purging Redis Cache

Connect to your Redis instance and issue a FLUSH command. Note that if your Redis instance supports more applications than Trickster, a FLUSH will clear the cache for all dependent applications.

### Purging bbolt Cache

Stop the Trickster process and delete the configured bbolt file.

### Purging BadgerDB Cache

Stop the Trickster process and delete the configured BadgerDB path.

## Cache Status

Trickster reports several cache statuses in metrics, logs, and tracing, which are listed and described in the table below.

| Status | Description |
| ----- | ----- |
| kmiss | The requested object was not in cache and was fetched from the origin |
| rmiss | Object is in cache, but the specific data range requested (timestamps or byte ranges) was not |
| hit | The object was fully cached and served from cache to the client |
| phit | The object was cached for some of the data requested, but not all |
| nchit | The response was served from the [Negative Cache](./negative-caching.md) |
| rhit | The object was served from cache to the client, after being revalidated for freshness against the origin |
| proxy-only | The request was proxied 1:1 to the origin and not cached |
| proxy-error | The upstream request needed to fulfill an associated client request returned an error |
