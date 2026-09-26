# Static File Server Backend

The Static Backend serves the contents of a local directory, rather than proxying requests to an upstream origin. Use it to host a website, a single-page application's assets, a maintenance page, or any other set of files directly from Trickster, alongside (and with the same listeners, TLS, virtual hosting, authentication, logging and tracing as) your other Backends.

The file server is built into Trickster on the Go standard library, and provides:

- `GET` and `HEAD` for files, with `ETag` and `Last-Modified` validators, conditional requests (`If-None-Match`, `If-Modified-Since`, `If-Match`, `If-Unmodified-Since`), and single, multi-part and `If-Range` byte range requests
- a configurable default file (`index.html`) for directory requests
- `Content-Type` detection from a built-in table of common web types, with your own additions and overrides
- a configurable `Cache-Control` header, with overrides by file extension
- a fileserver cache that holds small files in memory, kept current by filesystem change events, with the least recently used making room when it fills
- compression for the Backend's `compressible_types`, with each encoded rendition of a small file held in memory and reused
- a not-found file, for a custom error page or a single-page application
- [Prometheus metrics](./metrics.md) for the files served and the cache
- safe defaults: dotfiles are never served (other than `/.well-known/`), directory listings are off, and no request can reach a file outside of the configured root

## Configuring

A Static Backend sets its `provider` to `static`, and requires a `static` options block that names the `root` directory to serve.

```yaml
backends:
  website:
    provider: static
    hosts: [ www.example.com ]
    static:
      root: /var/www/html
```

Like any other Backend, this one is reachable at `/website/` on its listeners, at `/` for requests whose `Host` is `www.example.com`, and at `/` for every request when it sets `is_default: true`.

### Static Options

| Option | Default | Description |
| --- | --- | --- |
| `root` | *(required)* | Path to the directory holding the content to serve. It must exist and be a directory when the configuration is loaded. A relative path is resolved from Trickster's working directory. |
| `default_file` | `index.html` | The file served when a directory is requested (e.g., `/` or `/docs/`). It must be a plain file name that does not start with a `.` |
| `cache_control` | | The `Cache-Control` header value sent with every file. By default no `Cache-Control` header is sent, as with nginx and other common web servers, and a client judges how long a file stays fresh from its `Last-Modified` time. |
| `cache_control_by_extension` | | A map of file extension to `Cache-Control` value, which takes precedence over `cache_control` for matching files. |
| `response_headers` | | A map of additional headers to attach to every response. |
| `mime_types` | | A map of file extension to `Content-Type`, which adds to and takes precedence over the built-in types. |
| `not_found_file` | | A file, as a path within the root, that is served in place of a plain `404` response. See [Not-Found File](#not-found-file). |
| `not_found_status` | `404` | The status `not_found_file` is served with: `404` for an error page, or `200` for a single-page application. |
| `directory_listing` | `false` | When `true`, a directory that has no `default_file` is answered with a listing of its contents. |
| `cache` | | Options for the Fileserver cache, which holds small files in memory, described [below](#fileserver-cache). |

File extensions are case-insensitive, and may be written with or without the leading dot.

A fuller example:

```yaml
backends:
  website:
    provider: static
    is_default: true
    static:
      root: /var/www/html
      default_file: index.html
      # html is revalidated on every use, so a deployment is visible immediately
      cache_control: no-cache
      cache_control_by_extension:
        # fingerprinted assets never change, and can be cached for a year
        .js: public, max-age=31536000, immutable
        .css: public, max-age=31536000, immutable
        .woff2: public, max-age=31536000, immutable
      response_headers:
        X-Content-Type-Options: nosniff
        X-Frame-Options: DENY
      mime_types:
        .md: text/markdown; charset=utf-8
      not_found_file: errors/404.html
      directory_listing: false
      cache:
        max_file_size_bytes: 1048576
        max_size_bytes: 134217728
        max_files: 10000
        revalidation_interval: 10s
```

### Backend Options

These general Backend options work with a Static Backend as they do with any other: `hosts`, `any_host_routing`, `listener_names`, `is_default`, `path_routing_disabled`, `require_tls`, `tls`, `authenticator_name`, `cors`, `access_log`, `tracing_name`, `compressible_types`, `latency_min` and `latency_max`.

Options that describe proxying, caching or routing to an origin have no meaning for a file server. Rather than being silently ignored, these fail configuration validation when set on a Static Backend: `paths`, `req_rewriter_name`, `origin_url`, `rule_name`, `alb`, `prometheus`, `mysql`, `graphite`, `influxdb`, `sigv4`, `protocol`, `h2c_prior_knowledge`, `preserve_host`, `proxy_only` and `is_template`. Likewise, a `static` block on a Backend of any other provider fails validation.

A Static Backend has no origin to probe, so it always reports as available on the [health](./health.md) endpoint. It can be the target of a [Rule](./rule.md) or a member of an [ALB](./alb.md) pool.

## Requiring Authentication

To require users to authenticate before any file is served, attach an [Authenticator](./authenticator.md) to the Backend. Requests that fail authentication are rejected before the file server is consulted, so they reveal nothing about which files exist.

```yaml
authenticators:
  staff:
    provider: basic
    users_file: /etc/trickster/htpasswd
    users_file_format: htpasswd

backends:
  intranet:
    provider: static
    authenticator_name: staff
    static:
      root: /var/www/intranet
```

## Request Handling

| Request | Response |
| --- | --- |
| a file | `200 OK` with the file |
| a directory, with a trailing slash | `200 OK` with the directory's `default_file` |
| a directory with a trailing slash, but no `default_file` | `404 Not Found`, or a listing when `directory_listing` is `true` |
| a directory, without a trailing slash | `301 Moved Permanently` to the same path with a trailing slash |
| a file, with a trailing slash | `404 Not Found` |
| any path with a segment that starts with a `.`, other than a leading `/.well-known/` | `404 Not Found` |
| anything that does not exist, can't be read, or resolves outside of the root | `404 Not Found` |
| `OPTIONS` | `204 No Content` with an `Allow` header |
| any method other than `GET`, `HEAD` or `OPTIONS` | `405 Method Not Allowed` with an `Allow` header |

Every `404 Not Found` in this table is answered with the [not-found file](#not-found-file) when one is configured.

A missing `default_file` is deliberately a `404` rather than a `401` or `403`: with listings off, a directory is not a resource that exists to be forbidden.

The trailing-slash redirect is relative (`Location: docs/`), so it is correct whether the Backend was reached by its hostname, as the default Backend, or under its `/backend-name/` path.

### Dotfiles

Any request path containing a segment that begins with a `.` is answered with a `404`. This covers hidden files (`/.env`, `/.htpasswd`), everything beneath hidden directories (`/.git/config`), and attempts to reach them indirectly (`/docs/../.env`). Dotfiles are also left out of directory listings.

The one exception is `/.well-known/`, the location reserved by [RFC 8615](https://www.rfc-editor.org/rfc/rfc8615) for site-wide metadata such as `security.txt`, ACME HTTP-01 challenges and app-association files. It is exempt only as the first segment of the path (`/docs/.well-known/` is refused), dotfiles within it are still refused (`/.well-known/.secret`), and it is still left out of directory listings. The list of exemptions is built in, and is deliberately not configurable.

### Not-Found File

`not_found_file` names a file within the root to serve whenever the response would otherwise be a plain `404`: a path that doesn't exist, a directory with no `default_file`, a refused dotfile, and so on. `not_found_status` selects which of its two uses it is put to.

With the default status of `404`, it is a custom error page. The page is sent as the body of the `404`, with `Cache-Control: no-cache` and without the `ETag`, `Last-Modified` and `Accept-Ranges` headers, so that it can't be revalidated, ranged over or reused as though it were the missing file. Any `Range` or conditional headers on the request are ignored.

```yaml
    static:
      root: /var/www/html
      not_found_file: errors/404.html
```

With a status of `200`, it is the fallback for a single-page application, whose routes (`/dashboard`, `/users/42/edit`) exist only in the browser. Every unknown path is answered with the application itself, exactly as if the file had been requested by its own path: with its validators, its `Cache-Control`, conditional and byte range support, and its held renditions. Requests for files that do exist are unaffected.

```yaml
    static:
      root: /var/www/app
      not_found_file: index.html
      not_found_status: 200
```

If the not-found file is itself missing, the response is a plain `404`. A request using a method other than `GET` or `HEAD` is never answered with it. When `directory_listing` is on, a directory that can be listed is listed rather than answered with it.

### Root Confinement and Symbolic Links

All file access is confined to the `root` by the operating system, not by path inspection alone. No request can read a file outside of the root, whether by `..` traversal or through a symbolic link.

Symbolic links are followed only when they are *relative* links to a target *inside* the root. A link to anywhere outside of the root, and any link with an absolute target (even one that points inside the root), is answered with a `404`.

Only regular files are served. Devices, sockets and named pipes inside the root are answered with a `404`.

### Content Types

A file's `Content-Type` is resolved from, in order: the `mime_types` option, Trickster's built-in table of common web types (HTML, CSS, JavaScript, JSON, WebAssembly, images, fonts, audio, video, archives, documents and more), and the host's MIME database. The built-in table makes detection work the same in a minimal container image as on a full host. A file whose extension is unknown, or that has none, is identified from its first 512 bytes, and falls back to `application/octet-stream`.

### Validators, Ranges and Compression

Every file is served with an `ETag` and a `Last-Modified` header, and matching conditional requests are answered with a `304 Not Modified`.

Unless `cache_control` is set, no `Cache-Control` header is sent. A client then decides for itself how long to reuse a file before revalidating it, usually for a fraction of the time since the file was last modified, so files that have not changed in a long time are reused for longer. Where a change must be seen at once, such as the HTML entry point of an application with fingerprinted assets, set `cache_control` (or `cache_control_by_extension`) to `no-cache`, which has clients revalidate the file before each reuse.

The `ETag` is derived from the file's modification time and size, the same two components used by nginx and other common web servers. Because it comes from file metadata, the `ETag` is the same whether a file is served from memory or from disk, and a `HEAD` or conditional request is answered from a `stat` of the file without reading it. Instances of Trickster serving copies of the same content issue the same `ETag` for a file, so long as the deployment preserves modification times (as `rsync -a` and `tar` do).

As with those servers, a file that is replaced by different content of exactly the same size and modification time keeps its `ETag`. If your deployment tooling pins or preserves modification times, give changed files a new name (such as a content fingerprint) or a new modification time.

Files whose `Content-Type` is in the Backend's `compressible_types` are compressed for clients that accept it, in any of `zstd`, `br`, `gzip` and `deflate`. Those responses carry `Vary: Accept-Encoding`, and the weak form of the `ETag` (`W/"..."`), because the encoded bytes differ from the stored file the `ETag` describes. Byte ranges always address the stored file, so a `206 Partial Content` response is never compressed, and carries the `ETag` unchanged.

How a file is compressed depends on whether the [Fileserver cache](#fileserver-cache) can hold it. A file it can hold is compressed once, and the encoded *rendition* is held and reused, as described under [Held Renditions](#held-renditions). Any other file (a larger one, or any file when the cache is disabled) is compressed as it is sent, on every request.

## Fileserver Cache

The Fileserver cache is configured in the `cache` block of the `static` options, and belongs to the Static Backend alone. It is unrelated to the [caches](./caches.md) Trickster uses to accelerate proxied Backends (including the `memory` cache provider): it is not configured in the `caches` section, a Static Backend has no `cache_name`, and nothing it holds is reachable through the cache purge endpoints.

Small files are read into memory the first time their content is needed, and served from memory afterward without touching the disk. Larger files are streamed from disk on every request.

Only a response that sends the whole file loads it. A `HEAD` request, a conditional request answered with a `304`, and a byte range request are all served from disk without reading the file into memory, so they can't be used to fill the cache. When many requests arrive at once for a file that is not yet held, it is read once and shared between them.

| Option | Default | Description |
| --- | --- | --- |
| `disabled` | `false` | When `true`, every request is served from disk. |
| `max_file_size_bytes` | `1048576` (1 MiB) | The largest file that is held in memory. |
| `max_size_bytes` | `134217728` (128 MiB) | The most memory the cache will use. Each held file counts as its size plus a fixed 1 KiB allowance for its bookkeeping, so empty and tiny files are bounded too. |
| `max_files` | `10000` | The most objects that are held, which also bounds the number of directories that are watched. A file as stored, and each held rendition of it, is an object. |
| `revalidation_interval` | `10s` | How often held files are compared to the disk, as a backstop to change events. |

Capacity is reserved before a file is read, so the limits hold even while many different files are being loaded at once. Once either limit is reached, the [least recently used](#eviction) objects make room for new ones.

### Eviction

When a file needs room that the cache doesn't have, by either limit, the least recently used objects are evicted to make it. Recency is tracked without slowing requests down: serving a held file only marks it as used, and eviction passes over each marked object once, clearing its mark, before it will remove it. An object is therefore evicted only if it has gone unread since eviction last reached it.

Room is only ever made for a file that is then held. Eviction first works out what would have to go, and goes ahead only if that is enough; a file that there isn't room for is served from disk, and costs the cache nothing it holds. A request for a file that is never asked for again therefore can't flush content that is in use.

The work a request will do to find room is bounded, however large the cache is: it passes over a limited number of recently used objects before giving up. Those it passed are no longer marked as recently used, so if requests for room keep coming, objects that aren't being read in between give way to them, while objects in constant use never do. The objects a request does evict are in proportion to the size of the file they make way for. No single file is admitted at the cost of more than 128 objects. With the default limits that is rarely a constraint, as a full cache is usually full by its count of objects, and one eviction then makes room for a file of any size; it matters to a cache with a very high `max_files` that is full of very small files, in which a large file is served from disk instead.

A file is never evicted to make room for a rendition of itself, as a rendition is only held alongside the file it was made from. Where there is room for only one of them, it is the file that is held, and its renditions are made from memory for each request. Room held by files that are still being read into memory can't be evicted either; in the unlikely event that those account for the whole cache, a new file is served from disk.

### Held Renditions

A held file of a compressible type is also held in the encodings clients ask for, each as an object of its own beside the file as stored, so that a file is compressed once rather than on every request.

A request's `Accept-Encoding` header is read for the encodings Trickster supports (`zstd`, `br`, `gzip` and `deflate`), most preferred first: by the weight (`q`) the client gave each, and among encodings of equal weight, or when no weights are given (as by browsers), by Trickster's own preference in the order just listed. An encoding takes the weight of its own entry, or else that of the wildcard (`*`) if there is one. Encodings the client refused (`q=0`) are left out, and so are any it weighted below `identity`, since the file as stored is always there to be sent instead. Then:

1. If the file is held in any of those encodings, the most preferred of the renditions that are held is served.
2. Otherwise, if the file is held as stored, it is encoded from memory in the client's most preferred encoding, and the new rendition is held.
3. Otherwise, the file is read from disk and encoded in the client's most preferred encoding, and both the file as stored and the new rendition are held. A rendition that is held in some other encoding is never decoded to stand in for the file.

A new rendition is streamed to the client as it is encoded, and what was sent is kept to be held, so the first response is not delayed by the rendition being made for later ones. That first response is sent without a `Content-Length`; once held, a rendition is sent with its own.

Renditions count toward `max_size_bytes` and `max_files` like any other object, are evicted independently of one another, and are all dropped together when their file changes on disk. A rendition is only made for a response that sends the whole file: not for a `HEAD`, a byte range, or a conditional request (which may turn out to need no body; if it does need one, it is compressed as it is sent). Files smaller than 512 bytes are always sent as stored, as an encoding's own framing outweighs what it would save. When an encoding turns out not to make a file smaller, that encoding isn't tried for the file again until it changes; the other encodings are unaffected, and a client that prefers the unhelpful one is given the next it accepts.

When nothing a client accepts is left (for example `identity;q=0` from a client that accepts no supported encoding), the file is sent as stored rather than refused with a `406`, as other web servers do.

### Serving and the Cache

Holding a file never delays serving it. Held files are read without locking. Everything that changes the cache (holding a file or a rendition, or releasing room that turned out not to be needed) is done after, and apart from, the response that led to it. The one thing a request asks of the cache before it responds is to reserve room, which is what keeps memory within its limits however many files are being read at once; if the cache is busy at that moment, the request doesn't wait, and is served without being held. The file is then held by a later request for it.

### Change Detection

Trickster watches the directories of the files it holds, and stops watching a directory soon after it holds nothing from it. Letting go of a watch is done in the background, so evicting files never makes a request wait on the operating system. When a file is modified, replaced, renamed or removed, it is dropped from memory (typically within milliseconds) and the next request reads it from disk again. Renaming or removing a directory drops every held file beneath it. A changed file is never updated in place, and a read that overlaps a change is discarded rather than stored, so a request is never served a mix of old and new content from memory. A change touches only what it affects: the one file, or the files beneath the one directory. Every other held file stays in memory, and files that are being read into memory at that moment are unaffected, so a busy deployment does not push the rest of the site back to disk.

Every `revalidation_interval`, each held file's size, modification time and identity are compared to the disk, which catches changes that raise no event: a filesystem without change notifications (such as some network and container bind mounts), a changed symbolic link target, or a replaced root. On such filesystems, `revalidation_interval` is the longest a stale file can be served; lower it, or disable the fileserver cache, if that matters for your content.

### Atomic Deployments

Deploying by swapping a symbolic link is supported. When `root` is a link (e.g., `/var/www/current` → `/var/www/releases/42`) and the link is repointed, Trickster notices at the next `revalidation_interval`, switches to the new directory, and drops every held file. Requests in flight during the switch complete against the previous release.

## Directory Listings

With `directory_listing: true`, a request for a directory that has no `default_file` is answered with an HTML page linking to the directory's files and subdirectories, with their sizes and modification times. Dotfiles, links that can't be followed, and anything that is not a regular file or directory are left out. A directory that does have a `default_file` is always answered with that file. Listings are generated per request, and are sent with `Cache-Control: no-cache`.
