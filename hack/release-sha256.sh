#!/bin/bash

CHECKSUM_FILE=$1
BUILD_SUBDIR=$2
TAGVER=$3

set -e

rm -f "${CHECKSUM_FILE}"

# hash from inside the directory so the file lists bare archive names
pushd "${BUILD_SUBDIR}" > /dev/null
sha256sum trickster-"${TAGVER}".*.tar.gz > "$(basename "${CHECKSUM_FILE}")"
popd > /dev/null

cat "${CHECKSUM_FILE}"
