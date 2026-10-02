# Automatic Certificates (ACME)

Trickster can obtain and renew its own serving certificates from any certificate authority that speaks ACME ([RFC 8555](https://www.rfc-editor.org/rfc/rfc8555)), such as Let's Encrypt, ZeroSSL, Google Trust Services or a private step-ca. A backend opts in under its `tls` section, and Trickster issues a certificate for each of its hostnames, serves it from the listener's certificate store, and renews it in the background before it expires.

ACME-managed certificates are served alongside certificates loaded from files and certificates supplied at runtime, and they use the same SNI selection, hot-swap and inventory machinery described in [TLS Support](./tls.md). Issuance, renewal and storage all happen off the handshake path, so a certificate obtained through ACME costs nothing more per handshake than one loaded from a file.

## Quick Start

```yaml
listeners:
  default:
    port: 80
    tls_port: 443

acme:
  storage:
    path: /var/lib/trickster/acme
  issuers:
    letsencrypt:
      email: ops@example.com
      agree_to_terms: true

backends:
  site:
    provider: reverseproxycache
    origin_url: http://origin.internal:8080
    hosts: [www.example.com, example.com]
    tls:
      acme:
        issuer: letsencrypt
```

With this configuration, Trickster requests certificates for `www.example.com` and `example.com` from Let's Encrypt, answers the CA's validation on ports 80 and 443, and serves the certificates on the `default` listener as soon as they are issued.

The CA validates a name by connecting to it on **port 80** (`http-01`) or **port 443** (`tls-alpn-01`). When Trickster listens on other ports, such as the defaults of 8480 and 8483, forward 80 and 443 to them, or use the `dns-01` challenge.

Try a new configuration against a staging directory first, such as `https://acme-staging-v02.api.letsencrypt.org/directory`, since production CAs rate-limit failed and duplicate orders.

## Configuration

### The `acme` Section

| Setting | Description | Default |
|---|---|---|
| `wait_on_startup` | Holds readiness at startup until missing certificates are issued, or this long at most. See [Startup Wait](#startup-wait). | `0` (no wait) |
| `storage.provider` | `filesystem` or `redis`. See [Storage and Clusters](#storage-and-clusters). | `filesystem` |
| `storage.path` | The storage directory, required for `filesystem` storage. | none |
| `storage.redis.cache_name` | Borrows the connection settings of a named `redis` cache. | none |
| `storage.redis.connection` | Redis connection settings, with the same fields as a cache's `redis` section, when `cache_name` is not used. | none |
| `storage.redis.key_prefix` | Namespaces every ACME key. | `trickster:acme:` |
| `storage.redis.lock_ttl` | The lease of a distributed lock, refreshed while it is held. At least `5s`. | `1m` |
| `issuers` | Named issuers, which backends reference. At least one is required. | none |
| `on_demand` | Enables issuance during the first handshake for names no backend lists. See [On-Demand TLS](#on-demand-tls). | none |

Each issuer accepts:

| Setting | Description | Default |
|---|---|---|
| `directory_url` | The CA's ACME directory. | Let's Encrypt production |
| `email` | The account contact the CA sends expiry and policy notices to. | none |
| `agree_to_terms` | Accepts the CA's subscriber agreement. It must be `true`; Trickster never accepts terms implicitly. | `false` |
| `challenges` | The challenge types the issuer may use: `http-01`, `tls-alpn-01`, `dns-01`. | `[http-01, tls-alpn-01]`, or `[dns-01]` with a `dns_provider` |
| `key_type` | `ecdsa-p256`, `ecdsa-p384`, `rsa-2048`, `rsa-4096` or `ed25519`. | `ecdsa-p256` |
| `profile` | A certificate profile the CA advertises in its directory, such as Let's Encrypt's `shortlived`. | none |
| `external_account_binding` | `key_id`, and `hmac_key` or `hmac_key_file`, for CAs that require an existing account, such as ZeroSSL and Google Trust Services. | none |
| `trusted_ca_paths` | PEM files trusted, in addition to the system roots, for the CA's own HTTPS endpoint, as a private CA needs. | none |
| `dns_provider` | The DNS API that answers `dns-01` challenges. See [DNS-01](#dns-01). | none |

### A Backend's `tls.acme`

| Setting | Description | Default |
|---|---|---|
| `issuer` | The issuer the backend's certificates come from. | required |
| `domains` | The names to request certificates for. | the backend's `hosts` |

A backend that uses `tls.acme` cannot also set `full_chain_cert_path` or `private_key_path`. Its listeners must be `http` listeners with a `tls_port`. Other TLS settings, such as `certificate_authority_paths` for the origin, are unaffected.

## Domains

Trickster requests one certificate per name. Names are lowercased, a trailing dot is dropped, and duplicates are removed. Each name must have at least two labels, and IP addresses are not supported.

- A wildcard such as `*.example.com` requires an issuer that uses `dns-01`. It covers exactly one label, so it matches `a.example.com` but neither `example.com` nor `a.b.example.com`.
- A backend whose `hosts` include an any-depth pattern such as `**.example.com` must list the names it wants certificates for in `tls.acme.domains`, since no certificate can cover every depth.
- A name belongs to exactly one issuer. Several backends may list it if they all use the same issuer.

## Challenges

### HTTP-01

The CA requests `http://<name>/.well-known/acme-challenge/<token>` on port 80. Trickster answers on the plaintext port of each listener that serves an `http-01` domain, ahead of every route and middleware, so IP and geo access lists, rate limits and authenticators never block a CA's validation. Only a pending token is answered. Any other request under `/.well-known/acme-challenge/` gets an empty `404` and never reaches a backend. A listener that serves no `http-01` domain does no extra work per request.

An issuer that allows `http-01` but has no listener with a plaintext port cannot use it, and validation logs a warning.

### TLS-ALPN-01

The CA connects to port 443 offering only the `acme-tls/1` protocol ([RFC 8737](https://www.rfc-editor.org/rfc/rfc8737)). Trickster answers those handshakes with the validation certificate, and every other handshake costs a single length comparison. TLS-ALPN-01 runs over TCP, so the HTTP/3 endpoint never answers it.

### DNS-01

DNS-01 publishes a TXT record through a DNS provider's API. It is the only challenge that can issue wildcards, and it works for names the CA cannot reach. An issuer using `dns-01` uses it exclusively.

```yaml
acme:
  issuers:
    letsencrypt-dns:
      email: ops@example.com
      agree_to_terms: true
      dns_provider:
        provider: cloudflare
        cloudflare:
          api_token_file: /etc/trickster/secrets/cloudflare-token
```

| Provider | Settings |
|---|---|
| `cloudflare` | `api_token` or `api_token_file` (Zone.DNS:Edit), and optionally `zone_token` or `zone_token_file` |
| `route53` | Optionally `region`, `profile`, `hosted_zone_id`, and `access_key_id` with `secret_access_key` or `secret_access_key_file`; unset credentials use the AWS default credential chain |
| `rfc2136` | `server` (host:port), `key_name`, `key_alg` (such as `hmac-sha256`), and `key` or `key_file`; works with BIND, PowerDNS, Knot and other servers that accept dynamic updates |

Every provider also accepts `propagation_delay`, a wait before checking that the record is visible; `propagation_timeout`, which bounds that check (default 2 minutes, and a negative value skips it); and `resolvers`, the DNS servers (host:port) the check queries.

Secrets set directly in the configuration are redacted from the config handler and every config dump. The `_file` variants keep them out of the configuration altogether.

## Storage and Clusters

ACME accounts, certificates, private keys and locks are kept in storage, so a restart serves the certificates it already has instead of ordering new ones.

**Filesystem** storage keeps everything under `storage.path`, in directories created with mode 0700 and files with mode 0600. Lock files make it safe for several processes on one host. It is not safe on network filesystems with weak locking semantics.

**Redis** storage lets every Trickster instance that shares the keyspace share certificates. Distributed locks ensure that only one instance orders or renews a given name. Any instance can answer a challenge another instance started, so a load balancer may send the CA's validation to any of them. Every key carries one hash tag, so the keyspace stays within a single slot on Redis Cluster.

```yaml
acme:
  storage:
    provider: redis
    redis:
      cache_name: shared-redis   # borrow the connection settings of a redis cache
      key_prefix: "trickster:acme:"
```

ACME storage never shares the cache's keyspace, even when it borrows a cache's connection settings. Eviction would destroy account keys and certificates, so the Redis server must not evict these keys: run it with `maxmemory-policy noeviction`, or without `maxmemory`.

## Startup Wait

A brand-new deployment has no certificates until the CA issues them, and handshakes for those names fail in the meantime. `wait_on_startup` holds readiness until they are issued:

```yaml
acme:
  wait_on_startup: 90s
```

- Listeners bind immediately, because the CA's challenges must reach them. What waits is the readiness endpoint (`/trickster/ready`), which reports `503` with the body `certificates pending`, so load balancers and Kubernetes probes keep traffic away.
- Only names without a usable certificate in storage are waited for, and an expired stored certificate counts as missing. A routine restart therefore becomes ready immediately.
- When the duration elapses, readiness is released anyway. Trickster logs an error for each name still missing, with the CA's last error, and issuance keeps retrying in the background. Holding readiness indefinitely would let a CA outage take every instance out of rotation, including those serving backends that don't use ACME.
- The wait applies only at startup. Names added by a configuration reload are issued in the background and never return readiness to `503`.

## On-Demand TLS

On-demand TLS issues a certificate during the first handshake for a name no backend lists. This suits a CDN or a SaaS platform whose customers bring their own domains. Because it lets clients cause certificate orders, it requires at least one gate:

```yaml
acme:
  on_demand:
    issuer: letsencrypt
    listeners: [default]
    ask: http://127.0.0.1:9000/allowed          # GET ?domain=<name>; a 200 permits issuance
    allowed_domains: ["*.customers.example.com"] # names must also match one of these
    decision_ttl: 5m        # how long an answer or a refusal is remembered
    rate_limit: 10          # most issuances started per minute
    negative_cache_size: 10000
```

- When both `ask` and `allowed_domains` are set, a name must pass both.
- Refused names are remembered for `decision_ttl`, so repeated handshakes for an unwanted name cost a table lookup, not a request to `ask`.
- The first handshake for a permitted name waits while the certificate is issued, which typically takes a few seconds. Later handshakes are answered from the listener's certificate store.
- On-demand issuance cannot use a `dns-01` issuer, and it applies only to TLS over TCP. Handshakes on the HTTP/3 endpoint are served from the store.
- A handshake for a name the store already covers never reaches the on-demand gates.

## Renewals and Reloads

Certificates are renewed during the last third of their lifetime, or earlier when the CA advises it through ACME Renewal Information ([RFC 9773](https://www.rfc-editor.org/rfc/rfc9773)). A failed order or renewal is retried with backoff. Every renewal is hot-swapped into the listeners without dropping established connections.

A configuration reload reconciles what is managed:

- New names are ordered in the background.
- Removed names stop being served. Their certificates are never revoked, and they remain in storage, so adding a name back is immediate.
- When an issuer's settings change, its names keep serving their current certificates until the reconfigured issuer replaces them.
- Changing `storage` restarts certificate management against the new storage.

## Management API

The mgmt listener serves the ACME handler at `/trickster/acme` (configurable via `mgmt.acme_handler_path`). It is served only on the mgmt listener, never on proxy listeners.

- `GET /trickster/acme` lists every managed name with its issuer.
- `POST /trickster/acme?domain=<name>` starts a forced renewal in the background and responds `202 Accepted`. An unmanaged name gets `404`.

ACME-managed certificates also appear in the [certificate inventory](./tls.md#certificate-inventory-mgmt) with the source `acme` and the id `acme:<issuer>:<name>`.

## Observability

The TLS certificate metrics, such as `trickster_tls_certificate_expiration_time_seconds`, cover ACME-managed certificates as well. These metrics are specific to ACME:

- `trickster_acme_orders_total` and `trickster_acme_renewals_total`, by `issuer` and `result` (`success` or `failure`)
- `trickster_acme_challenge_requests_total`, by `type` (`http-01` or `tls-alpn-01`) and `result` (`served`, `unknown` or `error`)
- `trickster_acme_on_demand_decisions_total`, by `result` (`allowed`, `refused`, `refused_cached`, `rate_limited` or `ask_error`)
- `trickster_acme_startup_wait_seconds` and `trickster_acme_startup_wait_timeouts_total`

See [metrics.md](./metrics.md) for details.

## Kubernetes

ACME is not supported together with the [Kubernetes controller](./kubernetes-gateway.md). With the controller, certificates come from TLS Secrets, and [cert-manager](https://cert-manager.io/) is the established way to issue them. A Trickster Deployment that does not run the controller can use ACME, with a persistent volume for filesystem storage, or with Redis storage when it has several replicas.

## Limitations

- ACME certificates are served on `http` listeners only. Native-protocol listeners (`postgres`, `mysql`, `flight-sql`, `clickhouse`) and `tls` stream listeners use certificate files.
- IP-address certificates and certificate revocation are not supported.
