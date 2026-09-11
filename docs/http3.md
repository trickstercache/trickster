# HTTP/3

Trickster can serve any HTTP listener's routes over HTTP/3 (RFC 9114) in
addition to HTTP/1.1 and HTTP/2, using QUIC over UDP.

HTTP/3 helps most where Trickster acts as an edge cache: loss recovery without
head-of-line blocking benefits parallel byte-range fetches, and connection
migration keeps a client's session alive as it changes networks.

## Enabling

HTTP/3 attaches to an existing `http` listener that already serves TLS. QUIC has
no cleartext mode, so a listener without a working TLS endpoint cannot serve it.

```yaml
listeners:
  default:
    port: 8480
    tls_port: 8483
    http3:
      enabled: true
```

That is the whole configuration for the common case. The UDP endpoint defaults
to the same address and port as the TLS endpoint, so clients find HTTP/3 where
they already found HTTPS.

### Options

| Field | Default | Description |
|---|---|---|
| `enabled` | `false` | Serves this listener's routes over HTTP/3. |
| `address` | the listener's `tls_address` | IP for the UDP socket. |
| `port` | the listener's `tls_port` | UDP port to bind. |
| `advertised_port` | `port` | Port published in `Alt-Svc`. Set this when a load balancer or NAT presents a different port to clients than the one bound here. |

```yaml
listeners:
  default:
    tls_address: 0.0.0.0
    tls_port: 8483
    http3:
      enabled: true
      port: 8443          # bind UDP/8443
      advertised_port: 443 # but tell clients to use 443
```

TLS certificates come from the backends mapped to the listener, exactly as they
do for the TLS/TCP endpoint — see [TLS](./tls.md). HTTP/3 uses the same
certificates; no separate configuration is needed.

## How clients discover HTTP/3

Browsers and most clients do not attempt HTTP/3 first. They connect over
TCP and look for an `Alt-Svc` response header naming an HTTP/3 endpoint:

```
Alt-Svc: h3=":8443"; ma=2592000
```

Trickster adds this header to every response from the listener's TLS/TCP
endpoint while HTTP/3 is enabled, so adoption is automatic and gradual. The TCP
endpoint keeps serving HTTP/1.1 and HTTP/2 for clients that do not upgrade.

Protocol-upgrade requests (`Connection: Upgrade`) are not advertised, since such
a request hijacks its connection before response headers matter.

## Limitations

- **No protocol upgrades.** HTTP/3 has no equivalent of the HTTP/1.1 `101
  Switching Protocols` handshake; RFC 9114 4.2 makes connection-specific headers
  malformed. WebSockets over HTTP/3 use Extended CONNECT (RFC 9220), which is
  not implemented. Send WebSocket traffic over the TCP endpoint, which supports
  it fully.
- **Inbound only.** Trickster speaks HTTP/1.1 or HTTP/2 to origins regardless of
  the protocol a client used to reach it. This matches Traefik; among major
  proxies only Envoy implements HTTP/3 to upstreams, and it needs `Alt-Svc`
  caching plus TCP fallback to do so safely.
- **UDP must reach the listener.** Some networks block or throttle UDP/443.
  Clients that cannot establish QUIC silently keep using TCP, so this degrades
  rather than fails.

## Operating notes

### UDP receive buffer

QUIC moves far more data through a single socket than typical UDP workloads, and
the kernel default receive buffer is often too small. On Linux, raise it:

```bash
sysctl -w net.core.rmem_max=7500000
sysctl -w net.core.wmem_max=7500000
```

Without this, a warning is logged at startup. In containers that cannot change
sysctls the warning is unactionable and can be silenced with
`QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING=1`.

### Reloads

The HTTP/3 listener participates in the same reload and drain lifecycle as every
other listener. Route changes are hot-swapped without rebinding the socket;
changing the bound or advertised port restarts just that endpoint.

## Trying it locally

Most systems ship a `curl` built without HTTP/3 (`curl --version | grep HTTP3`
to check). Trickster includes a small client so this is not a prerequisite:

```bash
make dev-certs                     # self-signed cert for localhost
go run ./hack/h3-client -url https://127.0.0.1:8483/ -ca docs/developer/environment/certs/trickster-dev.crt
```

The client prints the negotiated protocol, so `HTTP/3.0 200 OK` confirms the
request was served over QUIC.
