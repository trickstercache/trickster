# Range Request Support

Trickster's HTTP Reverse Proxy Cache offers best-in-class acceleration and caching of Range Requests.

Much like its Time Series Delta Proxy Cache, Trickster's Reverse Proxy Cache will determine what ranges are cached, and only request from the origin any uncached ranges needed to service the client request, reconstituting the ranges within the cache object. This ensures minimal response time in the event of a cache miss.

In addition to supporting basic single-Range requests (`Range: bytes=0-5`) Trickster also supports Multipart Range Requests (`Range: bytes=0-5, 10-20`).

In the event that an upstream does not support Multipart Range Requests, Trickster offers a unique feature called Upstream Range Dearticulation, that will separate any ranges needed from the origin into individual, parallel HTTP requests, which are reconstituted by Trickster. This feature can be enabled for an origin by setting `dearticulate_upstream_ranges = true`, as in this example:

```toml
[origins]
    [origins.default]
    origin_type = 'reverseproxycache'
    origin_url = 'http://example.com/'
    dearticulate_upstream_ranges = true
```

In the event that downstream clients should not expect MultiPart Range Request support, Trickster offers a setting to fully disable support on a per-origin basis. Set `multipart_ranges_disabled = true` and Trickster will strip Range Request headers that include multiple Ranges, which will result in a 200 OK response with the full body.
