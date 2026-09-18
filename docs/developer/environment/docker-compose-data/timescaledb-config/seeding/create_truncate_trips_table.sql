-- TimescaleDB equivalent of the MySQL/ClickHouse `trips` table used by the
-- developer environment. Type mappings from the MySQL schema (PostgreSQL has
-- no unsigned integers, so each unsigned type widens by one step):
--   TINYINT [UNSIGNED]  -> smallint
--   SMALLINT UNSIGNED   -> integer
--   INT UNSIGNED        -> bigint
--   FLOAT/DOUBLE        -> real/double precision
--   VARCHAR(N)          -> text (TimescaleDB warns against varchar(N) on
--                          hypertables and recommends text)
--   VARBINARY(N)        -> text (short ASCII codes; bytea would render as
--                          hex in Grafana)
--   DATE                -> date
--   DATETIME            -> timestamptz (UTC; the server and Grafana sessions
--                          run with a UTC time zone)

-- Hides the "does not exist, skipping" notices from the first run's drops.
SET client_min_messages = warning;

-- Dropping first lets schema changes take effect on an existing developer
-- environment; every seed run reloads all rows anyway. Dropping a hypertable
-- drops its chunks with it.
DROP TABLE IF EXISTS trips;
DROP TABLE IF EXISTS trips_staging;

CREATE TABLE trips
(
    trip_id bigint NOT NULL,
    vendor_id text NOT NULL,
    pickup_date date NOT NULL,
    pickup_datetime timestamptz NOT NULL,
    dropoff_date date,
    dropoff_datetime timestamptz,
    store_and_fwd_flag smallint,
    rate_code_id smallint,
    pickup_longitude double precision,
    pickup_latitude double precision,
    dropoff_longitude double precision,
    dropoff_latitude double precision,
    passenger_count smallint,
    trip_distance double precision,
    fare_amount real,
    extra real,
    transit_tax real,
    tip_amount real,
    tolls_amount real,
    ehail_fee real,
    improvement_surcharge real,
    total_amount real,
    payment_type text,
    trip_type smallint,
    pickup text,
    dropoff text,
    cab_type text NOT NULL,
    pickup_zone_gid integer,
    pickup_tract_label real,
    pickup_borough_code smallint,
    pickup_borough_name text,
    pickup_tract_code text,
    pickup_district_class text,
    pickup_neighborhood_code text,
    pickup_neighborhood_name text,
    pickup_ward integer,
    dropoff_zone_gid integer,
    dropoff_tract_label real,
    dropoff_borough_code smallint,
    dropoff_borough_name text,
    dropoff_tract_code text,
    dropoff_district_class text,
    dropoff_neighborhood_code text,
    dropoff_neighborhood_name text,
    dropoff_ward integer
);

-- Partitions on pickup_datetime with the default 7-day chunks (about 13 for
-- the 12-week dataset) and creates the default trips_pickup_datetime_idx.
SELECT create_hypertable('trips', by_range('pickup_datetime'));

CREATE INDEX idx_cab_type_pickup_datetime ON trips (cab_type, pickup_datetime);

-- COPY cannot transform values in flight the way MySQL's LOAD DATA ... SET
-- does, so each file lands here as raw text and load_trips_from_staging.sql
-- applies the shift and casts. The column names and order must match the
-- seed files' header line (the load uses COPY ... HEADER match).
CREATE UNLOGGED TABLE trips_staging
(
    trip_id text, vendor_id text, pickup_date text, pickup_datetime text,
    dropoff_date text, dropoff_datetime text, store_and_fwd_flag text,
    rate_code_id text, pickup_longitude text, pickup_latitude text,
    dropoff_longitude text, dropoff_latitude text, passenger_count text,
    trip_distance text, fare_amount text, extra text, transit_tax text,
    tip_amount text, tolls_amount text, ehail_fee text,
    improvement_surcharge text, total_amount text, payment_type text,
    trip_type text, pickup text, dropoff text, cab_type text,
    pickup_zone_gid text, pickup_tract_label text, pickup_borough_code text,
    pickup_borough_name text, pickup_tract_code text,
    pickup_district_class text, pickup_neighborhood_code text,
    pickup_neighborhood_name text, pickup_ward text, dropoff_zone_gid text,
    dropoff_tract_label text, dropoff_borough_code text,
    dropoff_borough_name text, dropoff_tract_code text,
    dropoff_district_class text, dropoff_neighborhood_code text,
    dropoff_neighborhood_name text, dropoff_ward text
);
