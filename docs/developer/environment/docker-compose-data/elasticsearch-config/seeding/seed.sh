#!/bin/sh

#
# Copyright 2018 The Trickster Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#

set -eu

ES_URL="${ES_URL:-http://elasticsearch:9200}"
ES_INDEX="${ES_INDEX:-trickster-dev-logs}"

wait_for_elasticsearch() {
    echo "waiting for Elasticsearch at ${ES_URL}"
    until curl -fsS "${ES_URL}/_cluster/health?wait_for_status=yellow&timeout=1s" >/dev/null 2>&1; do
        sleep 2
    done
}

iso_utc() {
    date -u -d "@$1" '+%Y-%m-%dT%H:%M:%SZ'
}

create_index() {
    echo "creating ${ES_INDEX}"
    curl -fsS -X DELETE "${ES_URL}/${ES_INDEX}" >/dev/null 2>&1 || true
    curl -fsS -X PUT "${ES_URL}/${ES_INDEX}" \
        -H 'Content-Type: application/json' \
        -d '{
  "mappings": {
    "properties": {
      "@timestamp": { "type": "date" },
      "event_id": { "type": "keyword" },
      "service": { "type": "keyword" },
      "level": { "type": "keyword" },
      "status_code": { "type": "integer" },
      "duration_ms": { "type": "integer" },
      "cacheable": { "type": "boolean" },
      "message": { "type": "text" }
    }
  }
}' >/dev/null
}

append_log() {
    event_id="$1"
    timestamp="$2"
    service="$3"
    level="$4"
    status_code="$5"
    duration_ms="$6"
    cacheable="$7"
    message="$8"

    printf '{"index":{"_index":"%s","_id":"%s"}}\n' "${ES_INDEX}" "${event_id}" >> "${BULK_FILE}"
    printf '{"@timestamp":"%s","event_id":"%s","service":"%s","level":"%s","status_code":%s,"duration_ms":%s,"cacheable":%s,"message":"%s"}\n' \
        "${timestamp}" "${event_id}" "${service}" "${level}" "${status_code}" "${duration_ms}" "${cacheable}" "${message}" >> "${BULK_FILE}"
}

seed_recent_logs() {
    now="$(date -u +%s)"
    i=0
    while [ "${i}" -lt 180 ]; do
        ts="$(iso_utc "$((now - (i * 60)))")"
        case $((i % 4)) in
            0) service="api"; level="info"; status=200; cacheable=true ;;
            1) service="worker"; level="info"; status=202; cacheable=true ;;
            2) service="edge"; level="warn"; status=429; cacheable=false ;;
            *) service="checkout"; level="error"; status=500; cacheable=false ;;
        esac
        duration="$((25 + (i % 45)))"
        append_log "recent-${i}" "${ts}" "${service}" "${level}" "${status}" "${duration}" "${cacheable}" "recent trickster developer log ${i}"
        i=$((i + 1))
    done
}

seed_older_logs() {
    now="$(date -u +%s)"
    i=0
    while [ "${i}" -lt 72 ]; do
        ts="$(iso_utc "$((now - (3 * 86400) - (i * 3600)))")"
        case $((i % 3)) in
            0) service="api"; level="info"; status=200; cacheable=true ;;
            1) service="edge"; level="warn"; status=404; cacheable=false ;;
            *) service="worker"; level="error"; status=503; cacheable=false ;;
        esac
        duration="$((50 + (i % 75)))"
        append_log "older-${i}" "${ts}" "${service}" "${level}" "${status}" "${duration}" "${cacheable}" "older trickster developer log ${i}"
        i=$((i + 1))
    done
}

load_bulk_data() {
    echo "loading generated log data into ${ES_INDEX}"
    curl -fsS -X POST "${ES_URL}/_bulk?refresh=true" \
        -H 'Content-Type: application/x-ndjson' \
        --data-binary "@${BULK_FILE}" >/dev/null
}

BULK_FILE="$(mktemp)"
trap 'rm -f "${BULK_FILE}"' EXIT

wait_for_elasticsearch
create_index
seed_recent_logs
seed_older_logs
load_bulk_data

echo "seeded ${ES_INDEX}"
