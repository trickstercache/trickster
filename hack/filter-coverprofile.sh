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

# filter-coverprofile.sh removes entries from a Go coverage profile that
# should not count toward coverage metrics (generated code, entrypoints,
# and example code).

set -euo pipefail

PROFILE="${1:?usage: $0 <coverprofile>}"

# nothing to do if the profile wasn't generated (e.g., coverage disabled)
[ -f "${PROFILE}" ] || exit 0

EXCLUDE_PATTERNS=(
  '_gen\.go:'
  '^github\.com/trickstercache/trickster/v2/cmd/'
  '^github\.com/trickstercache/trickster/v2/examples/'
  '^github\.com/trickstercache/trickster/v2/pkg/testutil/'
)

pattern="$(IFS='|'; echo "${EXCLUDE_PATTERNS[*]}")"

tmp="$(mktemp)"
grep -Ev "${pattern}" "${PROFILE}" > "${tmp}" || true
mv "${tmp}" "${PROFILE}"
