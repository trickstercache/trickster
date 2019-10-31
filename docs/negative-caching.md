# Negative Caching

Negative Caching means to cache undesired HTTP responses for a very short period of time, in order to prevent overwhelming a system that would otherwise scale normally when desired, cacheable HTTP responses are being returned. For example, Trickster can be configured to cache `404 Not Found` or `500 Internal Server Error` responses for a short period of time, to ensure that a thundering herd of HTTP requests for a non-existent object, or unexpected downtime of a citical service, do not create an i/o bottleneck in your application pipeline.

Trickster supports negative caching of any status code >= 300 and < 600, on a per-Origin basis. In your Trickster configuration file, add the desired Negative Cache Map to the desired Origin config. The format of the Negative Cache Map is `status_code = ttl_in_secs` such as `404 = 30`. See the [example.conf](../cmd/trickster/conf/example.conf), or refer to the snippet below for more information.

By default, the Negative Cache Map is empty.

The Negative Cache Map must be an all-inclusive list of explicit status codes; there is currently no wildcard or status code range support for Negative Caching entries.

## Example Negative Caching Config

```toml
[origins]

    [origins.default]
    origin_type = 'rpc'

        [origins.default.negative_cache]
        404 = 10    # Cache 404's for 10 seconds
        500 = 10    # Cache 500's for 10 seconds
```
