# greptimeseed

Loads `hack/seedgen`'s shared gzip TSV files into the developer GreptimeDB
instance through authenticated HTTP SQL. No external Go dependencies or
additional fixture downloads are needed.

```sh
SEED_PROFILE=small SEED_TARGET=greptimedb make developer-seed-data
cd hack/greptimeseed && go test ./...
```

Configuration: `GREPTIMEDB_URL`, `GREPTIMEDB_DATABASE`,
`GREPTIMEDB_SEED_USER`, `GREPTIMEDB_SEED_PASSWORD`, `GREPTIMEDB_SEED_DATA`.
Defaults match the Compose developer environment. This is a destructive
fixture loader: each run drops and recreates `trips` and `trips_15m`.
Do not point it at a production database.

The loader checks the generated metadata and exact TSV column order, shifts
pickup/dropoff timestamps in UTC, regenerates their dates, and appends
`pickup_epoch` in seconds. Timestamp columns use microsecond precision.
Empty numeric fields become NULL; empty text stays empty text.

SQL INSERT batches preserve the TimescaleDB fixture's DATE, timestamp and
narrow numeric types. In GreptimeDB v1.2.1, line-protocol string fields
cannot populate existing DATE columns. The table uses low-cardinality
`cab_type` and `vendor_id` tags with append mode so coincident trips survive.
An ambiguous failed INSERT is not retried, since doing so could duplicate
rows. Re-run the entire seed instead.

After loading, the loader builds a static 15-minute rollup and checks row
count, shifted pickup/dropoff bounds, centering on the seed instant,
date/datetime and epoch agreement, and the rollup's total row count.
