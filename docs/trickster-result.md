# X-Trickster-Result Header

Trickster adds the `X-Trickster-Result` response header to describe how it handled a request. The header is intended for debugging cache behavior, proxy fallbacks, and partial origin fetches.

Example:

```http
X-Trickster-Result: engine=DeltaProxyCache; status=phit; fetched=[1612804980000-1612808580000]; ffstatus=hit
```

The header value is a semicolon-separated list of fields. Optional fields are included only when Trickster has a value for them.

| Field | Description |
| ----- | ----- |
| `engine` | The proxy engine that handled the response, such as `HTTPProxy`, `ObjectProxyCache`, or `DeltaProxyCache`. |
| `status` | The cache or proxy result. See [Cache Status](./caches.md#cache-status). |
| `fetched` | Time ranges fetched from the origin to satisfy the response. Ranges are formatted as `start-end`; multiple ranges are separated by semicolons inside the brackets. |
| `ffstatus` | Fast Forward cache result for time series requests. Possible values are `hit`, `miss`, `off`, or `err`. |
| `failed` | Time ranges that Trickster attempted to fetch but could not fetch successfully. This usually appears with `proxy-error` or partial fanout failures. |

## Result Statuses

`status` uses the same values reported in metrics, logs, and tracing. Common examples are:

| Status | Meaning |
| ----- | ----- |
| `hit` | The response was served fully from cache. |
| `phit` | Part of the response was served from cache and part was fetched from the origin. |
| `kmiss` | Trickster had no object for the cache key and fetched the response from the origin. |
| `rmiss` | Trickster had an object for the cache key, but not for the requested range. |
| `rhit` | Trickster revalidated a stale cached object against the origin and served it as a hit. |
| `nchit` | The response was served from the Negative Cache. |
| `purge` | The cache key was purged as directed by a request or response header. |
| `proxy-hit` | The request joined an in-flight origin fetch for the same cache key. |
| `proxy-only` | The request was proxied to the origin without writing or reading a cache object. |
| `proxy-error` | An upstream request needed for the response returned an error. |
| `error` | Trickster encountered a cache lookup or cache handling error. |

## Proxy-Only Results

`status=proxy-only` means the response came from the origin through Trickster's proxy path, without a cache read or cache write for that request. It does not necessarily mean the origin response was wrong.

Common reasons include:

| Cause | Example |
| ----- | ----- |
| Backend or path is configured to bypass caching | `provider: reverseproxy` or `proxy_only: true`. |
| Client request prevents caching | A request such as `Cache-Control: no-cache` can force Trickster to proxy and remove the existing object for that cache key. |
| Origin response is not cacheable | For example, response cache headers do not provide cacheability, or the response includes headers that Trickster treats as not cacheable. |
| Time series request cannot be parsed for delta caching | Trickster may fall back to object proxy cache for compatible requests, or proxy the request directly when it cannot safely cache the query shape. |
| Time series range is outside the retained cache window | Old data may be proxied without caching while newer ranges remain cacheable. |

When investigating `proxy-only`, check the backend provider, any `proxy_only` setting, request and response `Cache-Control` headers, and whether the request shape is supported by the configured backend provider.

## Fetched And Failed Ranges

`fetched` and `failed` describe the ranges Trickster fetched or failed to fetch while serving the response.

Example:

```http
X-Trickster-Result: engine=DeltaProxyCache; status=phit; fetched=[1612804980000-1612808580000;1612812180000-1612815780000]
```

For time series responses, range values are Unix timestamps in milliseconds. A `phit` result with `fetched` ranges usually means Trickster had some of the requested data cached and fetched the missing ranges from the origin.

`failed` ranges indicate the origin request for those ranges failed. Depending on the proxy engine and fanout behavior, Trickster may return an error response or a partial response with failure metadata.

## Fast Forward Status

`ffstatus` appears on time series responses when the Delta Proxy Cache checks Fast Forward data:

| Fast Forward Status | Meaning |
| ----- | ----- |
| `hit` | Fast Forward data was served from cache. |
| `miss` | Fast Forward data was fetched from the origin. |
| `off` | Fast Forward was not attempted for this request. |
| `err` | Fast Forward was attempted but failed or returned unusable data. |

Fast Forward is only relevant for supported time series backends and only when the request is eligible for the latest datapoint optimization.
