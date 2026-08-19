#!/bin/sh

# 
#  Copyright 2018 The Trickster Authors
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

# seed.sh
#
# This loads the two shared NYC taxi files into ClickHouse. The shared fetcher
# derives one offset from the actual source pickup bounds; ClickHouse and MySQL
# apply that exact offset to every pickup/dropoff date and datetime value.
#
# Every run of the script will truncate the trips table and re-seed.
# So developers can run this once every 2 months to always have "real-time" data
# in their local dev environment.

set -e
cd /seeding

FILE1="data/trips_1.gz"
FILE2="data/trips_2.gz"
SEED_METADATA="data/seed-window.env"
CH_HOST="${CH_SERVER_ADDR:-clickhouse}"

clickhouse_cmd() {
    clickhouse-client --host "$CH_HOST" --port 9000 --user default "$@"
}

load_seed_metadata() {
    gzip -t "$FILE1"
    gzip -t "$FILE2"
    if [ ! -s "$SEED_METADATA" ]; then
        echo "seed metadata is missing; run the seed_data_fetch service first"
        exit 1
    fi
    # shellcheck disable=SC1090
    . "$SEED_METADATA"
    for value in SOURCE_ROWS SOURCE_PICKUP_MIN_EPOCH SOURCE_PICKUP_MAX_EPOCH \
        SOURCE_DROPOFF_MIN_EPOCH SOURCE_DROPOFF_MAX_EPOCH SEED_EPOCH SHIFT_SECONDS; do
        eval "number=\${$value:-}"
        case "$number" in
            ''|*[!0-9-]*) echo "invalid $value in $SEED_METADATA"; exit 1 ;;
        esac
    done
    TARGET_PICKUP_MIN_EPOCH=$((SOURCE_PICKUP_MIN_EPOCH + SHIFT_SECONDS))
    TARGET_PICKUP_MAX_EPOCH=$((SOURCE_PICKUP_MAX_EPOCH + SHIFT_SECONDS))
    TARGET_DROPOFF_MIN_EPOCH=$((SOURCE_DROPOFF_MIN_EPOCH + SHIFT_SECONDS))
    TARGET_DROPOFF_MAX_EPOCH=$((SOURCE_DROPOFF_MAX_EPOCH + SHIFT_SECONDS))
}

wait_for_clickhouse() {
    attempts=30
    until clickhouse-client --host "${CH_SERVER_ADDR:-clickhouse}" --port 9000 \
        --user default --query "SELECT 1" >/dev/null 2>&1; do
        attempts=$((attempts - 1))
        if [ "$attempts" -eq 0 ]; then
            echo "ClickHouse did not become ready" >&2
            return 1
        fi
        echo "waiting for ClickHouse to become ready..."
        sleep 2
    done
}

create_truncate_table_clickhouse() {
    echo "truncating trips table"
    clickhouse_cmd < create_truncate_trips_table.sql
}

load_file_transform_to_clickhouse() {
    echo "loading raw source $1"
    gunzip -c "$1" | clickhouse_cmd \
        --query="INSERT INTO trips_seed FORMAT TabSeparatedWithNames"
}

apply_shift() {
    echo "applying shift_seconds=$SHIFT_SECONDS"
    clickhouse_cmd --query="INSERT INTO trips
        SELECT * REPLACE (
            toDate(pickup_datetime + toIntervalSecond($SHIFT_SECONDS)) AS pickup_date,
            pickup_datetime + toIntervalSecond($SHIFT_SECONDS) AS pickup_datetime,
            toDate(dropoff_datetime + toIntervalSecond($SHIFT_SECONDS)) AS dropoff_date,
            dropoff_datetime + toIntervalSecond($SHIFT_SECONDS) AS dropoff_datetime
        ) FROM trips_seed"
}

validate_seed() {
    echo "validating seeded data"
    facts=$(clickhouse_cmd --format=TSVRaw --query="SELECT
        count(), toUnixTimestamp(min(pickup_datetime)), toUnixTimestamp(max(pickup_datetime)),
        toUnixTimestamp(min(dropoff_datetime)), toUnixTimestamp(max(dropoff_datetime)),
        countIf(pickup_date != toDate(pickup_datetime)),
        countIf(dropoff_date != toDate(dropoff_datetime))
        FROM trips" | tr '\t' ' ')
    set -- $facts
    rows=$1
    min_pickup=$2
    max_pickup=$3
    min_dropoff=$4
    max_dropoff=$5
    pickup_mismatches=$6
    dropoff_mismatches=$7
    if [ "$rows" -ne "$SOURCE_ROWS" ] || [ "$rows" -le 0 ]; then
        echo "seed validation failed: expected $SOURCE_ROWS non-empty rows, got $rows"
        exit 1
    fi
    if [ "$min_pickup" -ne "$TARGET_PICKUP_MIN_EPOCH" ] || \
       [ "$max_pickup" -ne "$TARGET_PICKUP_MAX_EPOCH" ] || \
       [ "$min_dropoff" -ne "$TARGET_DROPOFF_MIN_EPOCH" ] || \
       [ "$max_dropoff" -ne "$TARGET_DROPOFF_MAX_EPOCH" ]; then
        echo "seed validation failed: shifted timestamp bounds do not match source bounds"
        exit 1
    fi
    if [ "$min_pickup" -gt "$SEED_EPOCH" ] || [ "$max_pickup" -lt "$SEED_EPOCH" ]; then
        echo "seed validation failed: pickup window does not straddle seed time"
        exit 1
    fi
    before=$((SEED_EPOCH - min_pickup))
    after=$((max_pickup - SEED_EPOCH))
    imbalance=$((before - after))
    [ "$imbalance" -lt 0 ] && imbalance=$((-imbalance))
    if [ "$imbalance" -gt 1 ] || [ "$pickup_mismatches" -ne 0 ] || \
       [ "$dropoff_mismatches" -ne 0 ]; then
        echo "seed validation failed: centering or date/datetime consistency"
        exit 1
    fi
    keys=$(clickhouse_cmd --format=TSVRaw --query="SELECT count()
        FROM system.tables WHERE database = currentDatabase() AND name = 'trips'
          AND partition_key = 'toYYYYMM(pickup_date)'
          AND sorting_key = 'pickup_datetime'")
    if [ "$keys" -ne 1 ]; then
        echo "seed validation failed: expected pickup partition and sorting keys"
        exit 1
    fi
    echo "seed complete: $rows rows, pickup window $min_pickup..$max_pickup, keys=$keys"
}

mkdir -p data
load_seed_metadata

wait_for_clickhouse
create_truncate_table_clickhouse

load_file_transform_to_clickhouse "$FILE1"
load_file_transform_to_clickhouse "$FILE2"
apply_shift
validate_seed
clickhouse_cmd --query="TRUNCATE TABLE trips_seed"
