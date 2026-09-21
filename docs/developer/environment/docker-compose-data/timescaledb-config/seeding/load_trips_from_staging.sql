-- Moves one seed file's rows from trips_staging into the trips hypertable.
-- Run by seed.sh with `psql -v shift_seconds=<int>`. The seed timestamps carry
-- no zone and are UTC; AT TIME ZONE 'UTC' pins that reading (and the date
-- regeneration) so the result never depends on the session TimeZone. Empty
-- text columns stay '' to match the MySQL copy; empty values in nullable
-- non-text columns become NULL.

INSERT INTO trips
(
    trip_id, vendor_id, pickup_date, pickup_datetime, pickup_epoch, dropoff_date,
    dropoff_datetime, store_and_fwd_flag, rate_code_id, pickup_longitude,
    pickup_latitude, dropoff_longitude, dropoff_latitude, passenger_count,
    trip_distance, fare_amount, extra, transit_tax, tip_amount, tolls_amount,
    ehail_fee, improvement_surcharge, total_amount, payment_type, trip_type,
    pickup, dropoff, cab_type, pickup_zone_gid, pickup_tract_label,
    pickup_borough_code, pickup_borough_name, pickup_tract_code,
    pickup_district_class, pickup_neighborhood_code, pickup_neighborhood_name,
    pickup_ward, dropoff_zone_gid, dropoff_tract_label, dropoff_borough_code,
    dropoff_borough_name, dropoff_tract_code, dropoff_district_class,
    dropoff_neighborhood_code, dropoff_neighborhood_name, dropoff_ward
)
SELECT
    s.trip_id::bigint,
    s.vendor_id,
    (t.pickup_ts AT TIME ZONE 'UTC')::date,
    t.pickup_ts,
    extract(epoch FROM t.pickup_ts)::bigint,
    (t.dropoff_ts AT TIME ZONE 'UTC')::date,
    t.dropoff_ts,
    NULLIF(s.store_and_fwd_flag, '')::smallint,
    NULLIF(s.rate_code_id, '')::smallint,
    NULLIF(s.pickup_longitude, '')::double precision,
    NULLIF(s.pickup_latitude, '')::double precision,
    NULLIF(s.dropoff_longitude, '')::double precision,
    NULLIF(s.dropoff_latitude, '')::double precision,
    NULLIF(s.passenger_count, '')::smallint,
    NULLIF(s.trip_distance, '')::double precision,
    NULLIF(s.fare_amount, '')::real,
    NULLIF(s.extra, '')::real,
    NULLIF(s.transit_tax, '')::real,
    NULLIF(s.tip_amount, '')::real,
    NULLIF(s.tolls_amount, '')::real,
    NULLIF(s.ehail_fee, '')::real,
    NULLIF(s.improvement_surcharge, '')::real,
    NULLIF(s.total_amount, '')::real,
    s.payment_type,
    NULLIF(s.trip_type, '')::smallint,
    s.pickup,
    s.dropoff,
    s.cab_type,
    NULLIF(s.pickup_zone_gid, '')::integer,
    NULLIF(s.pickup_tract_label, '')::real,
    NULLIF(s.pickup_borough_code, '')::smallint,
    s.pickup_borough_name,
    s.pickup_tract_code,
    s.pickup_district_class,
    s.pickup_neighborhood_code,
    s.pickup_neighborhood_name,
    NULLIF(s.pickup_ward, '')::integer,
    NULLIF(s.dropoff_zone_gid, '')::integer,
    NULLIF(s.dropoff_tract_label, '')::real,
    NULLIF(s.dropoff_borough_code, '')::smallint,
    s.dropoff_borough_name,
    s.dropoff_tract_code,
    s.dropoff_district_class,
    s.dropoff_neighborhood_code,
    s.dropoff_neighborhood_name,
    NULLIF(s.dropoff_ward, '')::integer
FROM trips_staging s
CROSS JOIN LATERAL
(
    SELECT (s.pickup_datetime::timestamp AT TIME ZONE 'UTC')
               + (:shift_seconds) * interval '1 second' AS pickup_ts,
           (NULLIF(s.dropoff_datetime, '')::timestamp AT TIME ZONE 'UTC')
               + (:shift_seconds) * interval '1 second' AS dropoff_ts
) t;
