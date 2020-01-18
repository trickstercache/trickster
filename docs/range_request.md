# Byte Range Request Support

Trickster's HTTP Reverse Proxy Cache offers best-in-class acceleration and caching of Byte Range Requests.

Much like its Time Series Delta Proxy Cache, Trickster's Reverse Proxy Cache will determine what ranges are cached, and only request from the origin any uncached ranges needed to service the client request, reconstituting the ranges within the cache object. This ensures minimal response time in the event of a cache miss.

In addition to supporting basic single-range requests (`Range: bytes=0-5`) Trickster also supports Multipart Range Requests (`Range: bytes=0-5, 10-20`).

In the event that an upstream origin does not support Multipart Range Requests, Trickster will enable that support. To do so, Trickster offers a unique feature called Upstream Range Dearticulation, that will separate any ranges needed from the origin into individual, parallel HTTP requests, which are reconstituted by Trickster. This feature can be enabled for any origin that only supports single-range requests, by setting the origin configuration value `dearticulate_upstream_ranges = true`, as in this example:

```toml
[origins]
    [origins.default]
    origin_type = 'reverseproxycache'
    origin_url = 'http://example.com/'
    dearticulate_upstream_ranges = true
```

In the event that downstream clients should not expect MultiPart Range Request support, Trickster offers a setting to fully disable support on a per-origin basis. Set `multipart_ranges_disabled = true`, as in the below example, and Trickster will strip Range Request headers that include multiple Ranges, which will result in a 200 OK response with the full body. Single-range requests are unaffected by this setting.

```toml
[origins]
    [origins.default]
    origin_type = 'reverseproxycache'
    origin_url = 'http://example.com/'
    multipart_ranges_disabled = true
```

## RangeSim

For verification of Trickster's compatibility with Byte Range Requests, we created a golang library and accompanying standalone application dubbed RangeSim. RangeSim simply prints out the `Lorem ipsum ...` sample text, pared down to the requested range or multipart ranges, with a few bells and whistles to allow you to customize its response for unit testing purposes. We make extensive use of RangeSim in unit testing to verify the integrity of Trickster's output after performing operations like merging disparate range parts, extracting ranges from other ranges, or from a full body, compressing adjacent ranges into a single range in the cache, etc.

You can check out RangeSim in the codebase at /cmd/rangesim and /pkg/rangesim. It is fairly straightforward to run or import into your own applications. For examples of using RangeSim for Unit Testing, see /internal/proxy/engines/objectproxycache_test.go
