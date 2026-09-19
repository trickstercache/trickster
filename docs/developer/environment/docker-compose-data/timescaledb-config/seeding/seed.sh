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

# seed.sh (TimescaleDB)
#
# This loads the same two generated trips files used by the ClickHouse and
# MySQL seeders into the local TimescaleDB database. The shared seed-data
# directory is mounted at /seeding/data and is populated by the
# seed_data_generate service. During loading, all pickup and dropoff dates are
# shifted by the one offset derived from the generated dataset's pickup bounds.
# The source midpoint lands on the seed instant, preserving trip durations and
# date/datetime relationships while placing approximately half of the
# distribution on either side of seed time.
#
# PostgreSQL's COPY cannot transform values in flight, so each file is copied
# into an UNLOGGED all-text staging table and then moved into the trips
# hypertable by load_trips_from_staging.sql, which applies the shift.
#
# Every run of the script will drop and re-create the trips table and re-seed.
# So developers can run this once every 2 months to always have "real-time"
# data in their local dev environment.

set -e
cd /seeding

FILE1="data/trips_1.gz"
FILE2="data/trips_2.gz"
SEED_METADATA="data/seed-window.env"

PG_HOST="${TIMESCALEDB_SERVER_ADDR:-timescaledb}"
PG_DB="${TIMESCALEDB_DATABASE:-trickster}"
PG_USER="${TIMESCALEDB_SEED_USER:-seeder}"
PG_PASSWORD="${TIMESCALEDB_SEED_PASSWORD:-trickster-dev-seed}"

psql_cmd() {
    PGPASSWORD="$PG_PASSWORD" PGTZ=UTC psql --host "$PG_HOST" --username "$PG_USER" \
        --dbname "$PG_DB" --no-psqlrc --quiet -v ON_ERROR_STOP=1 "$@"
}

load_seed_metadata() {
    gzip -t "$FILE1"
    gzip -t "$FILE2"
    if [ ! -s "$SEED_METADATA" ]; then
        echo "seed metadata is missing; run the seed_data_generate service first"
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

create_truncate_table_timescaledb() {
    echo "creating/truncating trips hypertable"
    psql_cmd -f create_truncate_trips_table.sql > /dev/null
}

# One psql session per file: the COPY reads the gunzip stream from stdin and
# HEADER match fails the load if the file's columns ever change order.
load_file_transform_to_timescaledb() {
    echo "loading $1 with shift_seconds=$SHIFT_SECONDS"
    gunzip -c "$1" | psql_cmd -v shift_seconds="$SHIFT_SECONDS" \
        -c "TRUNCATE trips_staging" \
        -c "COPY trips_staging FROM STDIN WITH (FORMAT text, HEADER match)" \
        -f load_trips_from_staging.sql
}

finish_load_timescaledb() {
    psql_cmd -c "DROP TABLE trips_staging" -c "ANALYZE trips"
    echo "materializing the trips_15m continuous aggregate"
    psql_cmd -f create_continuous_aggregate.sql > /dev/null
}

validate_seed() {
    echo "validating seeded data"
    facts=$(psql_cmd -A -t -F ' ' -c "
        SELECT count(*),
               coalesce(extract(epoch FROM min(pickup_datetime))::bigint, 0),
               coalesce(extract(epoch FROM max(pickup_datetime))::bigint, 0),
               coalesce(extract(epoch FROM min(dropoff_datetime))::bigint, 0),
               coalesce(extract(epoch FROM max(dropoff_datetime))::bigint, 0),
               count(*) FILTER (WHERE pickup_date <>
                                (pickup_datetime AT TIME ZONE 'UTC')::date),
               count(*) FILTER (WHERE dropoff_datetime IS NOT NULL AND
                                dropoff_date IS DISTINCT FROM
                                (dropoff_datetime AT TIME ZONE 'UTC')::date),
               count(*) FILTER (WHERE pickup_epoch <>
                                extract(epoch FROM pickup_datetime)::bigint)
        FROM trips;")
    set -- $facts
    rows=$1
    min_pickup=$2
    max_pickup=$3
    min_dropoff=$4
    max_dropoff=$5
    pickup_mismatches=$6
    dropoff_mismatches=$7
    epoch_mismatches=$8
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
    if [ "$epoch_mismatches" -ne 0 ]; then
        echo "seed validation failed: $epoch_mismatches rows whose pickup_epoch disagrees with pickup_datetime"
        exit 1
    fi
    chunks=$(psql_cmd -A -t -c "SELECT coalesce(max(num_chunks), 0)
        FROM timescaledb_information.hypertables
        WHERE hypertable_schema = 'public' AND hypertable_name = 'trips';")
    if [ "$chunks" -le 0 ]; then
        echo "seed validation failed: trips is not a hypertable with chunks"
        exit 1
    fi
    indexes=$(psql_cmd -A -t -c "SELECT count(*)
        FROM pg_indexes
        WHERE schemaname = 'public' AND tablename = 'trips'
          AND indexname IN ('trips_pickup_datetime_idx',
                            'idx_cab_type_pickup_datetime',
                            'idx_pickup_epoch');")
    if [ "$indexes" -ne 3 ]; then
        echo "seed validation failed: expected 3 query indexes, got $indexes"
        exit 1
    fi
    # Every row lands in exactly one 15-minute bucket of the continuous aggregate.
    aggregated=$(psql_cmd -A -t -c "SELECT coalesce(sum(trips), 0)::bigint FROM trips_15m;")
    if [ "$aggregated" -ne "$rows" ]; then
        echo "seed validation failed: trips_15m covers $aggregated of $rows rows"
        exit 1
    fi
    # The read-only roles get SELECT through the seeder's default privileges
    # (init/01-users.sql); a missing grant would only surface later in Grafana.
    readers=$(psql_cmd -A -t -c "SELECT count(*)
        FROM (VALUES ('trickster'), ('grafana_ro')) AS r(name)
        WHERE has_table_privilege(r.name, 'public.trips', 'SELECT')
          AND has_table_privilege(r.name, 'public.trips_15m', 'SELECT');")
    if [ "$readers" -ne 2 ]; then
        echo "seed validation failed: expected 2 read-only roles with SELECT on trips and trips_15m, got $readers"
        exit 1
    fi
    echo "seed complete: $rows rows, pickup window $min_pickup..$max_pickup, chunks=$chunks, indexes=$indexes"
}

load_seed_metadata

create_truncate_table_timescaledb

load_file_transform_to_timescaledb "$FILE1"
load_file_transform_to_timescaledb "$FILE2"
finish_load_timescaledb

validate_seed
