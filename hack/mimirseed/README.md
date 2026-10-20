# mimirseed

Loads the developer Mimir instances of the `mimir` Compose profile, for TSM
global evaluation. The `mimir_seed` Compose service runs this command, which
needs no external Go dependencies or downloads.

```sh
make developer-start-mimir
cd hack/mimirseed && go test ./...
```

Configuration: `MIMIR_URLS`, a comma-separated list of `region=url` pairs for
the `east` and `west` regions (default
`east=http://127.0.0.1:11080,west=http://127.0.0.1:11081`), and `MIMIR_TENANT`,
which sets the `X-Scope-OrgID` header for a multitenant Mimir. The developer
Mimir instances are single-tenant, so the Compose service sets no tenant.

- Each region holds its own pods, the pod `s1` that both regions hold with
  identical labels, and a backup copy of one of the other region's pods,
  labeled `isBackup="true"`. The regions hold 5 distinct pods between them.
- Every pod reports the counter `tsm_pod_cpu_seconds_total`, whose value is its
  timestamp in seconds, so every pod's `rate()` is exactly 1, and a sum over
  pods is exact.
- The seeder waits for each region to be ready, backfills 3 hours through
  remote write (`/api/v1/push`), then writes a new sample every 15 seconds
  until it is stopped. Rewriting samples that Mimir already holds, as a restart
  does, leaves them unchanged.
- Remote write requests are hand-encoded protobuf, compressed with a
  literal-only snappy encoder that every snappy decoder reads.

Do not point it at a production Mimir.
