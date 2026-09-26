#!/bin/sh
#
# Copyright 2026 The Trickster Authors
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Read-only by default; native and PromQL checks own isolated fixture data.
set -eu

case "${1:-}" in
  ""|--promql|--mysql) ;;
  *) printf 'Usage: %s [--promql|--mysql]\n' "$0" >&2; exit 2 ;;
esac

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root/integration"
reports=${GREPTIMEDB_REPORT_ROOT:-"$PWD/greptimedb/reports"}
mkdir -p "$reports"
reports=$(CDPATH= cd -- "$reports" && pwd)
GREPTIMEDB_REPORT_DIR=$(mktemp -d "$reports/run-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")
export GREPTIMEDB_REPORT_DIR
if [ "${1:-}" = --mysql ]; then
  export TRICKSTER_GREPTIMEDB_MYSQL_ACCEPTANCE=1
  status=0
  "${GO:-go}" test -json -count=1 -timeout 10m -run '^TestGreptimeMySQL' . > "$GREPTIMEDB_REPORT_DIR/go-test.jsonl" 2>&1 || status=$?
  printf 'MySQL test exit code: %s\nEvidence: %s\n' "$status" "$GREPTIMEDB_REPORT_DIR"
  exit "$status"
fi
if [ "${1:-}" = --promql ]; then
  export TRICKSTER_GREPTIMEDB_PROMQL_ACCEPTANCE=1
  status=0
  "${GO:-go}" test -json -count=1 -timeout 10m -run '^TestGreptimeDBPrometheus$' . > "$GREPTIMEDB_REPORT_DIR/go-test.jsonl" 2>&1 || status=$?
  printf 'PromQL test exit code: %s\nEvidence: %s\n' "$status" "$GREPTIMEDB_REPORT_DIR"
  exit "$status"
fi
export TRICKSTER_GREPTIMEDB_ACCEPTANCE=1

printf 'Report directory: %s\n' "$GREPTIMEDB_REPORT_DIR"
status=0
"${GO:-go}" test -json -count=1 -timeout 10m ./greptimedb > "$GREPTIMEDB_REPORT_DIR/go-test.jsonl" 2>&1 || status=$?
printf 'Test exit code: %s\n' "$status"
printf 'Report: %s/report.json\nLog: %s/go-test.jsonl\n' "$GREPTIMEDB_REPORT_DIR" "$GREPTIMEDB_REPORT_DIR"
if [ "${TRICKSTER_GREPTIMEDB_PROXY_ACCEPTANCE:-0}" = 1 ]; then
  printf 'Proxy report: %s/proxy-report.json\n' "$GREPTIMEDB_REPORT_DIR"
fi
if [ "${TRICKSTER_GREPTIMEDB_HTTP_ACCEPTANCE:-0}" = 1 ]; then
  printf 'HTTP SQL report: %s/http-sql-report.json\n' "$GREPTIMEDB_REPORT_DIR"
fi
exit "$status"
