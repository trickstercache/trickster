#!/bin/sh

sleep 10 > /dev/null 2>&1 # allows time for influxdb to be ready

export IFLX_APP=trickster
export IFLX_ORG=${IFLX_APP}-demo
export IFLX_TOK=${IFLX_ORG}-token

export IFLX_URL=http://influxdb:8086
export IFLX_RET='30d'

# this setups influxdb with default users, buckets, orgs, etc.

influx setup -f -r $IFLX_RET -b $IFLX_APP -u $IFLX_APP -p $IFLX_APP \
  -o $IFLX_ORG -t $IFLX_TOK --host=$IFLX_URL

# expected output:

# Config default has been stored in /etc/influxdb2/influx-configs.
# User		Organization	Bucket
# trickster	trickster-demo	trickster

# this captures the bucket id for the new bucket, and prints it to the log:

export IFLX_BID=$(influx bucket list -o $IFLX_ORG -t $IFLX_TOK \
  --host=$IFLX_URL | grep $IFLX_APP  | awk '{print $1}')
# $IFLX_BID should be a 16-char hex, like 60a439a7d894da68, for the bucket id

echo
echo "BUCKET ID IS [${IFLX_BID}]"

# expected output (your hex value will be different):
# BUCKET ID IS [60a439a7d894da68]

# this maps a 1.8-style db to the $IFLX_APP bucket, w/ same retention policy

echo

influx v1 dbrp create --db $IFLX_APP --rp $IFLX_RET --bucket-id $IFLX_BID \
  --default \
  -o $IFLX_ORG -t $IFLX_TOK --host=$IFLX_URL

# expected output (your hex values will be different):
# ID                 Database    Bucket ID          Retention Policy   Default   Organization ID
# 0737474b07adc000   trickster   e9c7df23e13b2129   30d                true      641f5409d74d40ac
