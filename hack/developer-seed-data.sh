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
ALL_TARGETS="clickhouse mysql timescaledb greptimedb druid prometheus graphite"
read -r -a targets <<< "$(echo "${SEED_TARGET:-$ALL_TARGETS}" | tr ',' ' ')"

trips_databases=()
graphite=0
prometheus=0
for t in ${targets[@]+"${targets[@]}"}; do
  case " $ALL_TARGETS " in
    *" $t "*) ;;
    *) echo "unknown SEED_TARGET '$t' (valid: $ALL_TARGETS)" >&2; exit 1 ;;
  esac
  case "$t" in
    graphite) graphite=1 ;;
    prometheus) prometheus=1 ;;
    *) trips_databases+=("$t") ;;
  esac
done
if [[ ${#trips_databases[@]} -eq 0 && $graphite -eq 0 && $prometheus -eq 0 ]]; then
  echo "SEED_TARGET is empty (valid: $ALL_TARGETS)" >&2
  exit 1
fi

# The streaming sidecar must be stopped while the whisper files are recreated.
seed_graphite() {
  docker compose stop graphite_generator
  docker compose run --rm -e GRAPHITE_SEED_FORCE=1 graphite_seed
  docker compose up -d graphite_generator
}

# prometheus_seed never replaces a running Prometheus's data, and devorigin must
# restart to pick up the new seed shift, so both are stopped around the import.
seed_prometheus() {
  docker compose stop prometheus devorigin
  docker compose run --rm --no-deps prometheus_seed_generate
  docker compose run --rm --no-deps prometheus_seed
  docker compose up -d --no-deps prometheus devorigin
}

# developer-start can return while its one-shot loaders are still running.
# All trips loaders share the fixture, even when only one database is reloaded;
# prometheus_seed_generate reads it too, and ALL_TARGETS yields prometheus_seed.
startup_services=()
if [[ ${#trips_databases[@]} -gt 0 || $prometheus -eq 1 ]]; then
  startup_services+=(seed_data_generate prometheus_seed_generate)
  for db in $ALL_TARGETS; do
    if [[ "$db" != graphite ]]; then startup_services+=("${db}_seed"); fi
  done
fi
if [[ $graphite -eq 1 ]]; then startup_services+=(graphite_seed); fi
startup_ids=$(docker compose ps -q --status running "${startup_services[@]}")
for id in $startup_ids; do
  echo "waiting for startup seeder $id"
  status=$(docker wait "$id")
  if [[ "$status" != 0 ]]; then
    echo "startup seeder $id: FAILED (exit $status)" >&2
    exit 1
  fi
done

names=()
pids=()
if [[ ${#trips_databases[@]} -gt 0 || $prometheus -eq 1 ]]; then
  if [[ ${#trips_databases[@]} -gt 0 ]]; then
    docker compose up -d --wait "${trips_databases[@]}"
  fi
  docker compose run --rm seed_data_generate
fi
if [[ $prometheus -eq 1 ]]; then
  seed_prometheus &
  names+=(prometheus_seed)
  pids+=($!)
fi
if [[ ${#trips_databases[@]} -gt 0 ]]; then
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
