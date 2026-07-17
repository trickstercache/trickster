# Cross-Origin Resource Sharing

Trickster can control Cross-Origin Resource Sharing (CORS) response headers for each backend. A path can override its backend's policy by providing its own `cors` block.

For compatibility, a backend or path without a `cors` block uses Trickster's legacy behavior: `Access-Control-Allow-Origin` is set to `*`, while other CORS headers from the origin are left unchanged.

## Modes

| Mode | Behavior |
| --- | --- |
| `preserve` | Return the origin's `Access-Control-*` response headers unchanged. |
| `merge` | Preserve origin CORS headers, then apply the configured headers as overrides. |
| `replace` | Remove all origin `Access-Control-*` response headers, then apply the configured headers. |
| `disable` | Remove all origin `Access-Control-*` response headers. This mode cannot include a `headers` block. |

When `preserve` or `merge` is configured, Trickster includes the request's `Origin` header in the cache key. This prevents an origin-specific CORS response from being served to a request from another origin.

## Backend Configuration

The following example replaces the origin's CORS headers with a fixed policy:

```yaml
backends:
  default:
    provider: reverseproxycache
    origin_url: http://api.example.com
    cors:
      mode: replace
      headers:
        Access-Control-Allow-Origin: https://dashboard.example.com
        Access-Control-Allow-Credentials: "true"
        Access-Control-Expose-Headers: X-Trickster-Result
```

To preserve the origin's CORS headers while replacing selected values, use `merge`:

```yaml
cors:
  mode: merge
  headers:
    Access-Control-Allow-Origin: https://trickster.example.com
```

To return all origin CORS headers unchanged, use `preserve` without a `headers` map:

```yaml
cors:
  mode: preserve
```

To disable CORS headers, use `disable` without a `headers` block:

```yaml
cors:
  mode: disable
```

Only `Access-Control-*` response headers can be configured in a `cors.headers` map. As with path `response_headers`, prefix a header name with `-` to remove that header in `merge` mode or `+` to append another value.

## Path Overrides

A path-level policy replaces the backend policy for requests matching that path:

```yaml
backends:
  default:
    provider: reverseproxycache
    origin_url: http://api.example.com
    cors:
      mode: preserve
    paths:
      - path: /private/
        match_type: prefix
        cors:
          mode: disable
```

For an ALB, user router, or rule backend that dispatches to another backend internally, the policy on the client-facing backend and path takes precedence. Internal routing therefore cannot change the CORS policy associated with the public Trickster route.
