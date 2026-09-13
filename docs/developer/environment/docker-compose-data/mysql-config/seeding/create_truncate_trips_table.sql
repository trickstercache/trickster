-- MySQL equivalent of the ClickHouse `trips` table used by the developer
-- environment. Type mappings from the ClickHouse schema:
--   Enum8            -> VARCHAR (MySQL ENUM cannot hold the '' member and
--                       adds no value for a dev dataset)
--   FixedString(N)   -> VARBINARY(N) (source TSV contains non-UTF8 bytes)
--   UInt8/UInt16/32  -> TINYINT/SMALLINT/INT UNSIGNED
--   Int8             -> TINYINT
--   Float32/Float64  -> FLOAT/DOUBLE
--   Date/DateTime    -> DATE/DATETIME (stored as UTC; server and Grafana
--                       sessions run with a UTC time zone)

CREATE TABLE IF NOT EXISTS trips
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
    mta_tax FLOAT,
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
    pickup_nyct2010_gid SMALLINT,
    pickup_ctlabel FLOAT,
    pickup_borocode TINYINT,
    pickup_ct2010 VARCHAR(16),
    pickup_boroct2010 VARCHAR(16),
    pickup_cdeligibil VARCHAR(4),
    pickup_ntacode VARBINARY(4),
    pickup_ntaname VARCHAR(128),
    pickup_puma SMALLINT UNSIGNED,
    dropoff_nyct2010_gid SMALLINT UNSIGNED,
    dropoff_ctlabel FLOAT,
    dropoff_borocode TINYINT UNSIGNED,
    dropoff_ct2010 VARCHAR(16),
    dropoff_boroct2010 VARCHAR(16),
    dropoff_cdeligibil VARCHAR(4),
    dropoff_ntacode VARBINARY(4),
    dropoff_ntaname VARCHAR(128),
    dropoff_puma SMALLINT UNSIGNED,
    KEY idx_pickup_datetime (pickup_datetime),
    KEY idx_pickup_date (pickup_date),
    KEY idx_cab_type_pickup_datetime (cab_type, pickup_datetime)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

TRUNCATE TABLE trips;
