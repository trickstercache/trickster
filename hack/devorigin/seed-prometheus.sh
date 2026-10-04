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

# seed-prometheus.sh
#
# This imports the trips history that devorigin's backfill wrote as
# OpenMetrics into an emptied Prometheus data directory. Each run of
# seed_data_generate shifts the trips data to a new seed instant, so, like the
# SQL seeders, every run replaces what Prometheus had. It never touches a
# running Prometheus, which must be stopped first to reseed.

set -e

OM_FILE="${OM_FILE:-/om/trips.om}"
DATA_DIR="${DATA_DIR:-/prometheus}"
PROMETHEUS_URL="${PROMETHEUS_URL:-http://prometheus:9090}"
# must match the trips scrape target in prometheus.yml so the backfilled and
# scraped samples land in the same series
INSTANCE="${INSTANCE:-devorigin:8482}"

if [ ! -s "$OM_FILE" ]; then
    echo "missing $OM_FILE; prometheus_seed_generate must run first" >&2
    exit 1
fi

if wget -q -T 2 -O /dev/null "$PROMETHEUS_URL/-/healthy" 2>/dev/null; then
    echo "prometheus is running; keeping its data (stop the environment to reseed)"
    rm -f "$OM_FILE"
    exit 0
fi

echo "wiping $DATA_DIR"
find "$DATA_DIR" -mindepth 1 -maxdepth 1 -exec rm -rf {} +

# the default 2h blocks would make promtool re-read the whole file per block
promtool tsdb create-blocks-from openmetrics --max-block-duration=1000h \
    --label=job=trips --label="instance=$INSTANCE" "$OM_FILE" "$DATA_DIR"

# this runs as root so it can remove the backfill file; Prometheus runs as nobody
chown -R 65534:65534 "$DATA_DIR"
rm -f "$OM_FILE"
echo "prometheus seed complete"
