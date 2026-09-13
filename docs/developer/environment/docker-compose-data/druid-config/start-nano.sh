#!/bin/bash

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

# Apache Druid's published image is distroless and does not include the Perl
# interpreter used by the upstream start-nano-quickstart supervisor.  The
# service scripts and the Java runtime are present, so start the same
# single-node processes with a small Bash supervisor instead.

set -Eeuo pipefail

cd /opt/druid

pids=()

cleanup() {
    trap - TERM INT EXIT
    for pid in "${pids[@]}"; do
        kill "$pid" 2>/dev/null || true
    done
    for pid in "${pids[@]}"; do
        wait "$pid" 2>/dev/null || true
    done
}

trap cleanup TERM INT EXIT

run_service() {
    "$@" &
    pids+=("$!")
}

# Keep the process set and configuration identical to Druid's bundled
# nano-quickstart launcher.  All services share this container's loopback
# network and the mounted /opt/druid/var and /opt/shared directories.
run_service ./bin/run-zk conf
sleep 2
run_service ./bin/run-druid coordinator-overlord conf/druid/single-server/nano-quickstart
run_service ./bin/run-druid broker conf/druid/single-server/nano-quickstart
run_service ./bin/run-druid router conf/druid/single-server/nano-quickstart
run_service ./bin/run-druid historical conf/druid/single-server/nano-quickstart
run_service ./bin/run-druid middleManager conf/druid/single-server/nano-quickstart

while :; do
    for pid in "${pids[@]}"; do
        if ! kill -0 "$pid" 2>/dev/null; then
            if wait "$pid"; then
                exit 0
            else
                rc=$?
                exit "$rc"
            fi
        fi
    done
    sleep 2
done
