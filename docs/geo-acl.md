# Geo ACLs

Geo ACLs restrict backends and paths by where their clients are. Other products call this
geo-restriction, geo-blocking, geo-filtering or geofencing. A geo ACL allows or denies countries,
first-level subdivisions (states, provinces, regions) and continents, and refuses a client in the
form its protocol expects: an HTTP status and body, a native database error, or a reset stream.

Two top-level sections configure it:

- `geo_locators` are named lookups that place a client address: a MaxMind DB format database, a list
  of RFC 8805 geofeed entries, or a location header a CDN in front of Trickster sets.
- `geo_acls` are named lists of the locations that may, or may not, pass. Each names the locator it
  uses, so many geo ACLs share one database.

Backends and paths name a geo ACL with `geo_acl_name`.

Trickster ships no location database and downloads none. You provide the file and keep it current;
see [Where location data comes from](#where-location-data-comes-from). To add a source of location
data, see [the locator provider guide](./developer/geo-locators.md).

A geo ACL is a compliance and licensing control, not a security boundary. A VPN, a proxy or Tor
places a client wherever it chooses.

## Example

```yaml
geo_locators:
  default:                             # a geo ACL uses the locator named default unless it names another
    provider: mmdb
    mmdb:
      path: /var/lib/GeoIP/GeoLite2-Country.mmdb

  city:
    provider: mmdb
    mmdb:
      path: /var/lib/GeoIP/GeoLite2-City.mmdb

  corrections:
    provider: geofeed
    geofeed:
      entries:
        - 203.0.113.0/24,DE,DE-BE
      files: [ /etc/trickster/private-relay.csv ]

geo_acls:
  north-america:
    allow: [ US, CA, MX ]
    exempt: [ private ]

  no-restricted-states:
    geo_locator_name: city             # subdivisions need a City-class database
    deny: [ US-TX, US-UT ]

  europe:
    allow: [ continent:EU ]

backends:
  video:
    provider: reverseproxycache
    origin_url: http://video.example.com
    geo_acl_name: north-america
    paths:
      - path: /trailers/
        geo_acl_name: none             # clears the backend's geo ACL for this path
      - path: /eu/
        geo_acl_name: europe           # replaces it for this path
  orders:
    provider: mysql
    geo_acl_name: no-restricted-states # a refused session gets MySQL error 1130
```

[example.full.yaml](../examples/conf/example.full.yaml) describes every option.

## Locators

Every locator has a `provider` and one options block named for it. A block for another provider, or
an unknown provider, fails validation.

### `mmdb`

Reads a MaxMind DB format file. That covers MaxMind GeoIP2 and GeoLite2, DB-IP, IPinfo, and
IP2Location's MMDB editions.

| Option | Default | Meaning |
|---|---|---|
| `path` | required | the database file |
| `schema` | `auto` | where records keep the location: `auto`, `geoip2`, `ipinfo` or `custom` |
| `fields` | | the record paths of `country` (required), `continent` and `subdivision`, for `schema: custom` |
| `reload_interval` | `5m` | how often the file is checked for a replacement, besides change events; 10s to 24h |
| `max_age` | `0` (never) | log a warning when the loaded file was built longer ago than this |

- **Schemas.** `geoip2` reads `country.iso_code`, `continent.code` and `subdivisions[0].iso_code`;
  MaxMind, DB-IP and IP2Location's MMDB editions use it. `ipinfo` reads `country_code` and
  `continent_code`, falling back to the `country` and `continent` codes of IPinfo's older files.
  `auto` chooses by the file's database type and confirms against the file's own records. A file
  that `auto` cannot read fails to load, and the error names `schema: custom`.
- **Custom paths** are lists of map keys and array indexes, such as `[subdivisions, 0, iso_code]`.
- **Fields a file serves.** A Country file serves countries and continents. A file serves
  subdivisions only when one of its records carries one, whatever its database type says; a load
  searches the whole file before deciding it serves none. A geo ACL with subdivision entries refuses
  a locator that serves none, when the configuration is applied.
- **In memory.** The file is read into memory, not mapped, so an updater that rewrites it in place
  can never fault the process. It costs the file's size, and twice that while a replacement loads.
  GeoLite2 Country is about 8 MB and GeoLite2 City about 65 MB; use a Country file in tight pods.
- **An IPv4-only file** logs a warning, since it places no IPv6 client.

### `geofeed`

Places addresses by [RFC 8805](https://www.rfc-editor.org/rfc/rfc8805.html) lines:
`prefix,country,region,city,postal code`. City and postal code are ignored.

| Option | Default | Meaning |
|---|---|---|
| `entries` | | inline lines |
| `files` | | geofeed files, watched for changes |
| `reload_interval` | `5m` | as for `mmdb` |

- At least one of `entries` and `files` is required. An inline entry that does not parse fails
  validation; a line in a file that does not parse is skipped, and a warning counts the skipped lines
  and shows the first.
- The longest prefix holding an address wins. An inline entry beats a file's for the same prefix,
  which makes inline entries a place for corrections.
- A line with every location field empty places no one: an address under it has no location, even
  when a shorter prefix would place it.
- `region` is a full ISO 3166-2 code, such as `US-AL`, whose country must agree with the line's. A
  country's continent is derived from the country.
- Feeds such as Apple's iCloud Private Relay egress ranges are published in this format.

### `header`

Reads a location header that a trusted upstream set, such as Cloudflare's `CF-IPCountry` or
CloudFront's `CloudFront-Viewer-Country` and `CloudFront-Viewer-Country-Region`.

| Option | Default | Meaning |
|---|---|---|
| `country` | required | the header holding a country code |
| `subdivision` | | a header holding a subdivision code, with or without its country |
| `continent` | | a header holding a continent code; otherwise the country's continent |
| `unknown_values` | `[XX, T1, ZZ, '-']` | values that mean no location |

- **Headers are believed only from the listener's `trusted_proxies`.** From any other peer they are
  not read, and the request has no location. A listener with no `trusted_proxies` believes no
  header, and a load warning says so.
- It judges HTTP requests only. A geo ACL whose locator is `header` cannot gate a backend that a
  native protocol or stream listener serves; validation refuses it.

### Replacing files

A replaced `mmdb` or `geofeed` file is loaded while serving, with no configuration reload. Write the
new file and rename it over the old one: a file written in place can be read half-written. A
replacement that fails to load leaves the last good file serving, counts a failed reload and logs
one warning. Replacements are refused when:

- an `mmdb` file does not open, or serves fewer location fields than the file it replaces;
- a `geofeed` file changed while it was read, or lost every entry where the old file had some.

A locator whose options are unchanged is kept across configuration reloads, with its loaded file.

## Geo ACLs

| Option | Default | Meaning |
|---|---|---|
| `geo_locator_name` | `default` | the locator that places clients |
| `allow` | | the only locations allowed |
| `deny` | | the locations denied |
| `unknown` | as an unlisted location | `allow` or `deny`: the verdict for a client with no location |
| `exempt` | | addresses and prefixes allowed with no lookup; `private` is every non-routable range |
| `action` | `reject` | `reject`, or `count`, which counts a denial and allows the client |
| `message` | see below | what a refused client is told, in every protocol |
| `response` | | the HTTP refusal's `status`, `headers` and `body` |

### Entries

A geo ACL has an `allow` list or a `deny` list, never both. An entry is one of:

| Spelling | Matches | Example |
|---|---|---|
| two letters | a country (ISO 3166-1 alpha-2) | `US` |
| country, hyphen, 1 to 3 letters or digits | a first-level subdivision (ISO 3166-2) | `US-TX`, `FR-75` |
| `continent:` and two letters | a continent: `AF` `AN` `AS` `EU` `NA` `OC` `SA` | `continent:EU` |

- Entries ignore case.
- A list is a union: `allow: [US, CA-QC]` is all of the United States and Quebec only.
- Country codes are checked against the assigned ISO 3166-1 codes, plus `XK` for Kosovo, which the
  databases use. An unassigned code fails with a suggestion: `UK` is not assigned; use `GB`.
- Continents take the `continent:` prefix because `AF`, `AS`, `NA` and `SA` are also countries.

### How a client is judged

1. An `exempt` address is allowed, with no lookup.
2. The locator places the address.
3. A client with no location (the locator had no answer, the lookup failed, or the address did not
   parse) takes the `unknown` verdict. Unset, that is the verdict of an unlisted location: denied by
   an `allow` list, allowed by a `deny` list.
4. Otherwise an `allow` list allows only what it lists, and a `deny` list denies only what it lists.
5. A denial under `action: count` is counted and allowed. On HTTP the request goes on carrying an
   `X-Trickster-Geo-Denied` header naming the geo ACL, for the origin's logs.

`private` stands for loopback, RFC 1918, link-local, carrier-grade NAT (100.64.0.0/10) and IPv6
unique local addresses. No database places them, so an `allow` list refuses them unless they are
exempt; a load warning says so for an `allow` list with neither `exempt` nor `unknown`.

### Where geo ACLs apply

- **Backends and paths.** A path's `geo_acl_name` replaces its backend's, and `geo_acl_name: none` on
  a path clears it. `none` is valid on paths only. Provider paths a configuration does not name take
  the backend's geo ACL.
- **HTTP.** The geo ACL runs in the route chain just outside the authenticator: a refused request
  never reaches a credential check, a mirror, the cache or the origin, and is logged and counted like
  any response. A backend's geo ACL is on its own route too, which ALB pools and rules dispatch into,
  so a pool member's geo ACL judges what an ALB sends it. Put the geo ACL on the backend a client
  reaches first: a fanout ALB treats a member's refusal as that member's response.
- **Native protocols.** A `mysql`, `postgres`, `clickhouse` or `flight-sql` listener judges each
  session by the geo ACL of the backend it maps to, before any credential is checked. `flight-sql`
  judges every call. `clickhouse` also judges every query, since it sends each one through its
  backend's HTTP route for `/`: the backend's geo ACL, or that of a `/` path that names its own, can
  refuse a query on an open session after a reload or a replaced file. See [Refusals](#refusals) for
  how that differs from a refused session. Otherwise a session admitted before a reload is not judged
  again. A backend that a native listener's ALB routes sessions to may not name a geo ACL, since the
  session is judged by the ALB's before it is routed; a path-level geo ACL on such a member judges its
  own HTTP requests.
- **Stream listeners.** A `tcp` or `udp` listener judges each connection or flow by its backend's geo
  ACL; a `tls` listener judges by the backend the server name selects. A refusal resets a `tcp` or
  `tls` connection and drops a `udp` flow's datagrams; no message can be sent on an opaque stream.
  An ALB on a stream listener dials its members without entering their routes, so a backend or
  discovery template in its pool may not name a geo ACL: set it on the ALB, which judges the
  connection before a member is dialed. A path-level geo ACL on such a member is refused too, unless
  HTTP also serves the member, whose requests it then judges. IP access lists follow the same rule.
- **Not gated:** the readiness path and the `mgmt` and `metrics` listeners.
- **Other gates** on the same route judge independently, and any refusal ends the request.

### The client address

- On HTTP it is the address the listener's `trusted_proxies` resolved from the forwarding headers,
  or else the peer's. A mirrored copy carries it, as does each query of a ClickHouse native session.
- On native and stream listeners it is the peer, or the source a trusted PROXY protocol header
  named.
- **A wrong `trusted_proxies` makes every client the proxy.** Behind a private address that is no
  location for everyone: an `allow` list refuses every client, which is noticed at once, and a
  `deny` list allows every client, which is not. Watch the rate of
  `trickster_geo_locator_lookups_total{result="not_found"}`; a listener with `proxy_protocol` and no
  `trusted_proxies` logs a load warning, since any client can name its own source.

## Refusals

Every protocol carries the geo ACL's `message`, which defaults to
`This resource is not available in your geographical area.` It is one line of at most 255 bytes.

| Client | Shown |
|---|---|
| HTTP | `403 Forbidden` and the message, as `text/plain; charset=utf-8` |
| `mysql` | `ERROR 1130 (HY000): <message>` |
| `psql` | `FATAL:  <message>`, SQLSTATE `28000` |
| `clickhouse-client` | `Code: 195. DB::Exception: <message>` (`IP_ADDRESS_NOT_ALLOWED`) |
| Flight SQL | gRPC status `PermissionDenied` with the message |
| `tcp`, `tls` | a reset |
| `udp` | nothing; the datagrams are dropped |

A `clickhouse` query refused on a session that was already admitted fails like any other query:
`Code: 62. DB::Exception: <message>`, where the message is the HTTP refusal's body, so a
`response.body` replaces it. The session stays open, and the refusal is counted with
`plane="http"`. Only a new session gets exception 195.

HTTP refusals:

- `response.status` is from 400 to 599. No status code is defined for a geographic refusal, so 403
  is the default, as CDNs answer.
- **451 is for a legal demand** ([RFC 7725](https://www.rfc-editor.org/rfc/rfc7725.html)), not a
  licensing or business restriction. A 451 should explain the demand in its body and carry a
  `Link: <uri>; rel="blocked-by"` header; a load warning names a 451 without one.
- `response.headers` are merged over the default `Cache-Control: no-store` and `Content-Type`.
- `response.body` replaces the message and its newline, for a JSON or HTML refusal.

### Caches in front of Trickster

Trickster marks its own refusals `no-store`. A shared cache in front of Trickster may still store
an allowed client's response and serve it to anyone. Serve a restricted path behind such a cache
with `Cache-Control: private`, or apply the same restriction at that cache.

## Where location data comes from

| Source | Format | Terms | Updates |
|---|---|---|---|
| MaxMind GeoIP2 (paid), GeoLite2 (free) | MMDB | an account and license key to download; attribution; a GeoLite file is replaced within 30 days of a release | GeoLite twice a week |
| DB-IP Lite | MMDB | CC BY 4.0, with a link back | monthly |
| IPinfo Lite | MMDB | CC BY-SA 4.0, with attribution; a token to download | frequent |
| IP2Location LITE | MMDB editions for some packages | free with attribution; sign-up | monthly |
| CDN headers | a request header | the CDN's | live |
| Geofeeds | RFC 8805 CSV | the publisher's | the publisher's |

- Licenses bind whoever distributes the file. Trickster ships none; a container image that bakes a
  GeoLite file in redistributes it.
- On Kubernetes, MaxMind's `geoipupdate` image can run as an init container and a sidecar writing to
  a volume that Trickster reads; see [Deploying on Kubernetes](./kubernetes-deploy.md).
- Accuracy falls with precision. MaxMind cites country accuracy above 99% with VPNs left out, state
  or province accuracy of 55% to 80%, and city accuracy of 20% to 75%. A subdivision list refuses
  some clients wrongly and admits others; `exempt` and a geofeed of corrections are the remedies.
- An address can move between networks daily. `max_age` and the build-time gauge show a stale file.
- Whether a restriction is lawful where it applies is the operator's question.

## Metrics

| Metric | Labels |
|---|---|
| `trickster_geo_acl_decisions_total` | `geo_acl`, `plane` (`http`, `native`, `stream`), `verdict` (`allow`, `deny`, `count`, `exempt`) |
| `trickster_geo_locator_lookups_total` | `geo_locator`, `result` (`found`, `not_found`, `error`) |
| `trickster_geo_locator_reloads_total` | `geo_locator`, `result` (`success`, `error`) |
| `trickster_geo_locator_build_timestamp_seconds` | `geo_locator`; `mmdb` only |

No metric carries a country. Refusals log at debug level only, so a flood of refused clients is not
a flood of log lines; a MySQL listener's failed logins log at debug too, and are counted by
`trickster_mysql_connection_errors_total`.

## Performance

Measured on an Apple M3 Max, none allocating:

| What | Time |
|---|---|
| a `geofeed` lookup, 10 or 300,000 entries | 11 to 15 ns |
| a geo ACL check over a `geofeed` | about 25 ns |
| the HTTP gate, an allowed request | about 50 ns over the route without it |
| an `mmdb` lookup, test database | about 105 ns |

An `mmdb` file is decoded once per record, then answered from a bounded cache of decoded records.
A route with no geo ACL is unchanged, and a native listener without one pays one atomic load per
session.

## Kubernetes

A geo ACL is an operator control, like an authenticator, so the controller takes it only from
operator-tier settings:

- `kubernetes.defaults.geo_acl_name` gates every route the controller generates. Since stream routes
  take it too, it must name a geo ACL whose locator places addresses.
- A GatewayClass's parameters can name `geo_acl_name` for the class's routes. A geo ACL whose locator
  reads headers serves the class's HTTP routes, and refuses its stream routes rather than serving
  them ungated.
- No Ingress annotation and no `TricksterCachePolicy` field names one, since whoever writes those in
  their own namespace could name a looser geo ACL, or none.
- The geo ACL goes on the backend a route attaches, never on a pool member, an endpoint template or
  a mirror target, so each request is judged once.

See [Kubernetes Ingress](./kubernetes-ingress.md) and [Kubernetes Gateway API](./kubernetes-gateway.md).
