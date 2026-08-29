#!/usr/bin/env bash
# Exercises all three InfluxDB 3.x query paths through Trickster:
#   1. HTTP InfluxQL via /api/v3/query_influxql
#   2. HTTP SQL via /api/v3/query_sql
#   3. Flight SQL via gRPC (requires grpcurl)
#
# Usage: ./verify-influxdb3.sh [base_url]
# Default base_url is http://localhost:8480/influx3

set -euo pipefail

BASE="${1:-http://localhost:8480/influx3}"
DIRECT="http://localhost:8181"
TOKEN="trickster-dev-token"
DB="trickster"

echo "== 1. HTTP InfluxQL via Trickster =="
curl -fsS -H "Authorization: Bearer ${TOKEN}" \
  --data-urlencode "q=SELECT mean(\"usage_idle\") FROM \"cpu\" WHERE \"cpu\" = 'cpu-total' AND time > now() - 5m GROUP BY time(10s)" \
  --data-urlencode "db=${DB}" \
  "${BASE}/query" | head -c 500
echo ""

echo "== 2. HTTP SQL via Trickster (/api/v3/query_sql) =="
NOW=$(date +%s)
FIVE_MIN_AGO=$((NOW - 300))
curl -fsS -H "Authorization: Bearer ${TOKEN}" \
  --data-urlencode "q=SELECT date_bin(INTERVAL '10 seconds', time) AS time, avg(usage_idle) FROM cpu WHERE cpu = 'cpu-total' AND time >= ${FIVE_MIN_AGO} AND time < ${NOW} GROUP BY 1 ORDER BY 1" \
  --data-urlencode "db=${DB}" \
  --data-urlencode "format=json" \
  "${BASE}/api/v3/query_sql" | head -c 500
echo ""

echo "== 3. Flight SQL via Trickster (port 8485) =="
if command -v grpcurl >/dev/null 2>&1; then
  grpcurl -plaintext -H "authorization: Bearer ${TOKEN}" \
    -d "{\"cmd\": {\"query\": \"SELECT avg(usage_idle) FROM cpu WHERE cpu = 'cpu-total' LIMIT 5\"}}" \
    localhost:8485 arrow.flight.protocol.FlightService/GetFlightInfo | head -c 500 || true
else
  echo "grpcurl not installed — skipping Flight SQL test"
fi
echo ""

echo "== Cache-hit check (re-run HTTP SQL) =="
curl -fsS -H "Authorization: Bearer ${TOKEN}" \
  --data-urlencode "q=SELECT date_bin(INTERVAL '10 seconds', time) AS time, avg(usage_idle) FROM cpu WHERE cpu = 'cpu-total' AND time >= ${FIVE_MIN_AGO} AND time < ${NOW} GROUP BY 1 ORDER BY 1" \
  --data-urlencode "db=${DB}" \
  --data-urlencode "format=json" \
  -D- \
  "${BASE}/api/v3/query_sql" | grep -i "x-trickster\|status" | head -5
