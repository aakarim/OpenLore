# Authentication & Access Control

## Keyless (Default)

By default, any SSH client can connect. No keys required.

## RBAC Quick Start

Create `lore.json` next to `openlore.yml`. This example gives keyless visitors
read-only public docs, reviewers read-only backend docs, and engineers
read/write backend docs:

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

Enable it in `openlore.yml`:

```yaml
auth_file: ./lore.json
readonly: false # keep true if no role should be able to write
```

Then start OpenLore normally. No role-management commands are required; edit
`lore.json` and restart the server after changing file-backed policy.

### How access is resolved

- `roles` declares the exact role names available to identities. Roles are flat
  and do not inherit.
- `docsets` own access policy. `access.allow` maps each role to `ro`, `rw`, or a
  plugin grant such as `publish`.
- An identity may have multiple roles. Their allows are combined, while any
  matching role in a docset's `access.deny` denies the entire docset.
- `guest` is built in for keyless and allowed unknown callers. It can only be
  granted read-only access.
- A unique `home` docset is implicitly read/write for its owner and needs no
  access entry. Nested docsets remain separate access boundaries.
- A docset without a matching allow is inaccessible.

## Publishing

Docsets can be made writable by adding `publish_dir` to the docset config in `lore.json`:

```json
"backend": { "paths": ["/docs/api", "/docs/backend"], "publish_dir": "./published/backend" }
```

The `publish_dir` is a directory on disk where published files are written. If it falls within the served directory tree, published files appear in the VFS immediately.

Use the `publish` command to write files:

```bash
echo "content" | publish <docset> <path>
```

Running `publish` with no args lists writable docsets.

Remote usage over SSH:

```bash
echo "content" | ssh -p 2222 server publish <docset> <path>
```

## Write Conflict Policy

When the writable substrate is enabled (`readonly: false`), whole-file overwrite
verbs — `>`, `tee`, `sed -i`, and `publish` — follow a configurable conflict
policy:

- **`hash`** (default) — overwrites are **compare-and-swap**. The write carries
  the hash of the content it is based on; if the file changed since then, the
  write is rejected with `file changed concurrently — re-read and retry` instead
  of silently clobbering the other change. For read-modify-write verbs (`sed -i`)
  the base is exactly what was transformed, giving true optimistic concurrency;
  for a blind redirect it is the content read at command time.
- **`last_write_wins`** — overwrites are unconditional (atomic, but the last
  writer silently wins).

Append (`>>`) and `patch` are **always** compare-and-swap, regardless of policy.

Set the global default in `openlore.yml`:

```yaml
write_conflict_policy: hash   # or last_write_wins
```

Override it per docset in `lore.json` (takes precedence for paths in that docset):

```json
"scratch": {
  "paths": ["/scratch"],
  "publish_dir": "./published/scratch",
  "write_conflict_policy": "last_write_wins"
}
```

## Unknown Identity Handling

In `lore.json`:
- `"unknown_identity": "allow"` — unrecognized keys receive the built-in
  `guest` role
- `"unknown_identity": "deny"` — unrecognized keys are rejected

## Federated Access (WIF)

To let CI runners, agents, and services reach the `/mcp` and `/api` endpoints
without long-lived keys — by exchanging a short-lived IdP token (GitHub Actions
OIDC, Kubernetes/SPIFFE, Okta/Entra/…) for an OpenLore token — see
`cat /workload-identity-federation.md`.
