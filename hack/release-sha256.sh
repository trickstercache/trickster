#!/bin/bash

CHECKSUM_FILE=$1
BUILD_SUBDIR=$2
TAGVER=$3

set -e

rm -f "${CHECKSUM_FILE}"

# hash from inside the directory so the file lists bare archive names
pushd "${BUILD_SUBDIR}" > /dev/null
shopt -s nullglob
archives=(trickster-"${TAGVER}".*.tar.gz trickster-"${TAGVER}".*.zip)
if [ ${#archives[@]} -eq 0 ]; then
    echo "no release archives for ${TAGVER} in ${BUILD_SUBDIR}" >&2
    exit 1
fi
sha256sum "${archives[@]}" > "$(basename "${CHECKSUM_FILE}")"
popd > /dev/null

cat "${CHECKSUM_FILE}"
