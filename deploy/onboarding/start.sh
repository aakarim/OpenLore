#!/bin/sh
set -eu

mkdir -p \
  /etc/openlore \
  /var/lib/openlore/data \
  /var/lib/openlore/published \
  /var/lib/openlore/ssh
cp lore.json /etc/openlore/lore.json

exec ./out --metrics-port 0
