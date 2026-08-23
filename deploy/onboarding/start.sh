#!/bin/sh
set -eu

mkdir -p \
  /etc/openlore \
  /var/lib/openlore/data \
  /var/lib/openlore/published \
  /var/lib/openlore/ssh
cp lore.json /etc/openlore/lore.json

if [ -n "${OPENLORE_ONBOARDING_PUBLIC_KEY:-}" ]; then
  ./out identity key \
    --identity onboarding \
    --key "$OPENLORE_ONBOARDING_PUBLIC_KEY" \
    --auth /etc/openlore/lore.json
fi

set -- ./out --metrics-port 0
if [ -n "${RAILWAY_TCP_PROXY_PORT:-}" ]; then
  set -- "$@" --external-ssh-port "$RAILWAY_TCP_PROXY_PORT"
fi

exec "$@"
