# Chunked Caching

## Overview

In some use cases, it may be desirable to reduce the amount of data being transmitted from the cache for small requests. Trickster accomplishes this by *chunking* cache data, or splitting it into subdivisions of a configurable maximum size. Chunking can be configured per-cache and applies to both timeseries and byterange data.

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
- To read: Read each subkey and reconsitute a body from `RangeParts`, if able
