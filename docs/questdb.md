# QuestDB Provider

Use `provider: questdb` for a QuestDB backend that exposes both the HTTP SQL
surface and the PostgreSQL wire protocol. Trickster keeps the HTTP surface as
an authenticated passthrough and uses the native pgwire path for the verified
SQL relay and cache contract.

## Surfaces

| Surface | Origin | Behavior |
| --- | --- | --- |
| HTTP SQL, Web Console and other HTTP paths | QuestDB HTTP, normally `9000` | Passthrough; no HTTP SQL delta cache is applied |
| PostgreSQL wire protocol | QuestDB pgwire, normally `8812` | Simple-query relay, object cache and verified fixed-width delta cache |
| Extended pgwire statements | QuestDB pgwire | Relayed; extended statements are not analyzed for delta caching |

The native listener uses the shared PostgreSQL wire adapter. A QuestDB backend
can therefore be mapped to an HTTP listener and a `protocol: postgres`
listener at the same time. The provider name, rather than `postgres`, selects
QuestDB's SQL analyzer and result semantics.

## Configuration

The HTTP origin and native origin are separate. Put the HTTP endpoint in
`origin_url` and the pgwire endpoint, including its credentials, in
`postgres.upstream_url`:

```yaml
listeners:
  questdb-pg:
    protocol: postgres
    port: 8490

authenticators:
  questdb-readers:
    provider: basic
    users:
      grafana_ro: ${QUESTDB_CLIENT_PASSWORD}

backends:
  questdb:
    provider: questdb
    origin_url: https://questdb.example:9000
    listener_names: [default, questdb-pg]
    authenticator_name: questdb-readers
    postgres:
      upstream_url: postgres://grafana_ro:REPLACE_ME@questdb.example:8812/qdb
      upstream_tls_mode: disable
    healthcheck:
      interval: 5s
      timeout: 3s
```

Use TLS and deployment-specific credentials in a real installation. The
developer environment uses cleartext QuestDB pgwire because QuestDB OSS does
not provide pgwire TLS in the tested image. `proxy_preserve` is only suitable
when the HTTP origin is configured to accept the same client credential; the
native upstream URL is the source of the pgwire origin credential.

If the backend is native-only, map only `questdb-pg` and use a PostgreSQL
`origin_url` with an explicit user. Without an HTTP listener, the provider
uses its authenticated pgwire health probe. With an HTTP listener, the health
check uses authenticated QuestDB `/execute` and the native listener still uses
the `postgres.upstream_url`.

## SQL Cache Contract

The QuestDB analyzer has a deliberately narrow delta-cache surface:

| Query shape | Result |
| --- | --- |
| `SAMPLE BY 5m`, `15m`, and other positive fixed `s/m/h/d` widths | Delta cache |
| `SAMPLE BY ... FILL(NULL)` | Object cache; fill rows depend on the selected range |
| `timestamp_floor('5m', timestamp_column)` with grouped output | Delta cache |
| `floor(extract(epoch FROM ts)/N)*N` | Delta cache when the bounds and result are renderable |
| Other deterministic reads | Object cache or relay, according to the normal pgwire policy |
| `FILL(PREV)`, `SAMPLE BY FROM ... TO ...`, calendar `M` widths, ambiguous grouping | Object/relay; no speculative rewrite |
| Writes, volatile statements, malformed or unknown time ranges | Relay; never a delta plan |

For `SAMPLE BY`, QuestDB applies the designated timestamp to the first plain
selected column and implicitly groups the remaining plain selected columns.
The adapter only accepts that unambiguous form. An explicit `GROUP BY`, a
computed first item or another ambiguous shape fails closed rather than
guessing the series identity.

The pgwire result path maps QuestDB's verified PostgreSQL type OIDs and
microsecond timestamp text to the shared time-series representation. Session
settings that can affect results or rendering, including timezone, date style,
interval style, float/bytea output and search path, remain part of the cache
identity. Unknown startup settings also partition the identity until observed;
unknown `SET` statements bypass caching conservatively.

## Developer Environment

The reproducible developer environment adds a pinned QuestDB OSS image, a
read-only pgwire role, an HTTP admin role for seeding, and the official
`questdb-questdb-datasource` Grafana plugin. It reuses the shared generated
trips fixture rather than downloading another dataset.

See [QuestDB Details](developer/environment/README.md#questdb-details) for
ports, seed commands, dashboard datasource names and direct-versus-proxy
checks. The versioned analyzer contract is in
[`pkg/backends/questdb/testdata/compatibility`](../pkg/backends/questdb/testdata/compatibility/README.md).

The integration conformance target compares direct and proxied QuestDB
results, protocol errors and session state. It also checks object and delta
cache counters, so a successful passthrough response cannot be mistaken for a
cache hit.

## Known Limits

- HTTP SQL is passthrough in this provider; native pgwire is the only path
  with QuestDB-specific delta caching in this change.
- Extended pgwire messages are relayed and are not delta-cacheable.
- QuestDB `FILL(PREV)`, clause-owned `FROM`/`TO` ranges and calendar-month
  widths remain object/relay paths.
- QuestDB OSS cleartext pgwire and the credentials in the developer compose
  file are for isolated local testing only.
