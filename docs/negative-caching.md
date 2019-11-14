# Negative Caching

Negative Caching means to cache undesired HTTP responses for a very short period of time, in order to prevent overwhelming a system that would otherwise scale normally when desired, cacheable HTTP responses are being returned. For example, Trickster can be configured to cache `404 Not Found` or `500 Internal Server Error` responses for a short period of time, to ensure that a thundering herd of HTTP requests for a non-existent object, or unexpected downtime of a citical service, do not create an i/o bottleneck in your application pipeline.

Trickster supports negative caching of any status code >= 300 and < 600, on a per-Origin basis. In your Trickster configuration file, add the desired Negative Cache Map to the desired Origin config. The format of the Negative Cache Map is `status_code = ttl_in_secs` such as `404 = 30`. See the [example.conf](../cmd/trickster/conf/example.conf), or refer to the snippet below for more information.

The Negative Cache Map must be an all-inclusive list of explicit status codes; there is currently no wildcard or status code range support for Negative Caching entries. By default, the Negative Cache Map is empty for all origin configs. The Negative Cache only applies to Cacheable Objects, and does not apply to Timeseries-Accelerated Requests via the Delta Proxy Cache engine, or to Proxy-Only configurations.

For any response code handled by the Negative Cache, the response object's effective cache TTL is explicitly overridden to the value of that code's Negative Cache TTL, regardless of any response headers provided by the Origin concerning cacheability. All response headers are left in-tact and unmodified by Trickster's Negative Cache, such that Negative Caching is transparent to the client. Trickster currently does not insert any response headers or information indicating to downstream clients that the response was served from the Negative Cache.

## Example Negative Caching Config

```toml
[origins]

    [origins.default]
    origin_type = 'rpc'

        [origins.default.negative_cache]
        404 = 10    # Cache 404's for 10 seconds
        500 = 10    # Cache 500's for 10 seconds
```
