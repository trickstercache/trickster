# Trickster 2.1

Trickster 2.1 includes a number of new features to give it even more uses in a number of deployment scenarios.

## Features

* Auto-discovery - the ALB can now manage pool members through common auto-discovery mechanisms such as Kubernetes APIs, DNS A and SRV records, etc.

* Config Management - We now support loading multiple config files in the same subdirectory below the main config. We've also added automatic config reloading when the file contents change - including automatic detection and reloading when a certificate is swapped out.

* Loggng - We've added support for customizable access logging and error logging per backend in NCSA format.

* Regex Path Matching - You can now define path routes with regexes to match incoming requests.

* We now support accelerating Graphite

* We now support accelerating MySQL with Time Series-like queries (e.g., Grafana dashboards)

* HTTP/2 and HTTP/3 - Listeners now accept cleartext HTTP/2 (h2c) by prior knowledge in addition to HTTP/2 over TLS, and Trickster negotiates HTTP/2 to origins that offer it. No configuration is required. Listeners can also now serve their routes over HTTP/3 (QUIC) alongside HTTP/1.1 and HTTP/2. Enable the new `http3` block on any TLS-enabled listener; responses on the TLS endpoint advertise the HTTP/3 endpoint via `Alt-Svc` so clients upgrade on their own. See [http3.md](./http3.md).

* Protocol Upgrades - WebSocket and other `Connection: Upgrade` requests are now tunneled end-to-end rather than rejected, including through paths that are otherwise cached.

* Streaming - Responses with an unknown length and Server-Sent Events streams are now flushed to the client as bytes arrive rather than buffered, and HTTP trailers are passed through.

## Breaking Changes

