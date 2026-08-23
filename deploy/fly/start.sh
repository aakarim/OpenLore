#!/bin/sh
set -eu

mkdir -p /data/config /data/control /data/published /data/ssh
cp lore.json /data/config/lore.json

exec ./out --metrics-port 0
