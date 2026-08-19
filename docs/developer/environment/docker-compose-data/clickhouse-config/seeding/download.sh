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

# download.sh
#
# Populates the shared NYC taxi seed data cache used by both the ClickHouse
# and MySQL seeders. Files are downloaded to a temp name and moved into place
# atomically, and verified with gzip, so seeders that start afterward (in any
# order, including in parallel) always observe complete, valid files.
#
# The clickhouse_seed and mysql_seed services depend on this service
# completing successfully before they start.

set -e
cd /seeding
mkdir -p data

fetch() {
    if gzip -t "$1" 2>/dev/null; then
        echo "$1 already cached and valid. Skipping download."
        return
    fi
    echo "downloading $2 -> $1"
    # the temp name is unique per host+pid so even two concurrently running
    # fetchers cannot corrupt each other; the loser of the final mv race
    # simply overwrites with an identical verified file
    tmp="$1.download.$(hostname).$$.tmp"
    rm -f "$1"
    wget -q -O "$tmp" "$2"
    gzip -t "$tmp"
    mv "$tmp" "$1"
    echo "$1 downloaded and verified."
}

fetch data/trips_1.gz "https://datasets-documentation.s3.eu-west-3.amazonaws.com/nyc-taxi/trips_1.gz"
fetch data/trips_2.gz "https://datasets-documentation.s3.eu-west-3.amazonaws.com/nyc-taxi/trips_2.gz"

# Derive one shift from the actual source bounds. This file is regenerated on
# every seed workflow even when the downloads are already cached, so the two
# database seeders use exactly the same seed instant and offset.
echo "scanning source timestamp bounds"
bounds=$(gunzip -c data/trips_1.gz data/trips_2.gz | awk -F '\t' '
    $1 != "trip_id" {
        rows++
        if (pickup_min == "" || $4 < pickup_min) pickup_min = $4
        if ($4 > pickup_max) pickup_max = $4
        if (dropoff_min == "" || $6 < dropoff_min) dropoff_min = $6
        if ($6 > dropoff_max) dropoff_max = $6
    }
    END {
        if (rows == 0 || pickup_min == "" || pickup_max == "") exit 1
        printf "%d|%s|%s|%s|%s\n", rows, pickup_min, pickup_max, dropoff_min, dropoff_max
    }')
old_ifs=$IFS
IFS='|'
set -- $bounds
IFS=$old_ifs

source_rows=$1
source_pickup_min=$2
source_pickup_max=$3
source_dropoff_min=$4
source_dropoff_max=$5
source_pickup_min_epoch=$(date -u -d "$source_pickup_min UTC" +%s)
source_pickup_max_epoch=$(date -u -d "$source_pickup_max UTC" +%s)
source_dropoff_min_epoch=$(date -u -d "$source_dropoff_min UTC" +%s)
source_dropoff_max_epoch=$(date -u -d "$source_dropoff_max UTC" +%s)
source_midpoint_epoch=$((source_pickup_min_epoch + (source_pickup_max_epoch - source_pickup_min_epoch) / 2))
seed_epoch=$(date -u +%s)
shift_seconds=$((seed_epoch - source_midpoint_epoch))

metadata="data/seed-window.env"
metadata_tmp="$metadata.$(hostname).$$.tmp"
{
    printf 'SOURCE_ROWS=%s\n' "$source_rows"
    printf 'SOURCE_PICKUP_MIN_EPOCH=%s\n' "$source_pickup_min_epoch"
    printf 'SOURCE_PICKUP_MAX_EPOCH=%s\n' "$source_pickup_max_epoch"
    printf 'SOURCE_DROPOFF_MIN_EPOCH=%s\n' "$source_dropoff_min_epoch"
    printf 'SOURCE_DROPOFF_MAX_EPOCH=%s\n' "$source_dropoff_max_epoch"
    printf 'SEED_EPOCH=%s\n' "$seed_epoch"
    printf 'SHIFT_SECONDS=%s\n' "$shift_seconds"
} > "$metadata_tmp"
mv "$metadata_tmp" "$metadata"
echo "seed shift ready: rows=$source_rows shift_seconds=$shift_seconds seed_epoch=$seed_epoch"
