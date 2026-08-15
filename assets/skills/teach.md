# Setting Up OpenLore

OpenLore serves your documentation to AI agents over SSH. Here's how to set it up for your project.

## Quick Start

1. Install OpenLore:
   ```bash
   go install github.com/aakarim/go-openlore/cmd/openlore@latest
   ```

2. Serve your docs directory:
   ```bash
   openlore ./docs
   ```

3. Connect and explore:
   ```bash
   ssh -p 2222 localhost
   ```

Run this way, OpenLore serves the directory you pass on the command line and
runs keyless (any client can connect) — no config file needed.

## Embedding Docs into a Binary

The real power of OpenLore is baking your docs into a single binary that anyone
can run.

1. Clone or fork the repo:
   ```bash
   git clone https://github.com/aakarim/go-openlore
   cd go-openlore
   ```

2. Replace the bundled docs with yours. `assets/lore/` ships with OpenLore's
   own default docs (`getting-started.md`, `commands.md`, …). Remove those and
   drop your own markdown in:
   ```bash
   rm -rf assets/lore/*
   cp -r /path/to/your/docs/* assets/lore/
   ```
   Whatever lives under `assets/lore/` is what the built binary serves at `/`.

3. Replace the bundled config. **Important:** `assets/config/openlore.yml`
   ships configured for the public `openlore.sh` deployment — it sets
   `auth_file: ./lore.json`, mounts published folders, and enables `openlore.sh`
   passkeys. If you build with it unchanged, the binary fails on startup with
   `loading auth config: open ./lore.json: no such file or directory`.

   Overwrite it with a minimal config for your own build:
   ```bash
   cat > assets/config/openlore.yml <<'YAML'
   version: "1"
   port: 2222
   http_port: 8080
   default_cwd: /

   files:
     allowed: ["*.md", "*.txt", "*.yml", "*.json"]
     ignore: [".git", "node_modules", ".env"]
   YAML
   ```
   With no `auth_file` set, the server runs keyless and serves your embedded
   docs out of the box. Add `auth_file: ./lore.json` only once you actually ship
   a `lore.json` (see "Setting Up Access Control").

4. Build the binary:
   ```bash
   go build -o my-lore ./cmd/openlore
   ```

5. Run it — it contains everything:
   ```bash
   ./my-lore
   ssh -p 2222 localhost
   ```
   Distribute the binary and anyone who runs it gets an SSH server with your
   docs. No directory argument is needed: with no path on the command line, the
   binary serves its embedded `assets/lore/` docs.

## Setting Up Access Control

If you want different agents to have different access, create a `lore.json`
next to `openlore.yml`. This quick start gives keyless visitors read-only access
to public docs, reviewers read-only access to backend docs, and engineers
read/write access to backend docs:

```json
{
  "allow_keyless": true,
  "unknown_identity": "deny",
  "roles": {
    "reviewer": {},
    "engineer": {}
  },
  "docsets": {
    "public": {
      "paths": ["/docs/public"],
      "access": {
        "allow": { "guest": "ro", "reviewer": "ro", "engineer": "rw" }
      }
    },
    "backend": {
      "paths": ["/docs/backend"],
      "access": {
        "allow": { "reviewer": "ro", "engineer": "rw" }
      }
    },
    "engineer-home": {
      "paths": [{ "homes/engineer": "/home/engineer" }]
    }
  },
  "identities": [
    {
      "name": "engineering-agent",
      "public_key": "ssh-ed25519 AAAA...",
      "roles": ["engineer", "reviewer"],
      "home": "engineer-home"
    }
  ]
}
```

- `roles` declares the role names identities may use. Role names are exact and
  roles do not inherit from one another.
- Each docset's `access.allow` maps roles to `ro`, `rw`, or a plugin grant such
  as `publish`. Grants from multiple roles are combined; a role listed in
  `access.deny` denies the entire docset and overrides all allows.
- `guest` is built in. It represents keyless and allowed unknown callers and
  can only be granted read-only access.
- An identity may have multiple roles. Its unique `home` docset is implicitly
  read/write for that identity, so it needs no access entry.
- A docset without a matching allow is inaccessible. Nested docsets are
  separate access boundaries, including inside a home.

Enable the file in `openlore.yml`, then start OpenLore normally:

```yaml
auth_file: ./lore.json
readonly: false # keep true if no role should be able to write
```

Set `allow_keyless` to `false` if every SSH connection must present a known
key. Set `unknown_identity` to `allow` only if unknown keys should receive the
built-in `guest` role.

## Using the GitHub Action

For automated builds, use the OpenLore GitHub Action:

```yaml
- uses: aakarim/openlore@v1
  with:
    docs-dir: ./docs
    config: ./openlore.yml
```

This produces binaries for Linux, macOS, and Windows.

## Setting Up Passkeys (Browser Access for Humans)

Passkeys let humans browse your docs in a web browser using WebAuthn (Face ID,
Touch ID, security keys).

1. Add to your `openlore.yml`:
   ```yaml
   passkeys:
     enabled: true
     rp_id: localhost                       # or your domain
     rp_origins: ["http://localhost:8080"]  # must match the HTTP server origin
   ```

2. Start the server, connect via SSH, and register:
   ```bash
   ssh -p 2222 localhost "passkey register --name 'My Laptop'"
   ```

3. Open the printed URL in a browser to complete registration.

4. Browse docs at `http://localhost:8080/lore/`.

Run `passkey help` in the SSH shell for all options.

## Exporting Embedded Docs

To extract docs from an existing OpenLore binary:

```bash
openlore export -o ./extracted-docs
```
