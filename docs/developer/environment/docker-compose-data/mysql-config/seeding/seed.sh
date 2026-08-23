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

# seed.sh (MySQL)
#
# This loads the same 2 large files of NYC taxi data used by the ClickHouse
# seeder into the local MySQL database. The download cache directory is shared
# with the ClickHouse seeder (mounted at /seeding/data), so whichever seeder
# runs first downloads the files and the other reuses them. During loading,
# all pickup and dropoff dates are shifted by the one offset derived from the
# source dataset's actual pickup bounds. The source midpoint lands on the seed
# instant, preserving trip durations and date/datetime relationships while
# placing approximately half of the distribution on either side of seed time.
#
# Every run of the script will truncate the trips table and re-seed.
# So developers can run this once every 2 months to always have "real-time"
# data in their local dev environment.

set -e
cd /seeding

FILE1="data/trips_1.gz"
FILE2="data/trips_2.gz"
SEED_METADATA="data/seed-window.env"

MYSQL_HOST="${MYSQL_SERVER_ADDR:-mysql}"
MYSQL_DB="${MYSQL_DATABASE:-trickster}"
MYSQL_USER="${MYSQL_SEED_USER:-seeder}"
MYSQL_PASSWORD="${MYSQL_SEED_PASSWORD:-trickster-dev-seed}"

mysql_cmd() {
    MYSQL_PWD="$MYSQL_PASSWORD" mysql --host "$MYSQL_HOST" --user "$MYSQL_USER" \
        --local-infile=1 "$MYSQL_DB" "$@"
}

load_seed_metadata() {
    gzip -t "$FILE1"
    gzip -t "$FILE2"
    if [ ! -s "$SEED_METADATA" ]; then
        echo "seed metadata is missing; run the seed_data_fetch service first"
        exit 1
    fi
    # The metadata is generated from integers only by the shared fetcher.
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

create_truncate_table_mysql() {
    echo "creating/truncating trips table"
    mysql_cmd < create_truncate_trips_table.sql
}

load_file_transform_to_mysql() {
    echo "loading $1 with shift_seconds=$SHIFT_SECONDS"
    gunzip -c "$1" | mysql_cmd -e "SET SESSION sql_mode=''; SET SESSION time_zone='+00:00';
        LOAD DATA LOCAL INFILE '/dev/stdin' INTO TABLE trips
        CHARACTER SET binary
        FIELDS TERMINATED BY '\t' ESCAPED BY '\\\\'
        LINES TERMINATED BY '\n'
        IGNORE 1 LINES
        (trip_id, vendor_id, @pickup_date, @pickup_datetime,
         @dropoff_date, @dropoff_datetime, store_and_fwd_flag, rate_code_id,
         pickup_longitude, pickup_latitude, dropoff_longitude, dropoff_latitude,
         passenger_count, trip_distance, fare_amount, extra, mta_tax, tip_amount,
         tolls_amount, ehail_fee, improvement_surcharge, total_amount, payment_type,
         trip_type, pickup, dropoff, cab_type, pickup_nyct2010_gid, pickup_ctlabel,
         pickup_borocode, pickup_ct2010, pickup_boroct2010, pickup_cdeligibil,
         pickup_ntacode, pickup_ntaname, pickup_puma, dropoff_nyct2010_gid,
         dropoff_ctlabel, dropoff_borocode, dropoff_ct2010, dropoff_boroct2010,
         dropoff_cdeligibil, dropoff_ntacode, dropoff_ntaname, dropoff_puma)
        SET pickup_datetime = DATE_ADD(STR_TO_DATE(@pickup_datetime, '%Y-%m-%d %H:%i:%s'),
                                       INTERVAL $SHIFT_SECONDS SECOND),
            pickup_date = DATE(DATE_ADD(STR_TO_DATE(@pickup_datetime, '%Y-%m-%d %H:%i:%s'),
                                        INTERVAL $SHIFT_SECONDS SECOND)),
            dropoff_datetime = DATE_ADD(STR_TO_DATE(NULLIF(@dropoff_datetime, ''), '%Y-%m-%d %H:%i:%s'),
                                        INTERVAL $SHIFT_SECONDS SECOND),
            dropoff_date = DATE(DATE_ADD(STR_TO_DATE(NULLIF(@dropoff_datetime, ''), '%Y-%m-%d %H:%i:%s'),
                                         INTERVAL $SHIFT_SECONDS SECOND));"
}

validate_seed() {
    echo "validating seeded data"
    facts=$(mysql_cmd -N -e "SET time_zone='+00:00';
        SELECT count(*),
               coalesce(unix_timestamp(min(pickup_datetime)), 0),
               coalesce(unix_timestamp(max(pickup_datetime)), 0),
               coalesce(unix_timestamp(min(dropoff_datetime)), 0),
               coalesce(unix_timestamp(max(dropoff_datetime)), 0),
               coalesce(sum(pickup_date <> date(pickup_datetime)), 0),
               coalesce(sum(dropoff_datetime IS NOT NULL AND
                            dropoff_date <> date(dropoff_datetime)), 0)
        FROM trips;" | tr '\t' ' ')
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
    if [ "$imbalance" -gt 1 ]; then
        echo "seed validation failed: pickup window is not centered on seed time"
        exit 1
    fi
    if [ "$pickup_mismatches" -ne 0 ] || [ "$dropoff_mismatches" -ne 0 ]; then
        echo "seed validation failed: date/datetime mismatch ($pickup_mismatches/$dropoff_mismatches)"
        exit 1
    fi
    indexes=$(mysql_cmd -N -e "SELECT count(DISTINCT index_name)
        FROM information_schema.statistics
        WHERE table_schema = '$MYSQL_DB' AND table_name = 'trips'
          AND index_name IN ('idx_pickup_datetime', 'idx_pickup_date',
                             'idx_cab_type_pickup_datetime');")
    if [ "$indexes" -ne 3 ]; then
        echo "seed validation failed: expected 3 query indexes, got $indexes"
        exit 1
    fi
    echo "seed complete: $rows rows, pickup window $min_pickup..$max_pickup, indexes=$indexes"
}

mkdir -p data
load_seed_metadata

create_truncate_table_mysql

load_file_transform_to_mysql "$FILE1"
load_file_transform_to_mysql "$FILE2"

validate_seed
