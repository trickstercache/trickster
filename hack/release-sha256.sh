#!/bin/bash

CHECKSUM_FILE=$1
BUILD_SUBDIR=$2
TAGVER=$3
BIN_DIR=$4

rm ${CHECKSUM_FILE} > /dev/null 2> /dev/null && touch ${CHECKSUM_FILE}

pushd $BUILD_SUBDIR > /dev/null
sha256sum trickster-${TAGVER}.tar.gz > $(basename $CHECKSUM_FILE)
popd > /dev/null

RSF="$(realpath ${CHECKSUM_FILE})" && for file in ${BIN_DIR}/*; do
    if [[ "$(basename $file)" == "sha256sum.txt" ]]; then
        continue
    fi
    pushd $(dirname $file) > /dev/null
    sha256sum "$(basename $file)" >> ${RSF}
    popd > /dev/null
done

cat $CHECKSUM_FILE

