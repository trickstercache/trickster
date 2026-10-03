# QuestDB Compatibility Corpus

`v1.json` is the executable specification for the QuestDB SQL analyzer. It
covers the fixed-width `SAMPLE BY`, `timestamp_floor` and epoch-floor shapes
that the pgwire path can delta-cache, including series columns. It also records
the QuestDB clauses that deliberately remain on the object or relay path,
including range-dependent `FILL(NULL)`.

The delta cases use an intentionally unaligned UTC range from
`2026-09-18T08:00:03.123456Z` through `2026-09-18T11:00:07.456789Z`. QuestDB's
pgwire timestamp text carries microseconds, so the corpus uses microsecond
rendering and verifies the exact cache extent after rendering.

The official QuestDB Grafana datasource uses its own SQL target model rather
than Grafana's PostgreSQL macro families. The dashboard queries in the
developer environment are therefore covered by the direct-origin and pgwire
integration checks, while this corpus keeps the analyzer contract independent
of a plugin's query-builder expansion.

Extended pgwire statements are relayed and are not represented here as delta
plans. Unsupported `FILL(NULL)`, `FILL(PREV)`, `FROM`/`TO`, calendar-month and
ambiguous grouping forms are intentionally expected to fail closed.
