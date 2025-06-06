# Chunked Caching

## Overview
In some caching setups, users may want to increase `timeseries_retention_factor` or `timeseries_ttl` forms a given backend to a very large size (e.g., a duration of days or weeks). This can cause issues if the cache provider is
`filesystem` or `redis`, because the entire time series is loaded to extract
even just a few data points. Eventually, this could negate the effects of
caching altogether.

To mitigate this, Trickster supports *chunking* cache data, by splitting large datasets into subdivisions of a configurable maximum size. The chunks are reconstituted upon retrieval. Only the chunks needed to service a client request are accessed, rather than the entire time series cache object.

Chunking can be configured per-cache and applies to both timeseries and byterange data.

## Configuration

Chunked caching can be enabled and disabled using `use_cache_chunking`:

```yaml
fs1:
    cache_type: filesystem
    provider: filesystem
    use_cache_chunking: true
    timeseries_chunk_factor: 420
    byterange_chunk_size: 4096
```

`timeseries_chunk_factor` determines the maximum extent of timerange chunks, and `byterange_chunk_size` determines the maximum size of byterange chunks. See [Detail](#detail) for more information.

## Detail

### Timeseries

Timeseries chunking splits the timeseries to be cached into parts with the same *duration*, but not necessarily the same literal size.

- Determine a *chunk duration* by multiplying the timerange step by `timerange_chunk_factor` (default 420)
- Determine the smallest possible extent that is aligned to the epoch along the chunk duration, while containing the entire timeseries
- To write: Write each chunk size subextent under a subkey
- To read: Read each subkey and merge the timeseries results

### Byterange

Byterange chunking splits the byterange into pieces with the same literal size. There are also some extra steps compared to the timeseries implementation to preserve the integrity of both full and partial responses being cached.

- Determine a *chunk size* from `byterange_chunk_size` (default 4096)
- Determine a range from the cache read/write request using the provided range or content length
- Failure to determine a range on write results in an error
- Failure to determine a range on read will read until the query fails
- Determine a maximum range aligned along the chunk size that contains the entire byterange
- To write: Write each chunk size range with `RangeParts` of all provided ranges cropped to that chunk range, under a subkey
- To read: Read each subkey and reconstitute a body from `RangeParts`, if able

## Full Example

This example has one Prometheus backend with a filesystem cache that has chunking *disabled*, and one InfluxDB backend with a memory cache that has chunking *enabled*. The memory cache uses 380 as its timeseries chunk factor, and doesn't define a byterange chunk size, so the default of 4096 will be used.

```yaml
frontend:
  listen_port: 8480

negative_caches:
  default:
    '400': 3s
    '404': 3s
    '500': 3s
    '502': 3s

caches:
  mem1:
    cache_type: memory
    provider: memory
    index:
      max_size_objects: 512
      max_size_backoff_objects: 128
    use_cache_chunking: true
    timeseries_chunk_factor: 380
    # byterange_chunk_size: 4096
  fs1:
    cache_type: filesystem
    provider: filesystem

request_rewriters:

  remove-accept-encoding:
    instructions:
      - [ header, delete, Accept-Encoding ]

  range-to-instant:
    instructions:
      - [ path , set , /api/v1/query ]
      - [ param , delete , start ]
      - [ param , delete , end ]
      - [ param , delete , step ]
      - [ chain , exec , remove-accept-encoding ]

rules:
  example:
    input_source: header
    input_key: Authorization
    input_type: string
    input_encoding: base64
    input_index: 1
    input_delimiter: ' '
    operation: prefix
    next_route: rpc1
    cases:
      '1':
        matches:
          - 'james:'
        next_route: sim1

tracing:
  jc1:
    tracer_type: otlp
    endpoint: 'http://127.0.0.1:14268/api/traces'
    tags:
      testTag: testTagValue
      testTag2: testTag2Value
  ja1:
    tracer_type: otlp
    endpoint: '127.0.0.1:6831'
    omit_tags:
      - http.url

backends:
  prom1:
    latency_max: 150ms
    latency_min: 50ms
    provider: prometheus
    origin_url: 'http://127.0.0.1:9090'
    cache_name: fs1
    tls:
      full_chain_cert_path: >-
        /private/data/trickster/docker-compose/data/trickster-config/127.0.0.1.pem
      private_key_path: >-
        /private/data/trickster/docker-compose/data/trickster-config/127.0.0.1-key.pem
      insecure_skip_verify: true
  influx:
    provider: influxdb
    origin_url: 'http://127.0.0.1:8086'
    cache_name: mem1
    backfill_tolerance: 30s
    timeseries_retention_factor: 5184000

logging:
  log_level: warn

metrics:
  listen_port: 8481
```
