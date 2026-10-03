CREATE TABLE sparse_trips
(
    pickup_datetime TIMESTAMP,
    value DOUBLE
)
TIMESTAMP(pickup_datetime)
PARTITION BY DAY
WAL
