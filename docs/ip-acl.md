# IP Access Lists

`ip_acls` defines named IPv4 and IPv6 access lists. A listener, backend, or path selects one with `ip_acl_name`. The exhaustive example is [`examples/conf/example.full.yaml`](../examples/conf/example.full.yaml).

```yaml
ip_acls:
  office:
    match: longest          # longest (default) | ordered
    default: deny           # deny (default) | allow
    allow: [10.0.0.0/8, 2001:db8::/32]
    deny: [10.0.99.0/24]
    allow_file: /etc/trickster/office-allow.lst
    deny_file: /etc/trickster/blocklist.lst
    source: client_ip       # client_ip (default) | peer
    action: reject          # reject (default) | drop
    status: 403             # 400-599; default 403

listeners:
  default:
    ip_acl_name: office

backends:
  api:
    ip_acl_name: partners
    paths:
      - path: /public/
        ip_acl_name: none
      - path: /admin/
        ip_acl_name: office
```

## Configuration and composition

Each entry under `ip_acls` is one named list. `ip_acl_name` attaches that name to a listener, a backend, or a path. `none` is reserved and is not a list name. On a listener or a backend it is an undefined name and the configuration fails to load. On a path it is the clear operation below.

Three attachments can apply to one HTTP request, in two decisions:

- The listener list is its own decision. It does not inherit, replace, or clear a backend or path list.
- The route decision is the path list when the path names one. That name replaces the backend list for that path, and the decision is counted with scope `path`. An empty path `ip_acl_name` inherits the backend list, and that decision is counted with scope `backend`. `ip_acl_name: none` clears the backend list for that path, so the route makes no access-list decision.

When both the listener and the route have a list, the request must be allowed by each. A denial from either one stops the request.

## Match modes

`match` is `longest` or `ordered`. Empty means `longest`. `default` is the verdict when nothing matches. Empty means `deny`. `allow` is the other value.

`longest` uses the longest matching prefix. An equal-length allow and deny is a deny, and the load reports a warning. `allow`, `deny`, `allow_file`, and `deny_file` belong to this mode. `rules` in this mode fails the load.

`ordered` walks `rules` from top to bottom and uses the first match. `allow`, `deny`, `allow_file`, and `deny_file` next to `rules` fail the load; each rule sets exactly one of those. An empty `rules` list is valid. With `default: deny` it is the same warning as any other list that has no entries: every address is denied. A rule that an earlier rule already covers completely is unreachable and is a load warning. The commented `legacy` list in [`example.full.yaml`](../examples/conf/example.full.yaml) is this mode:

```yaml
ip_acls:
  legacy:
    match: ordered
    default: deny
    rules:
      - deny: 10.1.2.0/24
      - allow: 10.0.0.0/8
      - deny: all
```

A list with no entries and `default: deny` is valid and warns, because every address is denied. That warning covers an empty longest list and an empty ordered rule list alike.

## Addresses and files

An entry is an address or a CIDR, in IPv4 or IPv6. Host bits in a CIDR are masked. `all` means `0.0.0.0/0` and `::/0`. `::/0` alone is IPv6. An IPv4-mapped IPv6 address is matched as its IPv4 form. At request time an address that cannot be parsed is denied, whatever `default` is. A zoned IPv6 address matches no prefix, so `default` applies.

`allow_file` and `deny_file` are one entry per line. A blank line, or a line whose first non-space character is `#`, is skipped. The file is read when the configuration is loaded and on every configuration reload. A missing or unreadable file fails that load. A reload happens only when a configuration file has changed, and an access-list file is not one: after editing only the list, update a configuration file's modification time (`touch` it, for example) before sending the reload, or the reload is skipped and the old list stays.

## `client_ip` and `peer`

`source` is `client_ip` or `peer`. Empty means `client_ip`. `peer` is valid only on a listener. A backend or path that names a `peer` list fails validation.

`client_ip` is the address already resolved through the listener's `trusted_proxies` and, where the listener reads one, its PROXY protocol header. See [Trusted Proxies](./configuring.md#trusted-proxies). `peer` is the socket peer. The two are not interchangeable across protocols.

### HTTP and HTTP/3

On HTTP and HTTP/3, `client_ip` is judged in middleware, after trusted-proxy and PROXY resolution. A `peer` list on HTTP/1 or HTTP/2 is judged when the TCP connection is accepted and is not judged again in middleware. HTTP/3 has no TCP accept, so a `peer` list is judged in middleware from the QUIC peer address.

### TCP, TLS, and UDP

On a `tcp` or `tls` listener, `peer` is judged at accept, before the PROXY header and before TLS. `client_ip` is judged later, during stream admission, from the client address on the flow. That address is the socket peer, or the address from a trusted PROXY header when the listener has read one.

UDP has no TCP accept. A UDP listener list, whichever its source, and the UDP backend list are judged during stream admission when the flow opens. Later datagrams of a flow that already opened are not judged again. A denied UDP flow stays denied for the datagrams that follow.

A backend `peer` list is not a valid attachment, so stream admission does not enforce one.

Stream admission enforces the list on the backend selected by the listener table, including a front ALB. Lists on stream ALB pool members, nested ALBs, or discovery templates are not supported and fail configuration validation. This includes named path lists on those members or templates, unless HTTP also serves the member: a path list then judges its HTTP requests. Attach the policy to the listener or front ALB instead. [Geo ACLs](./geo-acl.md) follow the same rule.

### Native protocols

A native listener (`mysql`, `postgres`, `clickhouse`, `flight-sql`) judges its list at accept. `client_ip` on that listener is the socket peer, and only when `proxy_protocol` is off. `client_ip` together with `proxy_protocol` is refused, because the socket peer and the address in the header are different addresses. `client_ip` with `proxy_protocol` and no `trusted_proxies` still loads on a listener that reads a PROXY header, including `udp`, and warns: an empty trusted-proxy list believes every peer's header.

Native sessions are judged by the listener list only, so a backend that has an access list and is mapped to a native listener loads with a warning. The configuration remains valid. A ClickHouse native listener also sends each query through the backend's routes with the session's address, so the backend or path list judges every query: `reject` fails the query with exception 62 and keeps the session open, and `drop` closes the session. A ClickHouse backend served only over HTTP does not get the warning.

## Actions

`action` is `reject` or `drop`. Empty means `reject`. `status` is the HTTP status used by `reject`, from 400 to 599. Empty means 403. Both actions count as a deny in the decision metric below.

### HTTP

`reject` writes that status, an empty body, and `Cache-Control: no-store`. The response then follows the normal access-log path.

`drop` aborts the request with `http.ErrAbortHandler`. No response bytes are written, and the access log does not write its completion line. See [access-logs.md](./access-logs.md).

`drop` is refused when the list is attached to a backend or path that an HTTP listener serves, including a backend mapped to both HTTP and a stream listener. HTTP reachability includes dispatch through ALB pools, discovery templates, rules, user routers, and mirrors. A backend mapped only to `tcp`, `tls`, or `udp` may use `drop`.

### Streams

On TCP and TLS, `reject` resets the connection and `drop` closes it. Neither writes an HTTP response. On UDP, both actions are the admission verdict for that flow: the flow is not opened, and later datagrams from that client are not admitted again. An HTTP `drop` does not describe this path.

## Readiness

The listener list wraps every handler on that listener. On a proxy listener that includes the readiness handler. On the management listener it includes that listener's routes. Readiness is not exempt from the list. A client the list denies does not receive a ready response from a listener that list protects.

## Reload

Access-list files are read again only when the configuration is loaded or reloaded, and a reload is skipped while no configuration file has changed; see [Addresses and files](#addresses-and-files). An access-list change does not by itself rebind the listener: the running listener swaps in the new list. Connections accepted after the reload, and stream flows admitted after the reload, are judged by the new list.

A request already in progress is not judged a second time. An established TCP or TLS flow keeps the upstream it was relayed to and is not judged again. An established UDP flow is not admitted again. A flow that was denied stays denied; a flow that was allowed is not closed because the list changed.

## Metrics

`trickster_ip_acl_decisions_total` counts each decision an attached list actually makes. The labels are:

| Label | Values |
|---|---|
| `ip_acl` | the list name |
| `scope` | `listener`, `backend`, or `path` |
| `verdict` | `allow` or `deny` |

`reject` and `drop` are both `deny`. The scope is the attachment that enforced the list: `listener` for the listener, `path` when the path named its own list, and `backend` when the path inherited the backend list. Each enforcement point increments the counter once. A TCP or TLS `peer` decision counted at accept is not counted again in HTTP middleware or in stream admission. See [metrics.md](./metrics.md).

## Debug logging

A denial writes one application log line at debug: `ip acl denied`, with `ip_acl`, `scope`, `address`, and `action`. Allows are not logged. The default log level does not show the line. This is separate from the access log and from the decision metric.

## Kubernetes

Generated backends can take a list from `kubernetes.defaults.ip_acl_name`, or from the GatewayClass parameter `ip_acl_name` when that parameter is set. Only a list with `source: client_ip` and `action: reject` is eligible. There is no Ingress annotation and no cache-policy field for the name. See [kubernetes-ingress.md](./kubernetes-ingress.md) and [kubernetes-gateway.md](./kubernetes-gateway.md).

## Compile time

Compiling a file of 100,000 IPv4 allow entries was measured with `go test -bench=BenchmarkCompileFile100k -benchmem -count=1 ./pkg/proxy/ipacl/` on darwin/arm64 (Apple M1):

```text
BenchmarkCompileFile100k-8   	      24	  55277351 ns/op	46640989 B/op	  600334 allocs/op
```

That is the measured cost of compiling that file, not of matching one address against an already compiled list.
