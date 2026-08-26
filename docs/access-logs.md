# Access and Error Logs

Trickster can write per-backend HTTP access logs and error logs, with
customizable formats, rotation and retention. Both logs are off by default;
each is enabled by configuring its filename.

## Basic Configuration

```yaml
backends:
  example1:
    provider: rp
    origin_url: http://example.com/
    access_log:
      filename: /var/log/trickster/example1.access.log
      error_filename: /var/log/trickster/example1.error.log
```

- The access log receives one line per request handled by the backend and is
  written only when `filename` is set.
- The error log receives one line per request whose response status is at or
  above `error_threshold` (default `400`) and is written only when
  `error_filename` is set. An error-logged request also appears in the access
  log when both are configured.
- Two backends may share a filename; they will safely share the underlying
  file and its rotation.
- When `instance_id` is set in the main config, it is inserted into log
  filenames just as with the application log (e.g., `example1.access.1.log`).

## Log Format

The `format` option accepts either a named preset or a custom format string
using Apache-style `%` tokens, so the well-known conventions from Apache
HTTP Server, Apache Traffic Server, Lighttpd, and similar servers apply
directly.

### Presets

| Name | Description |
| ----- | ----- |
| `common` | NCSA Common Log Format: `%h %l %u %t "%r" %>s %b` |
| `combined` | Apache/Nginx Combined format: `common` + Referer and User-Agent. This is the default. |
| `extended` | `combined` + duration (ms), cache status and backend name |
| `json` | One JSON object per line with a fixed field set (see below) |

### Custom Formats

```yaml
    access_log:
      filename: /var/log/trickster/example1.access.log
      format: '%h %u %t "%r" %>s %b %{ms}T %{cache-status}x'
```

Supported tokens:

| Token | Description |
| ----- | ----- |
| `%h`, `%a` | client IP address |
| `%l` | remote logname (always `-`) |
| `%u` | authenticated username (from HTTP Basic Auth), else `-` |
| `%t` | request start time in CLF format: `[26/Aug/2026:10:30:00 +0000]` |
| `%{sec}t`, `%{msec}t`, `%{usec}t` | request start time as a Unix epoch value |
| `%{LAYOUT}t` | request start time in a custom [Go time layout](https://pkg.go.dev/time#pkg-constants) |
| `%r` | first line of the request: `GET /path?query HTTP/1.1` |
| `%m` | request method |
| `%U` | request URL path |
| `%q` | query string, prefixed with `?`, or empty when none |
| `%H` | request protocol (e.g., `HTTP/1.1`) |
| `%s`, `%>s` | response status code |
| `%b` | response body bytes, or `-` when zero (CLF style) |
| `%B` | response body bytes, numeric |
| `%D` | request duration in microseconds |
| `%T` | request duration in whole seconds |
| `%{us}T`, `%{ms}T`, `%{s}T` | request duration in the given unit |
| `%{Name}i` | request header value |
| `%{Name}o` | response header value |
| `%{Name}c` | request cookie value |
| `%v` | requested virtual host |
| `%p` | listener port that served the request |
| `%A` | listener IP address that served the request |
| `%%` | a literal `%` |

Trickster-specific values use the `%{key}x` extension namespace:

| Token | Description |
| ----- | ----- |
| `%{backend}x` | backend name |
| `%{provider}x` | backend provider type |
| `%{cache-status}x` | cache result (`hit`, `phit`, `kmiss`, ...); see [Cache Status](./caches.md#cache-status) |
| `%{engine}x` | proxy engine that handled the request (e.g., `DeltaProxyCache`) |
| `%{path-config}x` | the matched [path config](./paths.md) path |

Missing values render as `-`. Values derived from the request (like headers
and usernames) are backslash-escaped so they cannot corrupt the log line
structure. Unknown tokens fail validation at startup.

The `json` preset emits these fields per line: `time`, `client_ip`, `user`,
`method`, `path`, `query`, `proto`, `status`, `bytes`, `duration_ms`,
`host`, `referer`, `user_agent`, `backend`, `provider`, `path_config`,
`cache_status`, `engine`.

## Rotation and Retention

Access and error logs are rotated and pruned automatically, using
nginx/logrotate-style numbered archives (`example1.access.log.1.gz` is the
most recent archive, `.2.gz` the next, and so on).

```yaml
    access_log:
      filename: /var/log/trickster/example1.access.log
      rotation:
        size: 256MB   # rotate when the live file would exceed this size (default 256MB)
        interval: 1d  # also rotate when the live file is older than this (default off)
      retention:
        count: 3      # keep at most 3 archives (default 80)
        age: 7d       # also prune archives older than this (default 7d)
      compress: true  # gzip archives (default true)
```

- `size` and `interval` may be combined; the log rotates when either
  threshold is reached. Setting both to `0` disables rotation.
- Sizes accept `KB`, `MB`, `GB` and `TB` suffixes (binary multiples), or a
  plain byte count.
- `retention.count: 0` disables count-based pruning and keeps all archives.
- Writes are buffered for up to one second or 64 KiB. A process or machine
  crash can lose the buffered tail; an orderly shutdown flushes it.
- Interval rotation keeps its epoch in a `<filename>.rotation` sidecar so a
  restart does not reset the interval clock.

Archives created by older Trickster releases use timestamped lumberjack
names and are not included in numbered-archive retention. They form a bounded
legacy set and may be removed manually after upgrading.

When upgrading from the original logging implementation, note these filename
and retention changes:

- `retention.count: 0` now keeps all archives; configure a positive count to
  bound archive retention.
- With `main.instance_id` enabled, filenames without a `.log` suffix now also
  include the instance ID (`trickster.out` becomes `trickster.2.out`). Update
  log shippers that still follow the unsuffixed filename.

The same `rotation`, `retention` and `compress` options are also available
in the main `logging:` config section to control rotation of the Trickster
application log, with the same defaults.

## Error Log Settings

Each `error_*` option inherits its value from the corresponding access log
option when unset:

```yaml
    access_log:
      filename: /var/log/trickster/example1.access.log
      format: combined
      error_filename: /var/log/trickster/example1.error.log
      error_format: ''      # default: inherits format
      error_threshold: 400  # log responses with status >= this (default 400)
      error_rotation:       # default: inherits rotation
        size: 64MB
      error_retention:      # default: inherits retention
        count: 7
      error_compress: true  # default: inherits compress
```
