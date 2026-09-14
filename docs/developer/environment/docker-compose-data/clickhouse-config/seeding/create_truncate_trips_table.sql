-- Dropping first lets schema changes (renamed columns, enum members) take
-- effect on an existing developer environment; every seed run reloads all rows.
DROP TABLE IF EXISTS trips_seed;
DROP TABLE IF EXISTS trips;

CREATE TABLE trips
(
    `trip_id` UInt32,
    `vendor_id` Enum8('1' = 1, '2' = 2, '3' = 3, '4' = 4, 'CMT' = 5, 'VTS' = 6, 'DDS' = 7, 'B02512' = 10, 'B02598' = 11, 'B02617' = 12, 'B02682' = 13, 'B02764' = 14, '' = 15),
    `pickup_date` Date,
    `pickup_datetime` DateTime,
    `dropoff_date` Date,
    `dropoff_datetime` DateTime,
    `store_and_fwd_flag` UInt8,
    `rate_code_id` UInt8,
    `pickup_longitude` Float64,
    `pickup_latitude` Float64,
    `dropoff_longitude` Float64,
    `dropoff_latitude` Float64,
    `passenger_count` UInt8,
    `trip_distance` Float64,
    `fare_amount` Float32,
    `extra` Float32,
    `transit_tax` Float32,
    `tip_amount` Float32,
    `tolls_amount` Float32,
    `ehail_fee` Float32,
    `improvement_surcharge` Float32,
    `total_amount` Float32,
    `payment_type` Enum8('UNK' = 0, 'CSH' = 1, 'CRE' = 2, 'NOC' = 3, 'DIS' = 4),
    `trip_type` UInt8,
    `pickup` FixedString(25),
    `dropoff` FixedString(25),
    `cab_type` Enum8('orange' = 1, 'blue' = 2, 'purple' = 3),
    `pickup_zone_gid` UInt8,
    `pickup_tract_label` Float32,
    `pickup_borough_code` Int8,
    `pickup_borough_name` String,
    `pickup_tract_code` String,
    `pickup_district_class` String,
    `pickup_neighborhood_code` FixedString(4),
    `pickup_neighborhood_name` String,
    `pickup_ward` UInt16,
    `dropoff_zone_gid` UInt8,
    `dropoff_tract_label` Float32,
    `dropoff_borough_code` UInt8,
    `dropoff_borough_name` String,
    `dropoff_tract_code` String,
    `dropoff_district_class` String,
    `dropoff_neighborhood_code` FixedString(4),
    `dropoff_neighborhood_name` String,
    `dropoff_ward` UInt16
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(pickup_date)
ORDER BY pickup_datetime;

CREATE TABLE trips_seed AS trips;
