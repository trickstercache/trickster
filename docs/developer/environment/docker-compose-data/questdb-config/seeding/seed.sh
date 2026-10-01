#!/bin/sh

#
#  Copyright 2026 The Trickster Authors
#
#  Licensed under the Apache License, Version 2.0 (the "License");
#  you may not use this file except in compliance with the License.
#  You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
#  Unless required by applicable law or agreed to in writing, software
#  distributed under the License is distributed on an "AS IS" BASIS,
#  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
#  See the License for the specific language governing permissions and
#  limitations under the License.
#

# seed.sh (QuestDB)
#
# This loads the same two generated trips files used by the other developer
# databases. Each file first goes through QuestDB's /imp endpoint into an
# all-STRING staging table. The following INSERT performs the timestamp shift,
# UTC date regeneration and explicit type conversion before the staging table
# is dropped. This keeps the source fixture and the QuestDB schema independent
# of the importer's type heuristics.

set -eu

SEED_DATA="${QUESTDB_SEED_DATA:-/seed-data}"
QDB_URL="${QUESTDB_URL:-http://questdb:9000}"
QDB_USER="${QUESTDB_HTTP_USER:-grafana_ro}"
QDB_PASSWORD="${QUESTDB_HTTP_PASSWORD:-${QDB_HTTP_PASSWORD:?QuestDB HTTP password is required}}"
FILE1="$SEED_DATA/trips_1.gz"
FILE2="$SEED_DATA/trips_2.gz"
SEED_METADATA="$SEED_DATA/seed-window.env"

sql_execute() {
    curl --fail --silent --show-error --user "$QDB_USER:$QDB_PASSWORD" \
        --get --data-urlencode "query=$1" "$QDB_URL/execute" >/dev/null
}

sql_export() {
    curl --fail --silent --show-error --user "$QDB_USER:$QDB_PASSWORD" \
        --get --data-urlencode "query=$1" "$QDB_URL/exp"
}

load_seed_metadata() {
    gzip -t "$FILE1"
    gzip -t "$FILE2"
    if [ ! -s "$SEED_METADATA" ]; then
        echo "seed metadata is missing; run seed_data_generate first" >&2
        exit 1
    fi
    # The metadata is generated from integers only by the shared fetcher.
    # shellcheck disable=SC1090
    . "$SEED_METADATA"
    for value in SOURCE_ROWS SOURCE_PICKUP_MIN_EPOCH SOURCE_PICKUP_MAX_EPOCH \
        SOURCE_DROPOFF_MIN_EPOCH SOURCE_DROPOFF_MAX_EPOCH SEED_EPOCH SHIFT_SECONDS; do
        eval "number=\${$value:-}"
        case "$number" in
            ''|*[!0-9-]*) echo "invalid $value in $SEED_METADATA" >&2; exit 1 ;;
        esac
    done
    TARGET_PICKUP_MIN_EPOCH=$((SOURCE_PICKUP_MIN_EPOCH + SHIFT_SECONDS))
    TARGET_PICKUP_MAX_EPOCH=$((SOURCE_PICKUP_MAX_EPOCH + SHIFT_SECONDS))
    TARGET_DROPOFF_MIN_EPOCH=$((SOURCE_DROPOFF_MIN_EPOCH + SHIFT_SECONDS))
    TARGET_DROPOFF_MAX_EPOCH=$((SOURCE_DROPOFF_MAX_EPOCH + SHIFT_SECONDS))
}

create_tables() {
    sql_execute 'DROP MATERIALIZED VIEW IF EXISTS trips_15m'
    sql_execute 'DROP TABLE IF EXISTS trips'
    sql_execute 'DROP TABLE IF EXISTS trips_staging'
    sql_execute 'DROP TABLE IF EXISTS sparse_trips'
    sql_execute "$(cat /seeding/create_trips.sql)"
    sql_execute "$(cat /seeding/create_sparse.sql)"
}

import_file() {
    file=$1
    echo "importing $file into all-string staging"
    response=$(gunzip -c "$file" | curl --fail --silent --show-error \
        --user "$QDB_USER:$QDB_PASSWORD" \
        --form-string "schema=$(cat /seeding/schema.json)" \
        --form 'data=@-' \
        "$QDB_URL/imp?fmt=json&name=trips_staging&overwrite=true&forceHeader=true&delimiter=%09&atomicity=abort")
    case "$response" in
        *'"status":"OK"'*'"rowsRejected":0'*) ;;
        *) echo "QuestDB rejected import: $response" >&2; exit 1 ;;
    esac
}

insert_staging() {
    sql_execute "$(cat <<SQL
INSERT INTO trips
SELECT
    cast(s.trip_id AS LONG),
    s.vendor_id,
    cast(dateadd('s', $SHIFT_SECONDS, cast(s.pickup_datetime AS TIMESTAMP)) AS DATE),
    dateadd('s', $SHIFT_SECONDS, cast(s.pickup_datetime AS TIMESTAMP)),
    datediff('s', cast('1970-01-01' AS TIMESTAMP),
             dateadd('s', $SHIFT_SECONDS, cast(s.pickup_datetime AS TIMESTAMP))),
    cast(dateadd('s', $SHIFT_SECONDS, cast(nullif(s.dropoff_datetime, '') AS TIMESTAMP)) AS DATE),
    dateadd('s', $SHIFT_SECONDS, cast(nullif(s.dropoff_datetime, '') AS TIMESTAMP)),
    cast(nullif(s.store_and_fwd_flag, '') AS INT),
    cast(nullif(s.rate_code_id, '') AS INT),
    cast(nullif(s.pickup_longitude, '') AS DOUBLE),
    cast(nullif(s.pickup_latitude, '') AS DOUBLE),
    cast(nullif(s.dropoff_longitude, '') AS DOUBLE),
    cast(nullif(s.dropoff_latitude, '') AS DOUBLE),
    cast(nullif(s.passenger_count, '') AS INT),
    cast(nullif(s.trip_distance, '') AS DOUBLE),
    cast(nullif(s.fare_amount, '') AS DOUBLE),
    cast(nullif(s.extra, '') AS DOUBLE),
    cast(nullif(s.transit_tax, '') AS DOUBLE),
    cast(nullif(s.tip_amount, '') AS DOUBLE),
    cast(nullif(s.tolls_amount, '') AS DOUBLE),
    cast(nullif(s.ehail_fee, '') AS DOUBLE),
    cast(nullif(s.improvement_surcharge, '') AS DOUBLE),
    cast(nullif(s.total_amount, '') AS DOUBLE),
    s.payment_type,
    cast(nullif(s.trip_type, '') AS INT),
    s.pickup,
    s.dropoff,
    s.cab_type,
    cast(nullif(s.pickup_zone_gid, '') AS INT),
    cast(nullif(s.pickup_tract_label, '') AS DOUBLE),
    cast(nullif(s.pickup_borough_code, '') AS INT),
    s.pickup_borough_name,
    s.pickup_tract_code,
    s.pickup_district_class,
    s.pickup_neighborhood_code,
    s.pickup_neighborhood_name,
    cast(nullif(s.pickup_ward, '') AS INT),
    cast(nullif(s.dropoff_zone_gid, '') AS INT),
    cast(nullif(s.dropoff_tract_label, '') AS DOUBLE),
    cast(nullif(s.dropoff_borough_code, '') AS INT),
    s.dropoff_borough_name,
    s.dropoff_tract_code,
    s.dropoff_district_class,
    s.dropoff_neighborhood_code,
    s.dropoff_neighborhood_name,
    cast(nullif(s.dropoff_ward, '') AS INT)
FROM trips_staging s
SQL
)"
}

load_file() {
    file=$1
    sql_execute 'DROP TABLE IF EXISTS trips_staging'
    import_file "$file"
    insert_staging
    sql_execute 'DROP TABLE trips_staging'
}

seed_sparse_table() {
    # Keep two observations thirty minutes apart. SAMPLE BY FILL(NULL) then
    # has a gap whose rendered rows depend on the selected range.
    sql_execute "INSERT INTO sparse_trips
        SELECT dateadd('m', 2, pickup_datetime), 1.0
        FROM trips ORDER BY pickup_datetime LIMIT 1"
    sql_execute "INSERT INTO sparse_trips
        SELECT dateadd('m', 32, pickup_datetime), 2.0
        FROM trips ORDER BY pickup_datetime LIMIT 1"
}

validate_seed() {
    echo "validating seeded QuestDB data"
    facts=$(sql_export "SELECT count() AS rows,
        datediff('s', cast('1970-01-01' AS TIMESTAMP), min(pickup_datetime)),
        datediff('s', cast('1970-01-01' AS TIMESTAMP), max(pickup_datetime)),
        datediff('s', cast('1970-01-01' AS TIMESTAMP), min(dropoff_datetime)),
        datediff('s', cast('1970-01-01' AS TIMESTAMP), max(dropoff_datetime)),
        sum(CASE WHEN pickup_date != cast(pickup_datetime AS DATE) THEN 1 ELSE 0 END),
        sum(CASE WHEN dropoff_datetime IS NOT NULL
                 AND dropoff_date != cast(dropoff_datetime AS DATE)
                 THEN 1 ELSE 0 END),
        sum(CASE WHEN pickup_epoch != datediff('s',
                 cast('1970-01-01' AS TIMESTAMP), pickup_datetime)
                 THEN 1 ELSE 0 END)
        FROM trips")
    facts=$(printf '%s\n' "$facts" | tail -n 1 | tr -d '\r')
    IFS=, read -r rows min_pickup max_pickup min_dropoff max_dropoff \
        pickup_mismatches dropoff_mismatches epoch_mismatches <<EOF
$facts
EOF
    if [ "$rows" -ne "$SOURCE_ROWS" ] || [ "$rows" -le 0 ]; then
        echo "seed validation failed: expected $SOURCE_ROWS non-empty rows, got $rows" >&2
        exit 1
    fi
    if [ "$min_pickup" -ne "$TARGET_PICKUP_MIN_EPOCH" ] || \
       [ "$max_pickup" -ne "$TARGET_PICKUP_MAX_EPOCH" ] || \
       [ "$min_dropoff" -ne "$TARGET_DROPOFF_MIN_EPOCH" ] || \
       [ "$max_dropoff" -ne "$TARGET_DROPOFF_MAX_EPOCH" ]; then
        echo "seed validation failed: shifted timestamp bounds do not match source bounds" >&2
        exit 1
    fi
    if [ "$min_pickup" -gt "$SEED_EPOCH" ] || [ "$max_pickup" -lt "$SEED_EPOCH" ]; then
        echo "seed validation failed: pickup window does not straddle seed time" >&2
        exit 1
    fi
    before=$((SEED_EPOCH - min_pickup))
    after=$((max_pickup - SEED_EPOCH))
    imbalance=$((before - after))
    [ "$imbalance" -lt 0 ] && imbalance=$((-imbalance))
    if [ "$imbalance" -gt 1 ]; then
        echo "seed validation failed: pickup window is not centered on seed time" >&2
        exit 1
    fi
    if [ "$pickup_mismatches" -ne 0 ] || [ "$dropoff_mismatches" -ne 0 ] || \
       [ "$epoch_mismatches" -ne 0 ]; then
        echo "seed validation failed: date/datetime or epoch mismatch" >&2
        exit 1
    fi
}

wait_for_rollup() {
    expected=$1
    total=0
    i=0
    while [ "$i" -lt 60 ]; do
        total=$(sql_export 'SELECT coalesce(sum(trips), 0) AS total FROM trips_15m' \
            | tail -n 1 | tr -d '\r')
        [ -n "$total" ] || total=0
        [ "$total" = "$expected" ] && break
        i=$((i + 1))
        sleep 1
    done
    if [ "$total" != "$expected" ]; then
        echo "materialized view did not catch up: expected $expected rows, got $total" >&2
        exit 1
    fi
}

load_seed_metadata
create_tables
load_file "$FILE1"
load_file "$FILE2"
seed_sparse_table
sql_execute "$(cat /seeding/create_rollup.sql)"
wait_for_rollup "$SOURCE_ROWS"
validate_seed
echo "seed complete: $SOURCE_ROWS rows, pickup window $TARGET_PICKUP_MIN_EPOCH..$TARGET_PICKUP_MAX_EPOCH"
