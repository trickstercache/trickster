-- A continuous aggregate over the seeded hypertable, so a panel backed by
-- pre-aggregated buckets can be compared with the same panel over raw rows.
-- The seed data never changes after loading, so there is no refresh policy:
-- one full refresh materializes every bucket. Refreshing cannot run inside a
-- transaction block, so seed.sh runs this file without --single-transaction.

SET client_min_messages = warning;

CREATE MATERIALIZED VIEW trips_15m
WITH (timescaledb.continuous, timescaledb.materialized_only = true) AS
SELECT time_bucket(INTERVAL '15 minutes', pickup_datetime) AS bucket,
       cab_type,
       count(*) AS trips,
       sum(total_amount) AS total_amount_sum
FROM trips
GROUP BY 1, 2
WITH NO DATA;

CALL refresh_continuous_aggregate('trips_15m', NULL, NULL);
