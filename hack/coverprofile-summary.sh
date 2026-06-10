#!/usr/bin/env bash
#
# Copyright 2018 The Trickster Authors
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

# coverprofile-summary.sh prints covered statements, total statements, and
# coverage percent from a Go coverage profile.

set -euo pipefail

PROFILE="${1:-.coverprofile}"

if [ ! -f "${PROFILE}" ]; then
  echo "coverprofile ${PROFILE} does not exist, please check path"
  exit 1
fi

awk '
  NR == 1 {
    if ($0 !~ /^mode: /) {
      printf "invalid coverprofile: missing mode line\n" > "/dev/stderr"
      exit 1
    }
    next
  }
  {
    statements = $2
    count = $3
    total += statements
    if (count > 0) {
      covered += statements
    }
  }
  END {
    if (total == 0) {
      printf "covered statements: 0\n"
      printf "total statements: 0\n"
      printf "coverage percent: 0.00%%\n"
      exit
    }
    printf "covered statements: %d\n", covered
    printf "total statements: %d\n", total
    printf "coverage percent: %.2f%%\n", (covered / total) * 100
  }
' "${PROFILE}"
