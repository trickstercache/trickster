# Customizing HTTP Path Behavior

Trickster supports, via configuration, customizing the upstream request and downstream response behavior on a per-Path, per-Origin basis, by providing a `paths` configuration section for each origin configuration. Here are the basic capabilities for customizing Path behavior:

- Modify client request headers prior to contacting the origin while proxying
- Modify origin response headers prior to processing the response object in Trickster and delivering to the client
- Modify the response code and body
- Limit the scope of a path by HTTP Method
- Select the HTTP Handler for the path (`proxy`, `proxycache` or a published origin-type-specific handler)
- Select which HTTP Headers, URL Parameters and other client request characteristics will be used to derive the Cache Key under which Trickster stores the object.
- Disable Metrics Reporting for the path

## Path Matching Scope

Paths are matchable as `exact` or `prefix`

The default match is `exact`, meaning the client's requested URL Path must be an exact match to the configured path in order to match and be handled by a given Path Config. For example a request to `/foo/bar` will not match an `exact` Path Config for `/foo`.

A `prefix` match will match any client-requested path to the Path Config with the longest prefix match. A `prefix` match Path Config to `/foo` will match `/foo/bar` as well as `/foobar` and `/food`. A basic string match is used to evaluate the incoming URL path, so it is recommended to consider finishing paths with a trailing `/`, like `/foo/` in Path Configurations, if needed to avoid any unintentional matches.

### Method Matching Scope

The `methods` section of a Path Config takes a string array of HTTP Methods that are routed through this Path Config. You can provide `[ '*' ]` to route all methods for this path.

## Suggested Use Cases

- Redirect a path by configuring Trickster to respond with a `302` response code and a `Location` header
- Issue a blanket `401 Unauthorized` code and custom response body to all requests for a given path.
- Adjust Cache Control headers in either direction
- Affix an Authorization header to requests proxied out by Trickster.
- Control which paths are cached by Trickster, and which ones are simply proxied.

## Header Behavior

### Basics

You can specify request and response headers be Set, Appended or Removed.

#### Setting

To Set a header means to insert if non-existent, or fully replace if pre-existing. To set a header, provide the header name and value you wish to set in the Path Config `request_headers` or `response_headers` sections, in the format of `'Header-Name' = 'Header Value'`.

As an example, if the client request provides a `Cache-Control: no-store` header, a Path Config with a header 'set' directive for `'Cache-Control' = 'no-transform'` will replace the `no-store` entirely with a `no-transform`; client requests that have no `Cache-Control` header that are routed through this Path will have the Trickster-configured header injected outright.

#### Appending

Appending a header means inserting the header if it doesn't exist, or appending the configured value(s) into a pre-existing header with the given name. To indicate an append behavior (as opposed to set), prefix the header name with a '+' in the Path Config.

Example: if the client request provides a `Cache-Control: no-store` header and the Path Config includes the request header `'+Cache-Control' = 'no-transform'`, the effective header when forwarding the request to the origin will be `Cache-Control: no-store; no-transform`.

#### Removing

Removing a header will strip it from the HTTP Response when present. To do so, prefix the header name with '-'. As an example: `-Cache-control: none`. When removing headers, a value is required to conform to TOML specifications, however, this value is innefectual. Note that there is currently no ability to remove a specific header value from a specific header - only the entire removal header. Consider setting the header value outright as described above, to strip any unwanted values.

#### Response Header Timing

Response Header injections occur as the object is received from the origin and before Trickster handles the object, meaning any caching response headers injected by Trickster will also be used by Trickster immediately to handle caching policies internally. This allows users to override cache controls from upstream systems if necessary to alter the actual caching behavior inside of Trickster. For example, InfluxDB sends down a `Cache-Control: No-Cache` header, which is fine for the user's browser, but Trickster needs to ignore this header in order to accelerate InfluxDB; so the default Path Configs for InfluxDB actually removes this header.

### Cache Key Components

By default, Trickster will use the HTTP Method, URL Path and any Authorization header to derive its Cache Key. In a Path Config, you may specify any additional HTTP headers and URL Parameters to be used for cache key derivation.

## Example Reverse Proxy Cache Config with Path Customizations

```toml
[origins]

    [origins.default]
    origin_type = 'rpc'

        [origins.default.paths]

            # root path '/'. Paths must be uniquely named but the
            # name is otherwise unimportant
            [origins.default.paths.root]
            path = '/' # each path must be unique for the origin
            methods = [ '*' ] # All HTTP methods applicable to this config
            match_type = 'prefix' # matches any path under '/'
            handler = 'proxy' # proxy only, no caching (this is the default)
            
                # When a user requests a path matching this route, Trickster will
                # inject these headers into the request before contacting the Origin
                [origins.default.paths.root.request_headers]
                'Cache-Control' = 'No-Transform' # Due to hyphens, quote the key name

                # inject these headers into the response from the Origin
                # before replying to the client
                [origins.default.paths.root.response_headers]
                'Expires' = '-1'

            [origins.default.paths.images]
            path = '/images/'
            methods = [ 'GET' ]
            handler = 'proxycache' # Trickster will cache the images directory
            match_type = 'prefix'
            
                [origins.default.paths.images.response_headers]
                'Cache-Control' = 'max-age=2592000' # cache for 30 days

            # but only cache this rotating image for 30 seconds
            [origins.default.paths.images_rotating]
            path = '/images/rotating.jpg'
            methods = [ 'GET' ]
            handler = 'proxycache'
            match_type = 'exact'

                [origins.default.paths.images_rotating.response_headers]
                'Cache-Control' = 'max-age=30'
                '-Expires' = '

            # redirect this sunsetted feature to a discontinued message
            [origins.default.paths.redirect]
            path = '/blog'
            methods = [ 'GET' ]
            handler = 'localresponse'
            match_type = 'prefix'
            response_code = 302

                [origins.default.paths.redirect.response_headers]
                Location = '/discontinued'

            # cache this API endpoint, keying on the query parameter
            [origins.default.paths.api]
            path = '/api/'
            methods = [ 'GET', 'POST' ]
            handler = 'proxycache'
            match_type = 'prefix'
            cache_key_params = [ 'query' ]
```

## Modifying Behavior of Time Series Origin Types

Each of the Time Series Origin Types supported in Trickster comes with its own custom handlers and pre-defined Path Configs that are registered with the HTTP Router when Trickster starts up.

For example, when Trickster is configured to accelerate Prometheus, pre-defined Path Configs are registered to control how requests to `/api/v1/query` work differently from requests to `/api/v1/query_range`. For example, the `/ap1/v1/query` Path Config uses the `query` and `time` URL query qarameters when creating the cache key, and is routed through the Object Proxy Cache; while the `/api/v1/query_range` Path Config uses the `query`, `start`, `end` and `step` parameters, and is routed through the Time Series Delta Proxy Cache.

In the Trickster config file, you can add your own Path Configs to your time series origin, as well override individual settings for any of the pre-defined Path Configs, and those custom settings will be applied at startup.

To know what configs you'd like to add or modify, take a look at the Trickster source code and examine the pre-definitions for the selected Origin Type. Each supported Origin Type's handlers and default Path Configs can be viewed under `/internal/proxy/origins/<origin_type>/routes.go`. These files are in a standard format that are quite human-readable, even for a non-coder, so don't be too intimidated. If you can understand Path Configs as TOML, you can understand them as Go code.

Examples of customizing Path Configs for Origin Types with Pre-Definitions:

```toml
[origins]

    [origins.default]
    origin_type = 'prometheus'

        [origins.default.paths]

            # route /api/v1/label* (including /labels/*)
            # through Proxy instead of ProxyCache as pre-defined
            [origins.default.paths.label]
            path = '/api/v1/label'
            methods = [ 'GET' ]
            match_type = 'prefix'
            handler = 'proxy'
            
            # route fictional new /api/v1/coffee to ProxyCache
            [origins.default.paths.series_range]
            path = '/api/v1/coffee'
            methods = [ 'GET' ]
            match_type = 'prefix'
            handler = 'proxycache'
            cache_key_params = [ 'beans', ']
            
            # block /api/v1/admin/ from being reachable via Trickster
            [origins.default.paths.admin]
            path = '/api/v1/admin/'
            methods = [ 'GET', 'POST', 'PUT', 'HEAD', 'DELETE', 'OPTIONS' ]
            match_type = 'prefix'
            handler = 'localresponse'
            response_code = 401
            response_body = 'No soup for you!'
            no_metrics = true
```
