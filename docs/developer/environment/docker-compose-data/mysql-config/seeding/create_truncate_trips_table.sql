-- MySQL equivalent of the ClickHouse `trips` table used by the developer
-- environment. Type mappings from the ClickHouse schema:
--   Enum8            -> VARCHAR (MySQL ENUM cannot hold the '' member and
--                       adds no value for a dev dataset)
--   FixedString(N)   -> VARBINARY(N)
--   UInt8/UInt16/32  -> TINYINT/SMALLINT/INT UNSIGNED
--   Int8             -> TINYINT
--   Float32/Float64  -> FLOAT/DOUBLE
--   Date/DateTime    -> DATE/DATETIME (stored as UTC; server and Grafana
--                       sessions run with a UTC time zone)

-- Dropping first lets schema changes take effect on an existing developer
-- environment; every seed run reloads all rows anyway.
DROP TABLE IF EXISTS trips;

CREATE TABLE trips
(
    trip_id INT UNSIGNED NOT NULL,
    vendor_id VARCHAR(8) NOT NULL,
    pickup_date DATE NOT NULL,
    pickup_datetime DATETIME NOT NULL,
    dropoff_date DATE,
    dropoff_datetime DATETIME,
    store_and_fwd_flag TINYINT UNSIGNED,
    rate_code_id TINYINT UNSIGNED,
    pickup_longitude DOUBLE,
    pickup_latitude DOUBLE,
    dropoff_longitude DOUBLE,
    dropoff_latitude DOUBLE,
    passenger_count TINYINT UNSIGNED,
    trip_distance DOUBLE,
    fare_amount FLOAT,
    extra FLOAT,
    transit_tax FLOAT,
    tip_amount FLOAT,
    tolls_amount FLOAT,
    ehail_fee FLOAT,
    improvement_surcharge FLOAT,
    total_amount FLOAT,
    payment_type VARCHAR(3),
    trip_type TINYINT UNSIGNED,
    pickup VARBINARY(25),
    dropoff VARBINARY(25),
    cab_type VARCHAR(6) NOT NULL,
    pickup_zone_gid SMALLINT UNSIGNED,
    pickup_tract_label FLOAT,
    pickup_borough_code TINYINT,
    pickup_borough_name VARCHAR(16),
    pickup_tract_code VARCHAR(16),
    pickup_district_class VARCHAR(4),
    pickup_neighborhood_code VARBINARY(4),
    pickup_neighborhood_name VARCHAR(128),
    pickup_ward SMALLINT UNSIGNED,
    dropoff_zone_gid SMALLINT UNSIGNED,
    dropoff_tract_label FLOAT,
    dropoff_borough_code TINYINT UNSIGNED,
    dropoff_borough_name VARCHAR(16),
    dropoff_tract_code VARCHAR(16),
    dropoff_district_class VARCHAR(4),
    dropoff_neighborhood_code VARBINARY(4),
    dropoff_neighborhood_name VARCHAR(128),
    dropoff_ward SMALLINT UNSIGNED,
    KEY idx_pickup_datetime (pickup_datetime),
    KEY idx_pickup_date (pickup_date),
    KEY idx_cab_type_pickup_datetime (cab_type, pickup_datetime)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

