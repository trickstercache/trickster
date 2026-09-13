# Collapsed Forwarding

Collapsed Forwarding is feature common among Reverse Proxy Cache solutions like Squid, Varnish and Apache Traffic Server. It works by ensuring only a single request to the upstream origin is performed for any object on a cache miss or revalidation attempt, no matter how many users are requesting the object at the same time.

Trickster has support for two types of Collapsed Forwarding: Basic (default) and Progressive

## Basic Collapsed Forwarding

Basic Collapsed Forwarding is the default functionality for Trickster, and works by waitlisting all requests for a cacheable object while a cache miss is being serviced for the object, and then serving the waitlisted requests once the cache has been populated.

The feature is further detailed in the following diagram:

<img src="./images/basic-collapsed-forwarding.png" width="800">

## Progressive Collapsed Forwarding

Progressive Collapsed Forwarding (PCF) is an improvement upon the basic version, in that it eliminates the waitlist and serves all simultaneous requests concurrently while the object is still downloading from the server, similar to Apache Traffic Server's "read-while-write" feature. This may be useful in low-latency applications such as DASH or HLS video delivery, since PCF minimizes Time to First Byte latency for extremely popular objects.

The feature is further detailed in the following diagram:

<img src="./images/progressive-collapsed-forwarding-cache.png" width="800">

### PCF for Proxy-Only Requests

Trickster provides a unique feature that implements PCF in Proxy-Only configurations, to bring the benefits of Collapsed Forwarding to HTTP Paths that are not configured to be routed through the Reverse Proxy Cache. (See [Paths](./paths.md) documentation for more info on routing).

The feature is further detailed in the following diagram:

<img src="./images/progressive-collapsed-forwarding-proxy.png" width="800">

## When collapsing is refused

Collapsing delivers the same bytes to every client that joins, which is a
stronger claim than caching makes. Trickster therefore applies the shared-cache
rules from RFC 9111 before fanning a response out, and refuses when any of the
following holds. A refused response is still served normally; each client simply
gets its own fetch.

| Condition | Reason |
|---|---|
| Method is not GET or HEAD | Collapsing a non-idempotent request would mean one of them never executed (RFC 9110 9.2.1) |
| Response status is not 200 | Partial and error responses are not safely shareable |
| `Cache-Control: private` or `no-store` | Explicitly single-user (RFC 9111 5.2.2.5, 5.2.2.7) |
| Response carries `Set-Cookie` | Per-client state; sharing it would disclose it across users |
| Request carried `Authorization` without `public`, `s-maxage` or `must-revalidate` on the response | RFC 9111 3.5 |
| `Vary: *`, or a `Vary` field not listed in the path's `cache_key_headers` | Two joiners could legitimately deserve different bytes |
| `Content-Type: text/event-stream` | An event stream is per-subscriber, not a shared object |
| Response is larger than the backend's `max_object_size_bytes` | The shared buffer is bounded by that limit |

The default is to refuse: a missed collapse costs one extra origin fetch, while
an incorrect one would serve one user's response to another.

## Object sizes

A response with a known `Content-Length` is buffered exactly. A response of
unknown length -- a chunked transfer, which is common for live video manifests
and segments -- is buffered as it arrives, growing up to the backend's
`max_object_size_bytes`. A collapsed transfer that exceeds that limit is aborted
for every attached client rather than being silently truncated.

If the origin fails or ends a body early, every client attached to that collapse
receives a failed response. An incomplete object is never presented as complete
and is never written to cache.

## How to enable Progressive Collapsed Forwarding

When configuring path configs as described in [Paths Documentation](./paths.md) add `collapsed_forwarding: progressive` in any path config using the `proxy` or `proxycache` handlers.

Example:

```yaml
origins:
  test:
    paths:
      - path: /test_path1/
        match_type: prefix
        handler: proxycache
        collapsed_forwarding: progressive
      - path: /test_path2/
        match_type: prefix
        handler: proxy
        collapsed_forwarding: progressive
```

See the [example.full.yaml](../examples/conf/example.full.yaml) for more configuration examples.

## How to test Progressive Collapsed Forwarding

An easy way to test PCF is to set up your favorite file server to host a large file(Lighttpd, Nginx, Apache WS, etc.), In Trickster turn on PCF for that path config and try make simultaneous requests.
If the networking between your machine and Trickster has enough bandwidth you should see both streaming at the equivalent rate as the origin request.

Example:

- Run a Lighttpd instance or docker container on your local machine and make a large file available to be served
- Run Trickster locally
- Make multiple curl requests of the same object

You should see the speed limited on the origin request by your disk IO, and your speed between Trickster limited by Memory/CPU
