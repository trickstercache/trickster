# Customizing HTTP Path Behavior

Trickster supports, via configuration, customizing the upstream request and downstream response behavior on a per-Path, per-Backend basis, by providing a `paths` configuration section for each backend configuration. Here are the basic capabilities for customizing Path behavior:

- Modify client request headers prior to contacting the origin while proxying
- Modify origin response headers prior to processing the response object in Trickster and delivering to the client
- Modify the response code and body
- Limit the scope of a path by HTTP Method
- Select the HTTP Handler for the path (`proxy`, `proxycache` or a published provider-specific handler)
- Select which HTTP Headers, URL Parameters and other client request characteristics will be used to derive the Cache Key under which Trickster stores the object.
- Disable Metrics Reporting for the path
- Hide the `X-Trickster-Result` response header from the client

## Path Matching Scope

Paths are matchable as `exact`, `prefix`, `segment` or `regex`

The default match is `exact`, meaning the client's requested URL Path must be an exact match to the configured path in order to match and be handled by a given Path Config. For example a request to `/foo/bar` will not match an `exact` Path Config for `/foo`.

A `prefix` match will match any client-requested path to the Path Config with the longest prefix match. A `prefix` match Path Config to `/foo` will match `/foo/bar` as well as `/foobar` and `/food`. A basic string match is used to evaluate the incoming URL path, so it is recommended to consider finishing paths with a trailing `/`, like `/foo/` in Path Configurations, if needed to avoid any unintentional matches.

A `segment` match is a prefix match that only matches on a path segment boundary: a `segment` Path Config for `/foo` matches `/foo`, `/foo/` and `/foo/bar`, but not `/foobar`. A `segment` path ending in `/`, like `/foo/`, matches `/foo/bar` but not `/foo`. Segment and prefix paths share one tier and are evaluated longest path first, so the two kinds can be mixed freely. This is the matching the Kubernetes Ingress `Prefix` and Gateway API `PathPrefix` types define.

A `segment` and a `prefix` path may name the same path; both are kept, and they are tried in the order described under Header and Query Parameter Conditions below, since neither is more specific than the other in the router's eyes. A plain `prefix` listed first therefore answers everything the `segment` would, leaving it unreached, so list the `segment` first when you want it to serve the paths on a boundary.

### Header and Query Parameter Conditions

A Path Config may also be conditioned on the request's headers and query parameters with `match_headers` and `match_query_params`. Each is a list of conditions with a `name`, a `value` and an optional `regex` flag. A condition is satisfied when the named header or parameter is present and its first value equals `value`, or, with `regex: true`, matches `value` as an [RE2](https://github.com/google/re2/wiki/Syntax) regular expression. The expression is not anchored, so anchor it (`^...$`) to require a whole-value match. A header or parameter that is absent never satisfies a condition, even one an empty value would satisfy; a header that is present and empty does. Header names are matched case-insensitively; parameter names are case-sensitive, as query parameters are.

These are match conditions and are distinct from `request_headers` and `request_params`, which modify the request after it has matched.

```yaml
paths:
  - path: /api/
    match_type: prefix
    match_headers:
      - name: X-Tenant
        value: gold
    handler: proxycache
    ...
  - path: /api/
    match_type: prefix
    match_query_params:
      - name: version
        value: '^v[0-9]+$'
        regex: true
    handler: proxy
    ...
  - path: /api/
    match_type: prefix
    handler: proxy
    ...
```

Conditioned paths on the same path and methods are tried in ascending `match_order`, then in the order they appear in the configuration, and the first one whose conditions the request satisfies handles it. A path with no conditions always matches, so it must rank last among the paths sharing its path and method: list it after them, or give it a higher `match_order`. `match_order` is what orders candidates that come from different backends, since backends are a map with no declaration order; equal orders across backends are tried in backend name order. A request satisfying none of the conditioned paths continues to the less specific tiers as if the path did not exist: a shorter prefix, a regex path, a less specific host, and finally a 404. A request for a path whose methods do not include the request method is answered 405 as always; conditions do not change that.

Compiled once at load, conditions cost nothing for paths that declare none: a router with no conditioned paths matches exactly as it did before they existed. The query string is parsed at most once per request, and only when a reachable conditioned path names a query parameter.

### Regex Paths

A path can also be a regular expression, evaluated against the client's requested URL Path. A path is treated as a regex when either:

- the path value starts with `^/` (or the escaped form `^\/`) — this is auto-detected and applies regardless of any configured `match_type`; or
- the Path Config explicitly sets `match_type: regex`, in which case the path need not start with `^/`; Trickster will prepend a `^` anchor if absent, so match semantics are consistent.

Regex paths use Go's [RE2 syntax](https://github.com/google/re2/wiki/Syntax) (linear-time matching; no backtracking). Patterns are always anchored at the start with `^`. A `$` end anchor is honored when present but never required — an unanchored end behaves like a prefix-style match, so `^/api/[0-9]+` matches `/api/42` and `/api/42/details` alike, while `^/api/[0-9]+$` matches only `/api/42`.

Evaluation order: regex paths are evaluated only after both `exact` and `prefix` matching have missed. Within the regex tier, patterns are evaluated longest-pattern-string first; equal-length patterns are evaluated in the order they appear in the configuration; the first pattern that matches wins.

### Provider Default Paths

Each backend provider defines its own default paths, which are merged with the `paths` you configure: a configured path replaces a default with the same path and methods, and any remaining defaults are kept. This is why a backend that configures only `/api` still answers every other path through its provider's catch-all `/` route.

Set `path_defaults_disabled: true` on a backend to register only its configured paths. The backend then answers exactly the paths it lists and returns 404 for everything else. This is how the Kubernetes controller keeps a generated backend to the paths its route declares, and it is also useful for narrowly scoping a backend by hand. A backend with defaults disabled and no configured paths serves nothing.

### Host Resolution

A backend's paths are matched either for the hostnames it lists in `hosts`, or, when it sets `any_host_routing: true`, for every hostname reaching its listeners. The two are mutually exclusive and configuring both fails validation. A backend that sets neither is reachable only through its `/backend_name/` path, an ALB, or a rule.

When a backend lists `hosts`, its paths are matched only for requests whose `Host` header (port excluded, compared case-insensitively) matches an entry. An entry may be an exact hostname, a single-label wildcard such as `*.example.com`, which matches `api.example.com` but not `example.com` or `a.b.example.com`, or an any-depth wildcard such as `**.example.com`, which matches `api.example.com` and `a.b.example.com` but still not `example.com`. A wildcard must be the leading label and nothing else in the entry may contain a `*`; a backend may not list both spellings for one domain.

The router resolves each request by host tier and stops at the first match: the exact hostname; then, at each label boundary of the hostname working outward, a `*.` wildcard covering the hostname (its first boundary only) before a `**.` wildcard at the same boundary; then the global routes, which are those of backends using `any_host_routing` or `is_default`. So for `a.b.example.com` the order is `a.b.example.com`, `*.b.example.com`, `**.b.example.com`, `**.example.com`, `**.com`, then global. Within each tier the path tiers run in the order above: exact, then longest prefix, then regex. So an exact host beats a wildcard host, a wildcard beats a global route, and a host-specific regex path beats a global exact path. Any-depth wildcards cost one map lookup per label boundary, and only when a backend registers one.

### Hiding the Result Header

Every response carries `X-Trickster-Result`, which says how the request was handled ([trickster-result.md](./trickster-result.md)). Set `hide_result_header: true` on a path to withhold it from the client, for a path facing the public where the cache behavior is nobody's business but the operator's. Metrics and the access log still record the result: the value is kept for the access log's `%{cache-status}x` and `%{engine}x` fields, so hiding it from clients does not blind the operator. The decision is the serving path's: when an ALB or rule path that hides the header dispatches to a backend whose path does not, the header is served, and only a response the dispatching path answered itself is withheld. The Kubernetes controller sets it from a `TricksterCachePolicy` with `resultHeader: Hide`.

```yaml
paths:
  - path: /
    match_type: prefix
    handler: proxycache
    hide_result_header: true
```

### Dispatch-Only Paths

A path with `dispatch_only: true` is registered on the backend's own router only, never on a listener. It is reachable when another backend hands the request over — an ALB selecting this backend from its pool, or a rule whose `next_route` names it — and is invisible to clients otherwise, even when the backend lists `hosts` or sets `any_host_routing`. Use it to keep the entry point for a path on one backend while the backend that finally serves it stays reachable only through that entry point. The Kubernetes controller relies on it: a route that another route's header match fronts keeps its path dispatch-only, so the rule that tests the header is the only thing registered on the listener.

**Catch-all warning:** a classic catch-all path — `match_type: prefix` on `/`, or an effective catch-all like `/api` in front of regexes that all begin `^/api` — always prefix-matches first and prevents the regex tier from ever being evaluated for those requests. When using regex paths, define the catch-all as a regex too (e.g. `^/.*`), which sorts shortest and therefore evaluates last. Trickster logs a startup warning when a backend defines regex paths alongside a `/` prefix catch-all.

```yaml
backends:
  default:
    provider: rpc
    origin_url: 'http://example.com'
    paths:
      # auto-detected as a regex path (starts with ^/)
      - path: '^/api/[0-9]+/results'
        methods: [ GET ]
        handler: proxycache
      # explicit opt-in; Trickster anchors this to ^/reports/(annual|monthly)/
      - path: '/reports/(annual|monthly)/'
        match_type: regex
        methods: [ GET ]
        handler: proxy
      # regex catch-all: evaluates last, does not shadow the regexes above
      - path: '^/.*'
        methods: [ '*' ]
        handler: proxy
```

### Timeouts and Retries

A backend's `timeout` bounds how long the origin may take to begin a
response and how long its body may stall. A path may bound more: `timeout`
is a deadline on the whole upstream exchange for requests on the path,
retries included, and `attempt_timeout` a deadline on each attempt; both
cancel the upstream request when reached, and `attempt_timeout` may not
exceed `timeout`. A `retry` block repeats a failed idempotent request
(`GET`, `HEAD`, `OPTIONS`, `TRACE`, `PUT`, `DELETE`, with a body only when
it can be replayed) up to `attempts` more times: always after a connection
failure, and after a response whose status is in `codes`. `backoff` waits
between attempts; a request the client abandoned is never retried. Retries
are drawn from a budget per path, `budget_percent` of the requests the path
saw in the last ten seconds with three always allowed, so an origin that
is down is not swamped by retries of everything that reaches it. Retried
requests are counted in `trickster_proxy_upstream_retries_total`. A `budget_percent` of `100` removes the budget, so every eligible request is retried; a request whose `timeout` or `attempt_timeout` runs out with no attempt answered is answered `504`, while an origin that cannot be reached at all is answered `502`.

```yaml
backends:
  default:
    provider: rpc
    origin_url: 'http://example.com'
    paths:
      - path: /api/
        match_type: prefix
        methods: [ GET, POST ]
        handler: proxycache
        timeout: 5s
        attempt_timeout: 2s
        retry:
          attempts: 2
          codes: [ 502, 503 ]
          backoff: 100ms
          budget_percent: 20
```

### Mirroring Requests

A path's `mirrors` list sends a copy of each request it serves to other
backends and discards the copies' responses; the client's response comes
from the path's own handler alone. A copy is served through the mirror
backend's router, so it follows that backend's paths and handlers, on its
own goroutine after the original has been handed to the path's handler; a
bodied request is buffered so every reader gets it. Each mirror's `percent`
selects the share of requests copied (all of them when unset) and its
`max_in_flight` bounds the copies in progress at once (64 when unset); a
copy the bound refuses is dropped. A copy is never mirrored again, however
the mirror backend's paths are configured. Copies are counted in
`trickster_proxy_mirror_requests_total` as `sent` or `dropped`. Every mirror
backend must exist and may not be a template.

```yaml
backends:
  default:
    provider: rpc
    origin_url: 'http://example.com'
    paths:
      - path: /api/
        match_type: prefix
        methods: [ '*' ]
        handler: proxy
        mirrors:
          - backend_name: shadow
            percent: 10
  shadow:
    provider: rp
    origin_url: 'http://shadow.example.com'
```

### Forwarding Trailers

The `proxy` handler proxies through the standard library's reverse proxy,
which relays response trailers. The caching handlers read a response in
full before serving it and drop its trailers, which is right for an HTTP
object and wrong for gRPC, whose status arrives in the trailers. A path with
`forward_trailers: true` relays the origin's trailers on a caching handler
too: the client's `TE: trailers` reaches the origin, the response is sent
chunked so trailers can follow it, and the origin's trailers are written
after the body. A response served from cache has no trailers, so a gRPC
path is best served by the `proxy` handler, which the Kubernetes controller
selects for every GRPCRoute; `forward_trailers` is for a hand-written
configuration that caches beside gRPC on one backend.

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
      - path: /
        req_rewriter_name: example

```

## Header and Query Parameter Behavior

In addition to running the request through a named rewriter, it is currently possible to make similar changes to the request with legacy path features that are described in this section. Note that these are likely to be deprecated in a future Trickster release, in favor of the more versatile named rewriters described above, which accomplish the same thing. Currently, if both a named rewriter and legacy path-based rewriting configs are defined for a given path, the named rewriter will be executed first.

### Basics

You can specify request query parameters, as well as request and response headers, to be Set, Appended or Removed.

#### Setting

To Set a header or parameter means to insert if non-existent, or fully replace if pre-existing. To set a header, provide the header name and value you wish to set in the Path Config `request_params`, `request_headers` or `response_headers` sections, in the format of `'Header-or-Parameter-Name' = 'Value'`.

The `Host` request header is special: it is what the upstream connection sends as its Host, so a `request_headers` entry naming it, under any spelling of the name, replaces the Host the origin sees rather than adding a header line. `+Host` behaves as a set, since Host carries one value, and `-Host` clears the override so the origin's own host is sent.

As an example, if the client request provides a `Cache-Control: no-store` header, a Path Config with a header 'set' directive for `'Cache-Control' = 'no-transform'` will replace the `no-store` entirely with a `no-transform`; client requests that have no `Cache-Control` header that are routed through this Path will have the Trickster-configured header injected outright. The same logic applies to query parameters.

##### Environment Variable Substitution

The `request_headers`, `request_params` and `response_headers` sections support environment variable substitution. This means you can use environment variables in your header or query parameter values. Example:

```yaml
backends:
  default:
    # ...
    paths:
      - path: /
        # ...
        request_params:
          'token': '${REQUEST_PARAM_TOKEN}'
        request_headers:
          'X-Auth-Token': '${REQUEST_HEADER_TOKEN}'
        response_headers:
          'X-Auth-Token': '${RESPONSE_HEADER_TOKEN}'
```

#### Appending

Appending a means inserting the header or parameter if it doesn't exist, or appending the configured value(s) into a pre-existing header with the given name. To indicate an append behavior (as opposed to set), prefix the header or parameter name with a '+' in the Path Config.

Example: if the client request provides a `token=SomeHash` parameter and the Path Config includes the parameter `'+token' = 'ProxyHash'`, the effective parameter when forwarding the request to the origin will be `token=SomeHash&token=ProxyHash`.

#### Removing

Removing a header or parameter means to strip it from the HTTP Request or Response when present. To do so, prefix the header/parameter name with '-', for example, `-Cache-control: none`. When removing headers, a value is required to be provided in order to conform to YAML specification; this value, however, is ineffectual. Note that there is currently no ability to remove a specific header value from a specific header - only the entire removal header. Consider setting the header value outright as described above, to strip any unwanted values.

#### Response Header Timing

Response Header injections occur as the object is received from the origin and before Trickster handles the object, meaning any caching response headers injected by Trickster will also be used by Trickster immediately to handle caching policies internally. This allows users to override cache controls from upstream systems if necessary to alter the actual caching behavior inside of Trickster. For example, InfluxDB sends down a `Cache-Control: No-Cache` header, which is fine for the user's browser, but Trickster needs to ignore this header in order to accelerate InfluxDB; so the default Path Configs for InfluxDB actually removes this header.

### Cache Key Components

By default, Trickster will use the HTTP Method, URL Path and any Authorization header to derive its Cache Key. On a backend with `preserve_host`, the client's `Host` is part of the key as well, since the origin sees it and may answer by it; a path whose `request_headers` fixes `Host` (a `Host` or `+Host` entry with a value, or `-Host`) sends one value, so its objects are keyed without it, while an empty `Host` entry changes nothing and keys as if absent. In a Path Config, you may specify any additional HTTP headers and URL Parameters to be used for cache key derivation, as well as information in the Request Body.

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
      # root path '/'
      - path: / # each path must be unique for the backend
        methods: [ '*' ] # All HTTP methods applicable to this config
        match_type: prefix # matches any path under '/'
        handler: proxy # proxy only, no caching (this is the default)
        # modify the query params en route to the origin; this adds authToken=${ROOT_REQUEST_AUTH_TOKEN}
        # (sourced from the environment variable ROOT_REQUEST_AUTH_TOKEN)
        request_params:
          authToken: ${ROOT_REQUEST_AUTH_TOKEN}
        # When a user requests a path matching this route, Trickster will
        # inject these headers into the request before contacting the Origin
        request_headers:
          Cache-Control: No-Transform
        # inject these headers into the response from the Origin
        # before replying to the client
        response_headers:
          Expires: '-1'
        # a path-level CORS policy overrides the backend policy; see docs/cors.md
        cors:
          mode: preserve
      - path: /images/
        methods:
          - GET
          - HEAD
        handler: proxycache # Trickster will cache the images directory
        match_type: prefix
        response_headers:
          Cache-Control: max-age=2592000 # cache for 30 days
      # but only cache this rotating image for 30 seconds
      - path: /images/rotating.jpg
        methods:
          - GET
        handler: proxycache
        match_type: exact
        response_headers:
          Cache-Control: max-age=30
          '-Expires': ''
      # redirect this sunsetted feature to a discontinued message
      - path: /blog
        methods:
          - '*'
        handler: localresponse
        match_type: prefix
        response_code: 302
        response_headers:
          Location: /discontinued
      # redirect plaintext requests to the same URL over TLS: the redirect
      # handler builds the Location from the request as the path's rewriter
      # leaves it, so only the parts the rewriter set change
      - path: /account
        methods:
          - '*'
        handler: redirect
        match_type: prefix
        response_code: 301
        req_rewriter_name: to-https
      # cache this API endpoint, keying on the query parameter
      - path: /api/
        methods:
          - GET
          - HEAD
        handler: proxycache
        match_type: prefix
        cache_key_params:
          - query
      # same API endpoint, different HTTP methods to route against, which are denied
      - path: /api/
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
      - path: /api/query/
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

## Redirecting Requests

The `redirect` handler answers a path with a redirection instead of an upstream request. Its `Location` is the request's own URL as the path's request rewriter and `request_headers` left it: a rewriter instruction that sets the scheme, hostname, port or path decides that part of the `Location`, a `Host` entry in `request_headers` decides the hostname when the rewriter did not, and whatever neither set is taken from the incoming request. The path's other `request_headers` are applied too, though nothing upstream receives them. An explicit scheme with no explicit port drops the request's port, since the redirect target is that scheme's well-known port, and a port that is the well-known one for the scheme is omitted. The path's `response_code` selects the redirection status (`302` unless it names another `3xx`), and its `response_headers` are applied, except that the composed `Location` always wins: a `response_headers` entry naming `Location` is overwritten by it. The `redirect` handler is registered by the `rp` and `rpc` providers. Like `localresponse`, it answers from configuration alone, so a request carrying `Connection: Upgrade` receives the redirection rather than being tunneled to the origin as it would be on a proxying path.

```yaml
request_rewriters:
  to-https:
    instructions:
      - [ scheme, set, https ]

backends:
  default:
    provider: rp
    origin_url: 'http://example.com'
    paths:
      - path: /account
        match_type: prefix
        handler: redirect
        response_code: 301
        req_rewriter_name: to-https
```

A request for `http://shop.example.com:8080/account/settings?tab=1` is answered with `301` and `Location: https://shop.example.com/account/settings?tab=1`. A fixed `Location` that ignores the request is better expressed with the `localresponse` handler, as in the example above.

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
      - path: /api/v1/label
        methods:
          - GET
        match_type: prefix
        handler: proxy
      # route fictional new /api/v1/coffee to ProxyCache
      - path: /api/v1/coffee
        methods:
          - GET
        match_type: prefix
        handler: proxycache
        cache_key_params:
          - beans
      # block /api/v1/admin/ from being reachable via Trickster
      - path: /api/v1/admin/
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
