#!/bin/bash
# Render start command for OpenLore.
#
# On the first boot of a new persistent disk, write a starter openlore.yml and
# lore.json for the service's public URL. Existing files are never changed, so
# edits made on the disk survive restarts, redeploys, and image upgrades.
set -euo pipefail

root=/var/lib/openlore
config="$root/config/openlore.yml"
policy="$root/config/lore.json"

origin="${OPENLORE_PUBLIC_URL:-${RENDER_EXTERNAL_URL:-}}"
origin="${origin%/}"
if [ -z "$origin" ]; then
  echo "render-start: set OPENLORE_PUBLIC_URL (RENDER_EXTERNAL_URL is unset)" >&2
  exit 1
fi
host="${origin#*://}"
host="${host%%/*}"
host="${host%%:*}"

mkdir -p "$root/config" "$root/data" "$root/published" "$root/ssh"
chmod 700 "$root/config" "$root/data" "$root/ssh"

if [ ! -e "$config" ]; then
  echo "render-start: writing starter $config for $origin"
  cat >"$config" <<EOF
version: "1"

port: 2222
http_port: ${PORT:-8080}
metrics_port: 0
default_cwd: /

host_key_path: $root/ssh/openlore_ed25519
auth_file: $policy
data_dir: $root/data
writable_dir: $root/published
readonly: false

tokens:
  issuer: $origin
  audience: $origin
  access_ttl: 1h
  refresh_ttl: 720h

mcp:
  enabled: true
  path: /mcp
  require_auth: true

api:
  enabled: true
  path: /api

passkeys:
  enabled: true
  rp_id: $host
  rp_name: OpenLore
  rp_origins: ["$origin"]
  lore_path: /lore
  passkeys_file: $root/data/passkeys.json
  session_ttl: 24h
EOF
fi

if [ ! -e "$policy" ]; then
  echo "render-start: writing starter $policy"
  cat >"$policy" <<'EOF'
{
  "allow_keyless": false,
  "unknown_identity": "deny",
  "roles": {
    "administrator": {
      "allow": { "capabilities": ["lore:config:edit"] }
    },
    "user": {}
  },
  "docsets": {
    "admin-home": {
      "paths": ["/user/admin"]
    },
    "general": {
      "paths": ["/channel/general"],
      "access": { "allow": { "user": "rw" } }
    }
  },
  "identities": [
    {
      "name": "admin",
      "roles": ["administrator", "user"],
      "home": "admin-home"
    }
  ]
}
EOF
  mkdir -p "$root/published/user/admin" "$root/published/channel/general"
fi

exec ./out --config "$config"
