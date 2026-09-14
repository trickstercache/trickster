# seedgen

Deterministic generator for the developer environment's `trips` seed data.
It replaces a download of real ride data with a synthetic dataset for the
fictional city of Emberwick that has the same 45-column schema, a similar
label cardinality and distribution, and a similar daily and weekly usage
curve, generated with no network access.

```sh
go run . -out ../../docs/developer/environment/docker-compose-data/seed-data
go run . -verify-only            # regenerate to memory and compare the hash
go run . -profile small -force   # a tenth of the rows, overwrite cached output
make seed-generate               # from the repo root; SEED_PROFILE, SEED_FORCE
make seed-verify                 # from the repo root
```

Flags: `-out`, `-profile default|small`, `-rows-per-day N`, `-seed-epoch T`,
`-force`, `-verify-only`. Each has an environment fallback (`SEED_OUT`,
`SEED_PROFILE`, `SEED_ROWS_PER_DAY`, `SEED_EPOCH`).

## Output

* `trips_1.gz` and `trips_2.gz`: gzip TSV with a header row; days 1–42 and
  43–84 of a 12-week window that starts on Monday 2024-01-01 UTC. The gzip
  header has a zero modification time so the files are stable too.
* `seed-window.env`: row count, pickup/dropoff bounds, the seed instant and
  the `SHIFT_SECONDS` that the database loaders add to every timestamp so the
  window's midpoint lands on the seed instant.
* `seed-data.sha256`: the uncompressed SHA-256 of the last run, used to skip
  regeneration when the cached output already matches.

## Determinism

Every value is a splitmix64 hash of `(row number, column salt)`, so each
column is independently reproducible and re-theming the names does not move
any number. Money and distance are integers (cents, hundredths of a mile),
totals are exact sums, and rows are strictly time-ordered with strictly
increasing ids. The SHA-256 of the uncompressed stream for the `default` and
`small` profiles is pinned in `main.go`; the program fails if the output
differs. Only the uncompressed hash is pinned because compressed bytes can
change between Go releases.

## Changing the data

* `theme.go`: city, boroughs, neighborhoods and their shares, the airport,
  cab colours and vendor codes. Hand-named neighborhoods come first; the long
  tail is composed from the part lists.
* `calendar.go`: day-of-week factors and the weekday/weekend hourly curves,
  which are interpolated to a per-minute density with per-5-minute noise.
* `trip.go`: per-row derivations (distance, fare, duration, tips, tolls,
  surcharges, coordinates).

After an intentional change, run `go run . -verify-only` for each profile,
pin the printed hashes in `main.go`, and run `go test ./...`.
