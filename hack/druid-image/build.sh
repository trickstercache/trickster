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

# Builds the multi-arch developer-environment Druid image from this directory's
# Dockerfile, tagged with its DRUID_VERSION.
#
# usage: hack/druid-image/build.sh [--push]
#
# Without --push, both platforms are built and loaded into the local image
# store (requires Docker's containerd image store). With --push, they are
# pushed to Docker Hub as one manifest list (requires docker login).

set -Eeuo pipefail

IMAGE_REPO=${IMAGE_REPO:-trickstercache/druid}
PLATFORMS=${PLATFORMS:-linux/amd64,linux/arm64}

dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

version=$(sed -n 's/^ARG DRUID_VERSION=//p' "${dir}/Dockerfile")
if [ -z "${version}" ]; then
    echo "DRUID_VERSION not found in ${dir}/Dockerfile" >&2
    exit 1
fi

output=--load
case "${1:-}" in
    --push) output=--push ;;
    "") ;;
    *)
        echo "usage: $0 [--push]" >&2
        exit 2
        ;;
esac

echo "building ${IMAGE_REPO}:${version} for ${PLATFORMS} (${output#--})"
docker buildx build \
    --platform "${PLATFORMS}" \
    --tag "${IMAGE_REPO}:${version}" \
    "${output}" \
    "${dir}"
