CREATE MATERIALIZED VIEW trips_15m AS
SELECT
    pickup_datetime,
    cab_type,
    count() AS trips,
    sum(total_amount) AS total_amount
FROM trips
SAMPLE BY 15m
