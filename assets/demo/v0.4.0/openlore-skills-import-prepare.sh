#!/bin/bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
DEMO=/tmp/openlore-skills-import-demo
SSH_PORT=2399

if [[ -f "$DEMO/server.pid" ]]; then
  kill "$(cat "$DEMO/server.pid")" 2>/dev/null || true
fi
tmux -L openlore-skills-video kill-server 2>/dev/null || true
rm -rf "$DEMO"
mkdir -p "$DEMO"/{bin,published/home/demo,.ssh,source}

ssh-keygen -q -t ed25519 -N '' -f "$DEMO/.ssh/demo_ed25519"
PUBLIC_KEY=$(awk '{print $1 " " $2}' "$DEMO/.ssh/demo_ed25519.pub")

cat > "$DEMO/embedded-openlore.yml" <<EOF
version: "1"
port: $SSH_PORT
http_port: 8094
metrics_port: 3094
default_cwd: /
auth_file: ./lore.json
data_dir: ./data
writable_dir: ./published
readonly: false
plugins:
  skills:
    enabled: true
    remote_check_ttl: 1s
    remote_timeout: 10s
    remote_max_bytes: 10MB
EOF

cat > "$DEMO/lore.json" <<EOF
{
  "allow_keyless": false,
  "unknown_identity": "deny",
  "default_cwd": "/home/demo",
  "roles": {},
  "docsets": {
    "docs": {
      "paths": ["/"],
      "access": {"allow": {"guest": "ro"}}
    },
    "demo-home": {
      "paths": ["/home/demo"]
    }
  },
  "identities": [
    {
      "name": "demo-agent",
      "public_key": "$PUBLIC_KEY",
      "home": "demo-home"
    }
  ]
}
EOF

rsync -a --delete --exclude .git --exclude assets/demo "$ROOT/" "$DEMO/source/"
cp "$DEMO/embedded-openlore.yml" "$DEMO/source/assets/config/openlore.yml"
(cd "$DEMO/source" && go build -o "$DEMO/openlore" ./cmd/openlore)

cat > "$DEMO/.ssh/config" <<EOF
Host skills.local
  HostName 127.0.0.1
  Port $SSH_PORT
  User demo
  IdentityFile $DEMO/.ssh/demo_ed25519
  IdentitiesOnly yes
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
  LogLevel ERROR
EOF

cat > "$DEMO/bin/ssh" <<'EOF'
#!/bin/bash
if [[ "${1:-}" == "openlore.sh" && "${2:-}" == "teach" ]]; then
  printf 'Teach the agent to install OpenLore and create a private home docset.\n'
  exit 0
fi
exec /usr/bin/ssh -F /tmp/openlore-skills-import-demo/.ssh/config "$@"
EOF

cat > "$DEMO/bin/claude" <<'EOF'
#!/bin/bash
cat >/dev/null
printf '\033[1;32m✓ OpenLore installed\033[0m\n'
printf '\033[1;32m✓ Private agent home configured\033[0m\n'
printf '\033[1;35mReady: openlore ./knowledge\033[0m\n'
EOF

cat > "$DEMO/bin/openlore" <<'EOF'
#!/bin/bash
DEMO=/tmp/openlore-skills-import-demo
if [[ "${1:-}" != "./knowledge" ]]; then
  printf 'usage: openlore ./knowledge\n' >&2
  exit 2
fi
cd "$DEMO"
nohup ./openlore </dev/null >server.log 2>&1 &
echo $! >server.pid
for _ in $(seq 1 50); do
  if /usr/bin/ssh -F "$DEMO/.ssh/config" skills.local pwd >/dev/null 2>&1; then
    printf '\033[1;32m✓ OpenLore ready\033[0m  ssh://skills.local\n'
    printf '  Private home: /home/demo\n'
    exit 0
  fi
  sleep .2
done
printf 'OpenLore did not start\n' >&2
exit 1
EOF

cat > "$DEMO/bin/user-prompt" <<'EOF'
#!/bin/bash
printf '\033[1A\033[2K\r\n\033[1;35muser ❯\033[0m can you import the grill-me skill from\n'
printf '       https://github.com/mattpocock/skills into my home folder\n'
printf '       in track it so it automatically updates\n'
EOF

cat > "$DEMO/bin/use-skill-prompt" <<'EOF'
#!/bin/bash
printf '\033[1A\033[2K\r\n\033[1;35muser ❯\033[0m use the grill-me skill in my openlore home directory\n'
EOF

cat > "$DEMO/bin/agent-use" <<'EOF'
#!/bin/bash
printf '\033[1A\033[2K\r\n\033[1;36magent ❯\033[0m I’ll locate the tracked skill and follow its instructions.\n'
EOF

cat > "$DEMO/bin/agent-grill" <<'EOF'
#!/bin/bash
printf '\033[1A\033[2K\r\n\033[1;36magent ❯\033[0m Following grill-me → /grilling\n'
printf '\n\033[1;35m❓ Q1 — What should I grill?\033[0m\n'
printf '   Paste the plan, decision, or idea you want me to stress-test.\n'
EOF

cat > "$DEMO/bin/demo-success" <<'EOF'
#!/bin/bash
printf '\033[1A\033[2K\r\n\033[1;32m✓ grill-me imported · main tracked · used from OpenLore home\033[0m\n'
EOF

chmod +x "$DEMO/bin/"*

tmux -L openlore-skills-video new-session -d -s demo -x 220 -y 55 \
  "env PATH=$DEMO/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin PS1='\[\033[1;33m\]server ❯\[\033[0m\] ' bash --noprofile --norc"
tmux -L openlore-skills-video split-window -h -l 62% -t demo \
  "env PATH=$DEMO/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin PS1='\[\033[1;33m\]agent ❯\[\033[0m\] ' bash --noprofile --norc"
tmux -L openlore-skills-video set-option -t demo pane-border-status top
tmux -L openlore-skills-video set-option -t demo pane-border-format ' #{pane_title} '
tmux -L openlore-skills-video set-option -t demo pane-border-style 'fg=#6E6383'
tmux -L openlore-skills-video set-option -t demo pane-active-border-style 'fg=#FF9A68'
tmux -L openlore-skills-video select-pane -t demo:0.0 -T 'SERVER · SETUP'
tmux -L openlore-skills-video select-pane -t demo:0.1 -T 'AGENT · SKILL IMPORT'
tmux -L openlore-skills-video select-pane -t demo:0.0
printf 'video-session-ready\n'
