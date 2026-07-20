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

KIBANA_URL="${KIBANA_URL:-http://kibana:5601}"
KIBANA_VERSION="${KIBANA_VERSION:-8.17.3}"

wait_for_kibana() {
    echo "waiting for Kibana at ${KIBANA_URL}"
    i=0
    until curl -fsS "${KIBANA_URL}/api/status" 2>/dev/null | grep -q '"level":"available"'; do
        i=$((i + 1))
        if [ "${i}" -gt 450 ]; then
            echo "timed out waiting for Kibana" >&2
            return 1
        fi
        sleep 2
    done
}

post_json() {
    path="$1"
    file="$2"
    curl -fsS -X POST "${KIBANA_URL}${path}" \
        -H 'kbn-xsrf: true' \
        -H 'Content-Type: application/json' \
        --data-binary "@${file}" >/dev/null
}

create_kibana_config() {
    cat > "${CONFIG_FILE}" <<JSON
{
  "attributes": {
    "defaultIndex": "trickster-dev-logs",
    "dateFormat:tz": "UTC"
  }
}
JSON
    post_json "/api/saved_objects/config/${KIBANA_VERSION}?overwrite=true" "${CONFIG_FILE}"
}

create_data_view() {
    cat > "${DATA_VIEW_FILE}" <<JSON
{
  "data_view": {
    "id": "trickster-dev-logs",
    "title": "trickster-dev-logs",
    "name": "Trickster Dev Logs",
    "timeFieldName": "@timestamp"
  },
  "override": true
}
JSON
    post_json "/api/data_views/data_view" "${DATA_VIEW_FILE}"
}

create_saved_search() {
    cat > "${SEARCH_FILE}" <<JSON
{
  "attributes": {
    "title": "Trickster Dev Logs",
    "description": "Generated Elasticsearch logs for Trickster developer environment verification.",
    "columns": ["@timestamp", "level", "service", "message", "duration_ms", "cacheable"],
    "sort": [["@timestamp", "desc"]],
    "kibanaSavedObjectMeta": {
      "searchSourceJSON": "{\"query\":{\"query\":\"\",\"language\":\"kuery\"},\"filter\":[],\"indexRefName\":\"kibanaSavedObjectMeta.searchSourceJSON.index\"}"
    }
  },
  "references": [
    {
      "name": "kibanaSavedObjectMeta.searchSourceJSON.index",
      "type": "index-pattern",
      "id": "trickster-dev-logs"
    }
  ]
}
JSON
    post_json "/api/saved_objects/search/trickster-dev-logs-search?overwrite=true" "${SEARCH_FILE}"
}

create_log_volume_visualization() {
    cat > "${VISUALIZATION_FILE}" <<JSON
{
  "attributes": {
    "title": "Trickster Dev Log Volume",
    "description": "Minute-by-minute log volume used to exercise Elasticsearch date histogram caching through Trickster.",
    "visState": "{\"title\":\"Trickster Dev Log Volume\",\"type\":\"histogram\",\"params\":{\"shareYAxis\":true,\"addTooltip\":true,\"addLegend\":false,\"legendPosition\":\"right\",\"scale\":\"linear\",\"mode\":\"stacked\",\"times\":[],\"addTimeMarker\":false,\"defaultYExtents\":false,\"setYExtents\":false,\"yAxis\":{}},\"aggs\":[{\"id\":\"1\",\"enabled\":true,\"type\":\"count\",\"schema\":\"metric\",\"params\":{}},{\"id\":\"2\",\"enabled\":true,\"type\":\"date_histogram\",\"schema\":\"segment\",\"params\":{\"field\":\"@timestamp\",\"interval\":\"m\",\"useNormalizedEsInterval\":true,\"scaleMetricValues\":false,\"drop_partials\":false,\"min_doc_count\":0,\"extended_bounds\":{},\"extendToTimeRange\":true}}],\"listeners\":{}}",
    "uiStateJSON": "{}",
    "version": 1,
    "kibanaSavedObjectMeta": {
      "searchSourceJSON": "{\"query\":{\"query\":\"\",\"language\":\"kuery\"},\"filter\":[],\"indexRefName\":\"kibanaSavedObjectMeta.searchSourceJSON.index\"}"
    }
  },
  "references": [
    {
      "name": "kibanaSavedObjectMeta.searchSourceJSON.index",
      "type": "index-pattern",
      "id": "trickster-dev-logs"
    }
  ]
}
JSON
    post_json "/api/saved_objects/visualization/trickster-dev-log-volume?overwrite=true" "${VISUALIZATION_FILE}"
}

create_dashboard() {
    cat > "${DASHBOARD_FILE}" <<JSON
{
  "attributes": {
    "title": "Trickster Dev Logs",
    "description": "Out-of-box dashboard for the generated Elasticsearch log data served through Trickster.",
    "hits": 0,
    "optionsJSON": "{\"useMargins\":true,\"syncColors\":false,\"syncCursor\":true,\"syncTooltips\":false,\"hidePanelTitles\":false}",
    "panelsJSON": "[{\"version\":\"8.17.3\",\"type\":\"visualization\",\"gridData\":{\"x\":0,\"y\":0,\"w\":48,\"h\":12,\"i\":\"1\"},\"panelIndex\":\"1\",\"embeddableConfig\":{},\"panelRefName\":\"panel_1\"},{\"version\":\"8.17.3\",\"type\":\"search\",\"gridData\":{\"x\":0,\"y\":12,\"w\":48,\"h\":18,\"i\":\"2\"},\"panelIndex\":\"2\",\"embeddableConfig\":{},\"panelRefName\":\"panel_2\"}]",
    "timeRestore": true,
    "timeFrom": "now-120m/m",
    "timeTo": "now/m-1ms",
    "refreshInterval": {
      "pause": true,
      "value": 0
    },
    "kibanaSavedObjectMeta": {
      "searchSourceJSON": "{\"query\":{\"query\":\"\",\"language\":\"kuery\"},\"filter\":[]}"
    }
  },
  "references": [
    {
      "name": "panel_1",
      "type": "visualization",
      "id": "trickster-dev-log-volume"
    },
    {
      "name": "panel_2",
      "type": "search",
      "id": "trickster-dev-logs-search"
    }
  ]
}
JSON
    post_json "/api/saved_objects/dashboard/trickster-dev-logs-dashboard?overwrite=true" "${DASHBOARD_FILE}"
}

CONFIG_FILE="$(mktemp)"
DATA_VIEW_FILE="$(mktemp)"
SEARCH_FILE="$(mktemp)"
VISUALIZATION_FILE="$(mktemp)"
DASHBOARD_FILE="$(mktemp)"
trap 'rm -f "${CONFIG_FILE}" "${DATA_VIEW_FILE}" "${SEARCH_FILE}" "${VISUALIZATION_FILE}" "${DASHBOARD_FILE}"' EXIT

wait_for_kibana
create_kibana_config
create_data_view
create_saved_search
create_log_volume_visualization
create_dashboard

echo "seeded Kibana data view, visualization, and dashboard for trickster-dev-logs"
