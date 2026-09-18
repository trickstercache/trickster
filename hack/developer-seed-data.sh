#!/usr/bin/env bash
# Regenerates the shared synthetic trips data and reloads every seeded database
# in the developer environment concurrently, then reports each seeder's result.
#
# Usage: hack/developer-seed-data.sh   (run from anywhere; SEED_PROFILE is honored)
#   SEED_TARGET scopes the run to specific targets, space- or comma-separated:
#   SEED_TARGET=timescaledb make developer-seed-data
set -euo pipefail

cd "$(dirname "$0")/../docs/developer/environment"

# Every trips database service <name> has a one-shot loader service <name>_seed.
# graphite is seeded by its own generator and is handled separately below.
ALL_TARGETS="clickhouse mysql timescaledb druid graphite"
read -r -a targets <<< "$(echo "${SEED_TARGET:-$ALL_TARGETS}" | tr ',' ' ')"

trips_databases=()
graphite=0
for t in ${targets[@]+"${targets[@]}"}; do
  case " $ALL_TARGETS " in
    *" $t "*) ;;
    *) echo "unknown SEED_TARGET '$t' (valid: $ALL_TARGETS)" >&2; exit 1 ;;
  esac
  if [[ "$t" == graphite ]]; then graphite=1; else trips_databases+=("$t"); fi
done
if [[ ${#trips_databases[@]} -eq 0 && $graphite -eq 0 ]]; then
  echo "SEED_TARGET is empty (valid: $ALL_TARGETS)" >&2
  exit 1
fi

# The streaming sidecar must be stopped while the whisper files are recreated.
seed_graphite() {
  docker compose stop graphite_generator
  docker compose run --rm -e GRAPHITE_SEED_FORCE=1 graphite_seed
  docker compose up -d graphite_generator
}

names=()
pids=()
if [[ ${#trips_databases[@]} -gt 0 ]]; then
  docker compose up -d --wait "${trips_databases[@]}"
  docker compose run --rm seed_data_generate
  for db in "${trips_databases[@]}"; do
    docker compose run --rm --no-deps "${db}_seed" &
    names+=("${db}_seed")
    pids+=($!)
  done
fi
if [[ $graphite -eq 1 ]]; then
  seed_graphite &
  names+=(graphite_seed)
  pids+=($!)
fi

rc=0
for i in "${!pids[@]}"; do
  if wait "${pids[$i]}"; then
    echo "seeder ${names[$i]}: ok"
  else
    echo "seeder ${names[$i]}: FAILED" >&2
    rc=1
  fi
done
exit $rc
