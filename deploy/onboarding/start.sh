#!/bin/sh
set -eu

mkdir -p \
  /var/lib/openlore/config \
  /var/lib/openlore/data \
  /var/lib/openlore/published \
  /var/lib/openlore/ssh

if [ ! -e /var/lib/openlore/config/openlore.yml ]; then
  cp openlore.yml /var/lib/openlore/config/openlore.yml
fi

if [ ! -e /var/lib/openlore/config/lore.json ]; then
  cp lore.json /var/lib/openlore/config/lore.json
  chmod 600 /var/lib/openlore/config/lore.json
  if [ -n "${OPENLORE_ONBOARDING_PUBLIC_KEY:-}" ]; then
    ./out identity key \
      --identity onboarding \
      --key "$OPENLORE_ONBOARDING_PUBLIC_KEY" \
      --auth /var/lib/openlore/config/lore.json
  fi
fi

set -- ./out --config /var/lib/openlore/config/openlore.yml --metrics-port 0
if [ -n "${RAILWAY_TCP_PROXY_PORT:-}" ]; then
  set -- "$@" --external-ssh-port "$RAILWAY_TCP_PROXY_PORT"
fi

exec "$@"
