#!/bin/bash
set -euo pipefail

# One-time provisioning script for a fresh OpenLore server.
# Requires a sudo-capable admin account for the initial setup only; the
# service itself runs as the unprivileged "openlore" user. Routine deploys
# are handled by .github/workflows/deploy-openlore-sh.yml (deploys as openlore).
#
# Usage (replace <admin> and <host>):
#   scp openlore-linux <admin>@<host>:/tmp/openlore
#   scp -r skills <admin>@<host>:/tmp/openlore-skills
#   scp lore.json <admin>@<host>:/tmp/openlore-lore.json
#   ssh <admin>@<host> 'sudo bash -s' < setup.sh

BINARY="${1:-/tmp/openlore}"
SKILLS_SRC="${2:-/tmp/openlore-skills}"
LORE_JSON="${3:-/tmp/openlore-lore.json}"
INSTALL_DIR="/opt/openlore"
USER="openlore"

echo "==> Creating user '$USER'..."
if ! id -u "$USER" &>/dev/null; then
    useradd --system --shell /usr/sbin/nologin --home-dir "$INSTALL_DIR" "$USER"
fi

echo "==> Setting up $INSTALL_DIR..."
mkdir -p "$INSTALL_DIR/skills" "$INSTALL_DIR/.ssh"
mkdir -p "$INSTALL_DIR/published"

echo "==> Installing binary..."
cp "$BINARY" "$INSTALL_DIR/openlore"
chmod +x "$INSTALL_DIR/openlore"

echo "==> Installing skills..."
if [ -d "$SKILLS_SRC" ]; then
    cp -r "$SKILLS_SRC"/* "$INSTALL_DIR/skills/"
fi

echo "==> Installing auth config..."
if [ -f "$LORE_JSON" ]; then
    cp "$LORE_JSON" "$INSTALL_DIR/lore.json"
fi

chown -R "$USER:$USER" "$INSTALL_DIR"

echo "==> Creating systemd service..."
cat > /etc/systemd/system/openlore.service << EOF
[Unit]
Description=OpenLore SSH docs server
After=network.target

[Service]
Type=simple
User=$USER
Group=$USER
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/openlore --skills-dir $INSTALL_DIR/skills -p 2222 --http-port 8080 --metrics-port 0 --host-key $INSTALL_DIR/.ssh/openlore_ed25519 --auth $INSTALL_DIR/lore.json
Restart=always
RestartSec=5

# Hardening
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=$INSTALL_DIR/.ssh $INSTALL_DIR/published
PrivateTmp=yes
ProtectKernelTunables=yes
ProtectControlGroups=yes

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable openlore
systemctl restart openlore

sleep 1
systemctl status openlore --no-pager

echo ""
echo "==> OpenLore is running!"
echo "    SSH:  port 2222"
echo "    HTTP: port 8080"
echo "    User: $USER"
echo "    Dir:  $INSTALL_DIR"
