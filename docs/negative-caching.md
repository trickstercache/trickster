# Negative Caching

Negative Caching means to cache undesired HTTP responses for a very short period of time, in order to prevent overwhelming a system that would otherwise scale normally when desired, cacheable HTTP responses are being returned. For example, Trickster can be configured to cache `404 Not Found` or `500 Internal Server Error` responses for a short period of time, to ensure that a thundering herd of HTTP requests for a non-existent object, or unexpected downtime of a citical service, do not create an i/o bottleneck in your application pipeline.

Trickster supports negative caching of any status code >= 300 and < 600, on a per-Origin basis. In your Trickster configuration file, associate the desired Negative Cache Map to the desired Origin config. See the [example.conf](../cmd/trickster/conf/example.conf), or refer to the snippet below for more information.

The Negative Cache Map must be an all-inclusive list of explicit status codes; there is currently no wildcard or status code range support for Negative Caching entries. By default, the Negative Cache Map is empty for all origin configs. The Negative Cache only applies to Cacheable Objects, and does not apply to Proxy-Only configurations.

For any response code handled by the Negative Cache, the response object's effective cache TTL is explicitly overridden to the value of that code's Negative Cache TTL, regardless of any response headers provided by the Origin concerning cacheability. All response headers are left in-tact and unmodified by Trickster's Negative Cache, such that Negative Caching is transparent to the client. The `X-Trickster-Result` response header will indicate a response was served from the Negative Cache by providing a cache status of `nchit`.

You can define multiple negative cache configurations, and reference them by name in the origin config. By Default, an origin will use the 'default' Negative Cache config, which, by default is empty. The default can be easily populated in the config file, and additionl configs can easily be added, as demonstrated below.

## Example Negative Caching Config

```toml

[negative_caches]
    [negative_caches.default]
    404 = 3 # cache 404 responses for 3 seconds

    [negative_caches.foo]
    404 = 3
    500 = 5
    502 = 5

[origins]
    [origins.default]
    provider = 'rpc'
    # by default will assume negative_cache_name = 'default'
    
    [origins.another]
    provider = 'rpc'
    negative_cache_name = 'foo'
```
