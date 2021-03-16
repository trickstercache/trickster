# Customizing HTTP Path Behavior

Trickster supports, via configuration, customizing the upstream request and downstream response behavior on a per-Path, per-Backend basis, by providing a `paths` configuration section for each backend configuration. Here are the basic capabilities for customizing Path behavior:

- Modify client request headers prior to contacting the origin while proxying
- Modify origin response headers prior to processing the response object in Trickster and delivering to the client
- Modify the response code and body
- Limit the scope of a path by HTTP Method
- Select the HTTP Handler for the path (`proxy`, `proxycache` or a published provider-specific handler)
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

## Request Rewriters

You can configure paths send inbound requests through a request rewriter that can modify any aspect of the inbound request (method, url, headers, etc.), before being processed by the path route. This means, when the path route inspects the request, it will have already been modified by the rewriter. Provide a rewriter with the `req_rewriter_name` config. It must map to a named/configured request rewriter (see [request rewriters](./request_rewriters.md) for more info). Note, you can also send requests through a rewriter at the backend level. If both are configured, backend-level rewriters are executed before path rewriters are.

```yaml
request_rewriters:
  # this example request rewriter adds an additional header to the request
  # you can include as many instructions in rewriter as required
  example:
    instructions:
      - [ header, set, Example-Header-Name, Example Value ]

backends:
  default:
    provider: rpc
    origin_url: 'http://example.com'
    paths:
      root:
        path: /
        req_rewriter_name: example

```

## Header and Query Parameter Behavior

In addition to running the request through a named rewriter, it is currently possible to make similar changes to the request with legacy path features that are described in this section. Note that these are likely to be deprecated in a future Trickster release, in favor of the more versatile named rewriters described above, which accomplish the same thing. Currently, if both a named rewriter and legacy path-based rewriting configs are defined for a given path, the named rewriter will be executed first.

### Basics

You can specify request query parameters, as well as request and response headers, to be Set, Appended or Removed.

#### Setting

To Set a header or parameter means to insert if non-existent, or fully replace if pre-existing. To set a header, provide the header name and value you wish to set in the Path Config `request_params`, `request_headers` or `response_headers` sections, in the format of `'Header-or-Parameter-Name' = 'Value'`.

As an example, if the client request provides a `Cache-Control: no-store` header, a Path Config with a header 'set' directive for `'Cache-Control' = 'no-transform'` will replace the `no-store` entirely with a `no-transform`; client requests that have no `Cache-Control` header that are routed through this Path will have the Trickster-configured header injected outright. The same logic applies to query parameters.

#### Appending

Appending a means inserting the header or parameter if it doesn't exist, or appending the configured value(s) into a pre-existing header with the given name. To indicate an append behavior (as opposed to set), prefix the header or parameter name with a '+' in the Path Config.

Example: if the client request provides a `token=SomeHash` parameter and the Path Config includes the parameter `'+token' = 'ProxyHash'`, the effective parameter when forwarding the request to the origin will be `token=SomeHash&token=ProxyHash`.

#### Removing

Removing a header or parameter means to strip it from the HTTP Request or Response when present. To do so, prefix the header/parameter name with '-', for example, `-Cache-control: none`. When removing headers, a value is required to be provided in order to conform to YAML specification; this value, however, is ineffectual. Note that there is currently no ability to remove a specific header value from a specific header - only the entire removal header. Consider setting the header value outright as described above, to strip any unwanted values.

#### Response Header Timing

Response Header injections occur as the object is received from the origin and before Trickster handles the object, meaning any caching response headers injected by Trickster will also be used by Trickster immediately to handle caching policies internally. This allows users to override cache controls from upstream systems if necessary to alter the actual caching behavior inside of Trickster. For example, InfluxDB sends down a `Cache-Control: No-Cache` header, which is fine for the user's browser, but Trickster needs to ignore this header in order to accelerate InfluxDB; so the default Path Configs for InfluxDB actually removes this header.

### Cache Key Components

By default, Trickster will use the HTTP Method, URL Path and any Authorization header to derive its Cache Key. In a Path Config, you may specify any additional HTTP headers and URL Parameters to be used for cache key derivation, as well as information in the Request Body.

#### Using Request Body Fields in Cache Key Hashing

Trickster supports the parsing of the HTTP Request body for the purpose of deriving the Cache Key for a cacheable object. Note that body parsing requires reading the entire request body into memory and parsing it before operating on the object. This will result in slightly higher resource utilization and latency, depending upon the size of the client request body.

 Body parsing is supported when the request's HTTP method is `POST`, `PUT` or `PATCH`, and the request `Content-Type` is either `application/x-www-form-urlencoded`, `multipart/form-data`, or `application/json`.

In a Path Config, provide the `cache_key_form_fields` setting with a list of form field names to include when hashing the cache key.

Trickster supports parsing of the Request body as a JSON document, including documents that are multiple levels deep, using a basic pathing convention of forward slashes, to indicate the path to a field that should be included in the cache key. Take the following JSON document:

```json
{
    "requestType": "query",
    "query": {
        "table": "movies",
        "fields": "eidr,title",
        "filter": "year=1979"
    }
}
```

To include the `requestType`, `table`, `fields`, and `filter` fields from this document when hashing the cache key, you can provide the following setting in a Path Configuration:

`cache_key_form_fields = [ 'requestType', 'query/table', 'query/fields', 'query/filter' ]`

## Example Reverse Proxy Cache Config with Path Customizations

```yaml
backends:
  default:
    provider: rpc
    paths:
      # root path '/'. Paths must be uniquely named but the
      # name is otherwise unimportant
      root:
        path: / # each path must be unique for the backend
        methods: [ '*' ] # All HTTP methods applicable to this config
        match_type: prefix # matches any path under '/'
        handler: proxy # proxy only, no caching (this is the default)
        # modify the query params en route to the origin; this adds authToken=secret_string
        request_params:
          authToken: secret string
        # When a user requests a path matching this route, Trickster will
        # inject these headers into the request before contacting the Origin
        request_headers:
          Cache-Control: No-Transform
        # inject these headers into the response from the Origin
        # before replying to the client
        response_headers:
          Expires: '-1'
      images:
        path: /images/
        methods:
          - GET
          - HEAD
        handler: proxycache # Trickster will cache the images directory
        match_type: prefix
        response_headers:
          Cache-Control: max-age=2592000 # cache for 30 days
      # but only cache this rotating image for 30 seconds
      images_rotating:
        path: /images/rotating.jpg
        methods:
          - GET
        handler: proxycache
        match_type: exact
        response_headers:
          Cache-Control: max-age=30
          '-Expires': ''
      # redirect this sunsetted feature to a discontinued message
      redirect:
        path: /blog
        methods:
          - '*'
        handler: localresponse
        match_type: prefix
        response_code: 302
        response_headers:
          Location: /discontinued
      # cache this API endpoint, keying on the query parameter
      api:
        path: /api/
        methods:
          - GET
          - HEAD
        handler: proxycache
        match_type: prefix
        cache_key_params:
          - query
      # same API endpoint, different HTTP methods to route against, which are denied
      api-deny:
        path: /api/
        methods:
          - POST
          - PUT
          - PATCH
          - DELETE
          - OPTIONS
          - CONNECT
        handler: localresponse
        match_type: prefix
        response_code: 401
        response_body: this is a read-only api endpoint
      # cache the query endpoint, permitting GET, HEAD, POST
      api-query:
        path: /api/query/
        methods:
          - GET
          - HEAD
          - POST
        handler: proxycache
        match_type: prefix
        cache_key_params:
          - query # for GET/HEAD
        cache_key_form_fields:
          - query # for POST
```

## Modifying Behavior of Time Series Backend

Each of the Time Series Providers supported in Trickster comes with its own custom handlers and pre-defined Path Configs that are registered with the HTTP Router when Trickster starts up.

For example, when Trickster is configured to accelerate Prometheus, pre-defined Path Configs are registered to control how requests to `/api/v1/query` work differently from requests to `/api/v1/query_range`. For example, the `/ap1/v1/query` Path Config uses the `query` and `time` URL query parameters when creating the cache key, and is routed through the Object Proxy Cache; while the `/api/v1/query_range` Path Config uses the `query`, `start`, `end` and `step` parameters, and is routed through the Time Series Delta Proxy Cache.

In the Trickster config file, you can add your own Path Configs to your time series backend, as well override individual settings for any of the pre-defined Path Configs, and those custom settings will be applied at startup.

To know what configs you'd like to add or modify, take a look at the Trickster source code and examine the pre-definitions for the selected Backend Provider. Each supported Provider's handlers and default Path Configs can be viewed under `/pkg/backends/<provider>/routes.go`. These files are in a standard format that are quite human-readable, even for a non-coder, so don't be too intimidated. If you can understand Path Configs as YAML, you can understand them as Go code.

Examples of customizing Path Configs for Providers with Pre-Definitions:

```yaml
backends:
  default:
    provider: prometheus
    paths:
      # route /api/v1/label* (including /labels/*)
      # through Proxy instead of ProxyCache as pre-defined
      label:
        path: /api/v1/label
        methods:
          - GET
        match_type: prefix
        handler: proxy
      # route fictional new /api/v1/coffee to ProxyCache
      series_range:
        path: /api/v1/coffee
        methods:
          - GET
        match_type: prefix
        handler: proxycache
        cache_key_params:
          - beans
      # block /api/v1/admin/ from being reachable via Trickster
      admin:
        path: /api/v1/admin/
        methods:
          - GET
          - POST
          - PUT
          - HEAD
          - DELETE
          - OPTIONS
        match_type: prefix
        handler: localresponse
        response_code: 401
        response_body: No soup for you!
        no_metrics: true
```
