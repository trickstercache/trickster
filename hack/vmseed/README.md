# vmseed

Loads the developer VictoriaMetrics instance. The `victoriametrics_seed`
Compose service first runs `hack/devorigin backfill`, which writes the last 15
days of trips metrics as OpenMetrics, then runs this command. No external Go
dependencies or downloads are needed.

```sh
SEED_TARGET=victoriametrics make developer-seed-data
cd hack/vmseed && go test ./...
```

Configuration: `VM_URL`, `VM_SEED_OM` (the backfill file, removed after each
run), `VM_SEED_INSTANCE` (the live `devorigin` scrape target's address), and
`VM_SEED_TIMEOUT`. Defaults match the Compose developer environment.

- The trips history is streamed gzip-compressed to `/api/v1/import/prometheus`
  with `job=trips` and the `instance` label of the live scrape target, so the
  history and the live samples form one set of series.
- The history is imported only when VictoriaMetrics has none, since a running
  `devorigin` keeps its seed shift. `make developer-seed-data` empties the
  storage volume before reseeding.
- Edge-case (`job="trickster_fixtures"`) and Graphite (`vmgraphite.*`) fixtures
  are functions of their timestamps; each run appends the samples since the
  previous run, found by one canary series per fixture.
- The seeder never deletes series: re-importing deleted series can lose samples
  in VictoriaMetrics v1.153.0.
- After importing, the seeder resets the rollup result cache. It then polls until
  each import's exact sample count is searchable, and checks that Graphite find and
  tag autocompletion discover the fixture. Samples scraped live before the import
  are excluded from the trips count.

Do not point it at a production VictoriaMetrics.
