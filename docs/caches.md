# Cache Types

There are 3 cache types supported by by Trickster

* In-Memory Cache (default)
* Filesystem Cache
* Redis Cache

The sample configuration ([conf/example.conf](../conf/example.conf)) demonstrates how to select and configure a particular cache type, as well as how to configure generic cache configurations such as Retention Policy.

## In-Memory Cache

In-Memory Cache is the default type that Trickster will implement if none of the other cache types are configured. The In-Memory cache utilizes a Golang [sync.Map](https://godoc.org/sync#Map) object for caching, which ensures atomic reads/writes against the cache with no possibility of data collisions. This option is good for both development environments and most smaller dashboard deployments.

When running Trickster in a Docker container, ensure your node hosting the container has enough memory available to accommodate the cache size of your footprint, or your container may be shut down by Docker with an Out of Memory error (#137). Similarly, when orchestrating with Kubernetes, set resource allocations accordingly.

We are working on better profiling of Trickster's In-Memory Cache footprint and will provide some general sizing guidance on when it is best to select one of the other Cache Types in a future release.

## Filesystem Cache

The Filesystem Cache is a popular option when you have larger dashboard setup (e.g., many different dashboards with many varying queries, Dashboard as a Service for several teams running their own Prometheus instances, etc.) that requires more storage space than you wish to accommodate in RAM. A Filesystem Cache configuration keeps the Trickster RAM footprint small, and is generally comparable in performance to In-Memory. Trickster performance can be degraded when using the Filesystem Cache if disk i/o becomes a bottleneck (e.g., many concurrent dashboard users).

The default Filesystem Cache path is `/tmp/trickster`. The sample configuration demonstrates how to specify a custom cache path. Ensure that the user account running Trickster has read/write access to the custom directory or the application will exit on startup upon testing filesystem access. All users generally have access to /tmp so there is no concern about permissions in the default case.


## Redis Cache

Redis is a good option for larger dashboard setups that also have heavy user traffic, where you might see degraded performance with a Filesystem Cache. This allows Trickster to scale better than a Filesystem Cache, but you will need to provide your own Redis instance at which to point your Trickster instance. The default Redis endpoint is `redis:6379`, and should work for most docker and kube deployments with containers or services named `redis`. The sample configuration demonstrates how to customize the Redis endpoint. In addition to supporting TCP endoints, Trickster supports unix sockets for Trickster and Redis running on the same VM or baremetal host.

Ensure that your Redis instance is located close to your Trickster instance in order to minimize additional roundtrip latency.

## Purging the Cache

Cache purges should not be necessary, but in the event that you wish to do so, the following steps should be followed based upon your selected Cache Type.

A future release will provide a mechanism to fully purge the cache (regardless of the underlying cache type) without stopping a running Trickster instance.

### In-Memory

Since this cache type runs inside the virtual memory allocated to the Trickster process, bouncing the Trickster process or container will effectively purge the cache.

### Filesystem

To completely purge a Filesystem-based Cache, you will need to:

* Docker/Kube: delete the Trickster container and run a new one
* Metal/VM: Stop the Trickster process and manually run `rm -rf /tmp/trickster` (or your custom-configured directory).

### Redis Cache

Connect to your Redis instance and issue a FLUSH command. Note that if your Redis instance supports more applications than Trickster, a FLUSH will clear the cache for all dependent applications.
