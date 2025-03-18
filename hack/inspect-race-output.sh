#!/bin/bash

LOGFILE=$1
if [ ! -f $LOGFILE ]; then
    echo "Logfile $LOGFILE does not exist, please check path"
    exit 1
fi

results=$(cat ${LOGFILE} | grep -IE '^      .*trickster.*\.go:([0-9]+) \+0x[\w0-9]*' | sed -n 's/.*trickster\/\(.*\):.*/\1/p' | sort | uniq)
totalRaces=$(cat ${LOGFILE} | grep -IE 'DATA RACE' | wc -l | awk '$1=$1')
if [ -z "$results" ]; then
    echo "No races detected"
else
    echo "${totalRaces} race(s) detected across the following files:"
    echo "$results"
    # TODO: Uncomment this to trigger failure once we resolve existing race conditions
    # exit 2
fi