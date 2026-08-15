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

## Embedding Docs into a Binary

The real power of OpenLore is baking your docs into a single binary that anyone can run:

1. Clone or fork the repo:
   ```bash
   git clone https://github.com/aakarim/go-openlore
   cd go-openlore
   ```

2. Place your documentation in `assets/lore/`:
   ```bash
   rm assets/lore/PUT_YOUR_DOCS_HERE
   cp -r /path/to/your/docs/* assets/lore/
   ```

3. (Optional) Customize the config in `assets/config/openlore.yml`

4. Build the binary:
   ```bash
   go build -o my-lore ./cmd/openlore
   ```

5. Distribute the binary — it contains everything. Anyone who runs it gets an SSH server with your docs.

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

Passkeys let humans browse your docs in a web browser using WebAuthn (Face ID, Touch ID, security keys).

1. Add to your `openlore.yml`:
   ```yaml
   passkeys:
     enabled: true
     rp_id: localhost                      # or your domain
     rp_origins: ["http://localhost:8080"]  # must match HTTP server origin
   ```

2. Start the server, connect via SSH, and register:
   ```bash
   ssh -p 2222 localhost "passkey register --name 'My Laptop'"
   ```

3. Open the printed URL in a browser to complete registration.

4. Browse docs at `http://localhost:8080/lore/`.

Run `passkey help` in the SSH shell for all options, or see `/docs/passkeys.md` for full details.

## Exporting Embedded Docs

To extract docs from an existing OpenLore binary:

```bash
openlore export -o ./extracted-docs
```
