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

`match: longest` uses the longest matching prefix. An equal-length allow and deny is a deny, reported as a load warning. `match: ordered` uses `rules` only, first match wins, and an unreachable rule is a load warning. `all` means `0.0.0.0/0` and `::/0`. `::/0` alone is IPv6.

Entries are addresses or CIDRs. Host bits in a CIDR are masked. A file is one entry per line, read when the configuration is loaded and on every reload. A blank line, or a line whose first non-space character is `#`, is skipped. A missing or unreadable file fails the load. Changing only the file does not reload Trickster; reload the configuration to read it again.

`source: client_ip` is the address resolved through `trusted_proxies` and the PROXY protocol. `source: peer` is the socket peer and is valid only on a listener. `none` is reserved: it is not an access-list name. On a path, `ip_acl_name: none` clears the backend list. On a listener or backend it is an undefined list. An empty path name inherits the backend list. A listener list is independent of both.

`action: drop` is refused when the list is attached to a backend or path that an HTTP listener serves, including a backend mapped to both HTTP and a stream listener. A backend mapped only to `tcp`, `tls`, or `udp` may use `drop`.

A native listener (`mysql`, `postgres`, `clickhouse`, `flight-sql`) refuses `source: client_ip` while `proxy_protocol` is enabled. `client_ip` with `proxy_protocol` and no `trusted_proxies` loads with a warning, on every protocol including `udp`: an empty trusted-proxy list believes every peer's PROXY header where the listener reads one. A backend that has an access list and is mapped to a native listener also loads with a warning. Native sessions are judged by the listener list only, and a ClickHouse native bridge request carries no client address, so the backend list denies it. The configuration remains valid. A ClickHouse backend served only over HTTP does not get that warning.

An access list with no entries and `default: deny` is valid and warns, because every address is denied.
