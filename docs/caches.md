# Cache Overview

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

The Filesystem Cache is a popular option when you need more storage space than you wish to accommodate in RAM: a large dashboard setup (e.g., many different dashboards with many varying queries, Dashboard as a Service for several teams running their own Prometheus instances, etc.), or large objects like media and downloads. A Filesystem Cache configuration keeps the Trickster RAM footprint small. Trickster performance can be degraded when using the Filesystem Cache if disk i/o becomes a bottleneck.

The default Filesystem Cache path is `/tmp/trickster`. The sample configuration demonstrates how to specify a custom cache path. Ensure that the user account running Trickster has read/write access to the custom directory or the application will exit on startup upon testing filesystem access. All users generally have access to /tmp so there is no concern about permissions in the default case.

A local filesystem (e.g., ext4 or XFS) is recommended. Network filesystems are discouraged, as they are slow to create, rename and list the many files a cache is made of.

See [Disk Caches](#disk-caches) for how the Filesystem Cache lays out, recovers and bounds what it stores.

## bbolt

The BoltDB Cache is a popular key/value store, created by [Ben Johnson](https://github.com/benbjohnson). [CoreOS's bbolt fork](https://github.com/etcd-io/bbolt) is the version implemented in Trickster. A bbolt store is a filesystem-based solution that stores the entire database in a single file. Trickster, by default, creates the database at `trickster.db` and uses a bucket name of 'trickster' for storing key/value data. See the example config file for details on customizing this aspect of your Trickster deployment. The same guidance about filesystem permissions described in the Filesystem Cache section above apply to a bbolt Cache.

bbolt commits each write to disk before it returns, so it is slower to store an object than the Filesystem Cache, and is as fast or faster to read one. It is a good fit for caches of many small objects, since it does not spend a file on each.

See [Disk Caches](#disk-caches) for how the bbolt Cache recovers and bounds what it stores.

## Disk Caches

The Filesystem and bbolt Caches keep whatever they are given, so Trickster manages the lifecycle of their objects with a Cache Index, which it holds in memory and persists to the cache.

### Stored Objects

Each object is stored with a header that holds its cache key, its expiration and a checksum of its content. An object that is found to be expired, incomplete or damaged when it is read is treated as a cache miss and removed. The checksum of an object of 10 MiB or more is verified after the object is returned rather than before, so that a large read is not held for it; such an object, if damaged, is served once and then removed. The format of what is stored is private to Trickster and versioned: objects stored by a version that used another format are treated as cache misses and removed, so a disk cache can be cold after an upgrade. The release notes say when that is so.

The Filesystem Cache writes each object to a new file and renames it into place, so a reader never finds part of an object, even after a crash. Files are spread across two levels of directories under `cache_path`, named for the first characters of the file's name, to keep any one directory small:

```text
/tmp/trickster/9f/86/9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
/tmp/trickster/_meta/
```

A file is named for its cache key when the key is a lowercase hex digest of 32 or 64 characters, as the keys of HTTP objects are, and otherwise for a hash of the key. The `_meta` directory holds the Cache Index.

### Serving Large Objects

An object proxied through the [Reverse Proxy Cache](./paths.md) is stored in two sections: what describes it (status, headers, caching policy) and its body. When the whole of an object is cached uncompressed, a `GET` that is a fresh cache hit reads the body from the disk cache as it is written to the client, and a `Range` request reads only the ranges that were asked for. Neither holds the object in memory, whatever its size. Objects that Trickster compresses in the cache (see `compressible_types`) are read whole, as are objects that must first be revalidated.

### Cache Index

| Setting | Default | Description |
| ----- | ----- | ----- |
| `reap_interval` | `3s` | how often expired objects are removed, and a cache that is over its size is brought back within it |
| `flush_interval` | `5s` | how often changes to the index are persisted |
| `index_expiry` | `8760h` | how old a persisted index can be, and still be used at startup |
| `max_size_bytes` | `536870912` | the size in bytes at which least-recently-accessed objects are evicted |
| `max_size_backoff_bytes` | `16777216` | how far under `max_size_bytes` an eviction takes the cache |
| `max_size_objects` | `0` | the count of objects at which least-recently-accessed objects are evicted; `0` for no limit |
| `max_size_backoff_objects` | `100` | how far under `max_size_objects` an eviction takes the cache |
| `scan_interval` | `24h` | how often the cache is swept; `0` to sweep only when the index cannot be trusted |
| `scan_batch_size` | `512` | how many objects a sweep reads before it pauses |
| `scan_batch_pause` | `50ms` | how long a sweep pauses after each batch |

The Filesystem Cache also accepts `min_free_bytes`, the space to keep free on the filesystem that holds `cache_path`. When less is free, the cache evicts as it does when it is over `max_size_bytes`. It is supported on Unix-like systems.

**Persistence.** The index is persisted as a snapshot and a journal of the changes made since. Each `flush_interval`, the changes are appended to the journal; once the journal has grown to half the size of the snapshot, a new snapshot replaces both. Objects that expire within a minute of being stored are not persisted.

**Recovery.** When Trickster starts with an index that was persisted by a clean shutdown, it is used as it is. When the index is missing, expired, damaged, or was not closed by a clean shutdown, Trickster uses what it can of it and sweeps the cache in the background, while serving requests. A sweep reads the header of every object, `scan_batch_size` at a time, and lists the objects the index did not know of, so that nothing in the cache is orphaned. It also removes what can't be served: objects that are expired or damaged, files left by an interrupted write, and objects stored by a version of Trickster that used another format.

**Eviction.** Objects are evicted in the order they were least recently accessed. In a cache of more than 1024 objects, the object to evict is the least recently accessed of 32 taken at random, which costs the same however large the cache is, and closely follows the true order. A write that takes the cache over its size has the reaper act within a second, without waiting for `reap_interval`.

## BadgerDB

[BadgerDB](https://github.com/dgraph-io/badger) works similarly to bbolt, in that it is a filesystem-based key/value datastore. BadgerDB provides its own native object lifecycle management (TTL) and other additional features that distinguish it from bbolt. See the configuration for more info on using BadgerDB with Trickster.

## Redis

Note: Trickster does not come with a Redis server. You must provide a pre-existing Redis endpoint for Trickster to use.

Redis is a good option for larger dashboard setups that also have heavy user traffic, where you might see degraded performance with a Filesystem Cache. This allows Trickster to scale better than a Filesystem Cache, but you will need to provide your own Redis instance at which to point your Trickster instance. The default Redis endpoint is `redis:6379`, and should work for most docker and kube deployments with containers or services named `redis`. The sample configuration demonstrates how to customize the Redis endpoint. In addition to supporting TCP endpoints, Trickster supports Unix sockets for Trickster and Redis running on the same VM or bare-metal host.

Ensure that your Redis instance is located close to your Trickster instance in order to minimize additional roundtrip latency.

In addition to basic Redis, Trickster also supports Redis Cluster and Redis Sentinel. Refer to the sample configuration for customizing the Redis client type.

Trickster supports Redis servers that use TLS encryption by setting `use_tls: true` in the config. Refer to the sample configuration for more info.

## Purging an Item from the Cache

You can purge an item from the cache by making a call to the purge endpoint, as follows:

```http://${trickster-address}:${mgmt-port}/trickster/purge/path/${backendName}/${path/to/purge}```

For example, if you want to purge `/api/v1/labels` from backend `prom1`, a curl might look like:

```
curl http://localhost:8484/trickster/purge/path/prom1/api/v1/labels
```


## Purging the Full Cache

Full Cache purges should not be necessary, but in the event that you wish to do so, the following steps should be followed based upon your selected Cache Type.

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

Trickster reports several cache statuses in metrics, logs, tracing, and the [`X-Trickster-Result`](./trickster-result.md) response header, which are listed and described in the table below.

| Status | Description |
| ----- | ----- |
| kmiss | The requested object was not in cache and was fetched from the origin |
| rmiss | Object is in cache, but the specific data range requested (timestamps or byte ranges) was not |
| hit | The object was fully cached and served from cache to the client |
| phit | The object was cached for some of the data requested, but not all |
| nchit | The response was served from the [Negative Cache](./negative-caching.md) |
| rhit | The object was served from cache to the client, after being revalidated for freshness against the origin |
| purge | The cache key was purged as directed by a request or response header |
| proxy-only | The request was proxied 1:1 to the origin and not cached |
| proxy-error | The upstream request needed to fulfill an associated client request returned an error |
| error | Trickster encountered a cache lookup or cache handling error |
| proxy-hit | The request joined an existing in-flight origin fetch for the same cache key |
