#!/usr/bin/env bash
# Regenerates the shared synthetic trips data and reloads every seeded database
# in the developer environment concurrently, then reports each seeder's result.
#
# Usage: hack/developer-seed-data.sh   (run from anywhere; SEED_PROFILE is honored)
#   Without SEED_TARGET, only the databases that are already running are reseeded.
#   SEED_TARGET scopes the run to specific targets, space- or comma-separated:
#   SEED_TARGET=timescaledb make developer-seed-data
set -euo pipefail

cd "$(dirname "$0")/../docs/developer/environment"

# Each database is in its own compose profile; enabling them all makes every
# service addressable no matter which profiles the environment was started with.
export COMPOSE_PROFILES='*'

# Every trips database service <name> has a one-shot loader service <name>_seed.
# graphite is seeded by its own generator and is handled separately below.
ALL_TARGETS="clickhouse mysql timescaledb greptimedb druid questdb prometheus victoriametrics graphite"
if [[ -z "${SEED_TARGET:-}" ]]; then
  running=" $(docker compose ps --status running --services | tr '\n' ' ') "
  SEED_TARGET=""
  for t in $ALL_TARGETS; do
    case "$running" in *" $t "*) SEED_TARGET+=" $t" ;; esac
  done
  if [[ -z "$SEED_TARGET" ]]; then
    echo "no seeded databases are running; start the environment or set SEED_TARGET (valid: $ALL_TARGETS)" >&2
    exit 1
  fi
fi
read -r -a targets <<< "$(echo "$SEED_TARGET" | tr ',' ' ')"

trips_databases=()
graphite=0
prometheus=0
victoriametrics=0
for t in ${targets[@]+"${targets[@]}"}; do
  case " $ALL_TARGETS " in
    *" $t "*) ;;
    *) echo "unknown SEED_TARGET '$t' (valid: $ALL_TARGETS)" >&2; exit 1 ;;
  esac
  case "$t" in
    graphite) graphite=1 ;;
    prometheus) prometheus=1 ;;
    victoriametrics) victoriametrics=1 ;;
    *) trips_databases+=("$t") ;;
  esac
done
if [[ ${#trips_databases[@]} -eq 0 && $graphite -eq 0 && $prometheus -eq 0 && $victoriametrics -eq 0 ]]; then
  echo "SEED_TARGET is empty (valid: $ALL_TARGETS)" >&2
  exit 1
fi

devorigin_fed=$((prometheus | victoriametrics))

# The streaming sidecar must be stopped while the whisper files are recreated.
seed_graphite() {
  docker compose stop graphite_generator
  docker compose run --rm -e GRAPHITE_SEED_FORCE=1 graphite_seed
  docker compose up -d graphite_generator
}

# devorigin must restart to pick up the new seed shift, so it is stopped around
# both imports. prometheus_seed never replaces a running Prometheus's data, and
# VictoriaMetrics can lose re-imported samples of deleted series, so both
# reseed into emptied storage.
seed_devorigin_fed() {
  local services=(devorigin)
  if [[ $prometheus -eq 1 ]]; then services+=(prometheus); fi
  docker compose stop "${services[@]}"
  if [[ $prometheus -eq 1 ]]; then
    docker compose run --rm --no-deps prometheus_seed_generate
    docker compose run --rm --no-deps prometheus_seed
  fi
  if [[ $victoriametrics -eq 1 ]]; then
    docker compose stop victoriametrics
    docker compose run --rm --no-deps --entrypoint /bin/sh victoriametrics \
      -c 'find /storage -mindepth 1 -maxdepth 1 -exec rm -rf {} +'
    docker compose up -d --wait --no-deps victoriametrics
    docker compose run --rm --no-deps victoriametrics_seed
  fi
  docker compose up -d --no-deps "${services[@]}"
}

# developer-start can return while its one-shot loaders are still running.
# All trips loaders share the fixture, even when only one database is reloaded;
# prometheus_seed_generate reads it too, and ALL_TARGETS yields prometheus_seed.
startup_services=()
if [[ ${#trips_databases[@]} -gt 0 || $devorigin_fed -eq 1 ]]; then
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

# Prometheus and VictoriaMetrics both join devorigin's live trips metrics to their
# history, and reseeding restarts devorigin with a new seed shift, so whichever of
# them is running is reseeded alongside the other to keep both in phase.
if [[ $prometheus -ne $victoriametrics ]]; then
  running=" $(docker compose ps --status running --services | tr '\n' ' ') "
  if [[ $prometheus -eq 1 && "$running" == *" victoriametrics "* ]]; then
    echo "also reseeding victoriametrics, which shares devorigin's trips metrics with prometheus"
    victoriametrics=1
  elif [[ $victoriametrics -eq 1 && "$running" == *" prometheus "* ]]; then
    echo "also reseeding prometheus, which shares devorigin's trips metrics with victoriametrics"
    prometheus=1
  fi
fi

names=()
pids=()
if [[ ${#trips_databases[@]} -gt 0 || $devorigin_fed -eq 1 ]]; then
  if [[ ${#trips_databases[@]} -gt 0 ]]; then
    docker compose up -d --wait "${trips_databases[@]}"
  fi
  docker compose run --rm seed_data_generate
fi
if [[ $devorigin_fed -eq 1 ]]; then
  seed_devorigin_fed &
  label=""
  if [[ $prometheus -eq 1 ]]; then label+=" prometheus_seed"; fi
  if [[ $victoriametrics -eq 1 ]]; then label+=" victoriametrics_seed"; fi
  names+=("${label# }")
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
