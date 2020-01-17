# trickster

[Trickster](https://github.com/Comcast/trickster) is a reverse proxy cache for the Prometheus HTTP APIv1 that dramatically accelerates dashboard rendering times for any series queried from Prometheus.

## Introduction

This chart bootstraps a [Trickster](https://github.com/Comcast/trickster) deployment on a [Kubernetes](http://kubernetes.io) cluster using the [Helm](https://helm.sh) package manager.

## Configuration

The following table lists the configurable parameters of the trickster chart and their default values.

Parameter | Description | Default
--- | --- | ---
`affinity` | Node/Pod affinities | `{}`
`image.repository` | Image | `hub.docker.com/tricksterio/trickster`
`image.tag` | Image tag | `1.0.1`
`image.pullPolicy` | Image pull policy | `IfNotPresent`
`ingress.enabled` | If true, Trickster Ingress will be created | `false`
`ingress.annotations` | Annotations for Trickster Ingress` | `{}`
`ingress.fqdn` | Trickster Ingress fully-qualified domain name | `""`
`ingress.tls` | TLS configuration for Trickster Ingress | `[]`
`nodeSelector` | Node labels for pod assignment | `{}`
`originURL` | Default trickster originURL, references a source Prometheus instance | `http://prometheus:9090`
`caches.name` | Name of the cache to be defined | `default`
`caches.type` | The cache_type to use.  {boltdb, filesystem, memory, redis} | `memory`
`caches.compression` | Boolean to compress the cache | `true`
`caches.timeSeriesTTLSecs` | The relative expiration of cached timeseries | `21600`
`caches.fastForwardTTLSecs` | The relative expiration of cached fast forward data | `15`
`caches.objectTTLSecs` | The relative expiration of generically cached (non-timeseries) objects | `30`
`caches.index.reapIntervalSecs` | How long the Cache Index reaper sleeps between reap cycles | `3`
`caches.index.flushIntervalSecs` | How often the Cache Index saves its metadata to the cache from application memory | `5`
`caches.index.maxSizeBytes` | How large the cache can grow in bytes before the Index evicts least-recently-accessed items | `536870912`
`caches.index.maxSizeBackoffBytes` | How far below max_size_bytes the cache size must be to complete a byte-size-based eviction exercise | `16777216`
`caches.index.maxSizeObjects` | How large the cache can grow in objects before the Index evicts least-recently-accessed items | `0`
`caches.index.maxSizeBackoffObjects` | How far under max_size_objects the cache size must be to complete object-size-based eviction exercise | `100`
`caches.redis.clientType` | redis architecture to use ('standard', 'cluster', or 'sentinel') | `standard`
`caches.redis.protocol` | The protocol for connecting to redis ('unix' or 'tcp') | `tcp`
`caches.redis.endpoint` | The fqdn+port or path to a unix socket file for connecting to redis | `redis:6379`
`caches.redis.endpoints` | Used for Redis Cluster and Redis Sentinel to define a list of endpoints | `['redis:6379']`
`caches.redis.password` | Password provides the redis password | `''`
`caches.redis.sentinelMaster` | Should be set when using Redis Sentinel to indicate the Master Node | `''`
`caches.redis.db` | The Database to be selected after connecting to the server | `"0"`
`caches.redis.maxRetries` | The maximum number of retries before giving up on the command | `"0"`
`caches.redis.minRetryBackoffMs` | The minimum backoff time between each retry | `"8"`
`caches.redis.maxRetyBackoffMs` | The maximum backoff time between each retry | `"512"`
`caches.redis.dialTimeoutMs` | The timeout for establishing new connections | `"5000"`
`caches.redis.readTimeoutMs` | The timeout for socket reads. If reached, commands will fail with a timeout instead of blocking | `"3000"`
`caches.redis.writeTimeoutMs` | The timeout for socket writes. If reached, commands will fail with a timeout instead of blocking | `"3000"`
`caches.redis.poolSize` | The maximum number of socket connections | `"20"`
`caches.redis.minIdleConns` | The minimum number of idle connections which is useful when establishing new connection is slow | `"0"`
`caches.redis.maxConnAgeMs` | The connection age at which client retires (closes) the connection | `"0"`
`caches.redis.poolTimeoutMs` | The amount of time client waits for connection if all connections are busy before returning an error | `"4000"`
`caches.redis.idleTimeoutMs` | The amount of time after which client closes idle connections | `"300000"`
`caches.redis.idleCheckFrequencyMs` | The frequency of idle checks made by idle connections reaper | `"60000"`
`caches.filesystem.path` | The directory location under which the Trickster filesystem cache will be maintained | `/tmp/trickster`
`caches.boltdb.file` | The filename of the BoltDB database | `trickster.db`
`caches.boltdb.bucket` | The name of the BoltDB bucket | `trickster`
`origins.name` | Identifies the name of the cache (configured above) that you want to use with this origin proxy. | `default`
`origins.isDefault` | Describes whether this origin is the default origin considered when routing http requests | `true`
`origins.type` | The origin type. Valid options are 'prometheus', 'influxdb', 'irondb' | `prometheus`
`origins.scheme` | The scheme | `http`
`origins.host` | The upstream origin by fqdn/IP and port  | `'prometheus:9090'`
`origins.pathPrefix` | Provides any path that is prefixed onto the front of the client's requested path | `''`
`origins.timeoutSecs` | Defines how many seconds Trickster will wait before aborting and upstream http request | `"180`
`origins.keepAliveTimeoutSecs` | Defines how long Trickster will wait before closing a keep-alive connection due to inactivity | `"300"`
`origins.maxIdleConns` | The maximum concurrent keep-alive connections Trickster may have opened to this origin | `"20"`
`origins.apiPath` | The path of the Upstream Origin's API | `/api/v1`
`origins.ignoreNoCacheHeader` | Disables a client's ability to send a no-cache to refresh a cached query | `false`
`origins.timeseriesRetentionFactor` | The maximum number of recent timestamps to cache for a given query | `"1024"`
`origins.timeseriesEvictionMethod` | The metholodogy used to determine which timestamps are removed once the timeseries_retention_factor limit is reached ('oldest', 'lru') | `"oldest"`
`origins.fastForwardDisable` | When set to true, will turn off the 'fast forward' feature for any requests proxied to this origin | `false`
`origins.backfillToleranceSecs` | Prevents new datapoints that fall within the tolerance window (relative to time.Now) from being cached | `"0"`
`logLevel` | The verbosity of the logger. Possible values are 'debug', 'info', 'warn', 'error'. | `info`
`replicaCount` | Number of trickster replicas desired | `1`
`resources` | Pod resource requests & limits | `{}`
`service.annotations` | Annotations to be added to the Trickster Service | `{}`
`service.clusterIP` | Cluster-internal IP address for Trickster Service | `""`
`service.externalIPs` | List of external IP addresses at which the Trickster Service will be available | `[]`
`service.loadBalancerIP` | External IP address to assign to Trickster Service | `""`
`service.loadBalancerSourceRanges` | List of client IPs allowed to access Trickster Service | `[]`
`service.metricsPort` | Port used for exporting Trickster metrics | `8080`
`service.nodePort` | Port to expose Trickster Service on each node | ``
`service.metricsNodePort` | Port to expose Trickster Service metrics on each node | ``
`service.port` | Trickster's Service port | `9090`
`service.type` | Trickster Service type | `ClusterIP`
`tolerations` | Tolerations for pod assignment | `[]`
