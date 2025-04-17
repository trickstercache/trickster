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
# This downloads 2 large files of NYC taxi data from the ClickHouse S3 Bucket,
# and loads them in to the local ClickHouse database. During download, the files
# are transformed so that the dates in the file, which range from July 2015 to
# October 2015, are adjusted to fall with days before and after the current date
# at the time of seeding. This ensures that relevant data is available to show
# on the dashboard and can take advantage of Trickster's caching protocols that
# favor very recent data (not >10 years old).

cd seeding

if date -v1d >/dev/null 2>&1; then
    # MacOS
    MONTH_LAST=$(date -v-1m +%Y-%m)
    MONTH_CURR=$(date +%Y-%m)
    MONTH_NEXT=$(date -v+1m +%Y-%m)
    MONTH_2OUT=$(date -v+2m +%Y-%m)
else
    # Linux
    MONTH_LAST=$(date -d "$(date +%Y-%m-01) -1 month" +%Y-%m)
    MONTH_CURR=$(date +%Y-%m)
    MONTH_NEXT=$(date -d "$(date +%Y-%m-01) +1 month" +%Y-%m)
    MONTH_2OUT=$(date -d "$(date +%Y-%m-01) +2 month" +%Y-%m)
fi

FILE1="data/trips_1.gz"
FILE2="data/trips_2.gz"
URL1="https://datasets-documentation.s3.eu-west-3.amazonaws.com/nyc-taxi/trips_1.gz"
URL2="https://datasets-documentation.s3.eu-west-3.amazonaws.com/nyc-taxi/trips_2.gz"

AGE_THRESHOLD=$((60 * 60 * 24 * 7))
LC_CTYPE=C # allows sed to play nice with TSV files that have some binary data

is_old_file() {
  local file="$1"
  if [ -f "$file" ]; then
    local mtime
    mtime=$(stat -c %Y "$file")
    local now
    now=$(date +%s)
    local age=$((now - mtime))
    [ "$age" -gt "$AGE_THRESHOLD" ]
  else
    return 1
  fi
}

# pass the RESEED=1 env to wipe any cached seed data, that may have old dates.
# if the seed data is older than 7 days, it will auto-wipe here as well.
if [ "$RESEED" = "1" ] || is_old_file "$FILE1" || is_old_file "$FILE2"; then
  echo "Deleting seed data cache."
  rm -f "$FILE1"
  rm -f "$FILE2"
fi

download_transform() {
    if [ ! -f "$1" ]; then
        echo "$1 not found. Downloading from $2..."
        wget -qO - "$2" | gunzip -c | \
        sed -e "s/2015-07-/${MONTH_LAST}-/g" | \
        sed -e "s/2015-08-/${MONTH_CURR}-/g" | \
        sed -e "s/2015-09-/${MONTH_NEXT}-/g" | \
        sed -e "s/2015-10-/${MONTH_2OUT}-/g" | \
        gzip > "$1"
    else
        echo "$1 already exists. Skipping download."
    fi
}

create_truncate_table_clickhouse() {
    echo "truncating trips table"
    clickhouse-client --host "${CH_SERVER_ADDR:-clickhouse}" --port 9000 \
        --user default < create_truncate_trips_table.sql
}

load_file_to_clickhouse() {
    echo "loading $1"
    gunzip -c "$1" | clickhouse-client --host "${CH_SERVER_ADDR:-clickhouse}" \
        --port 9000 --user default \
        --query="INSERT INTO trips FORMAT TabSeparatedWithNames"
}

mkdir -p data
download_transform "$FILE1" "$URL1"
download_transform "$FILE2" "$URL2"

create_truncate_table_clickhouse

load_file_to_clickhouse "$FILE1"
load_file_to_clickhouse "$FILE2"
