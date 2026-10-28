# Administering OpenLore Over SSH

Operators (and trusted agents) can inspect and edit the server's access-control
configuration — `lore.json` — from inside an SSH session. No separate admin
tool: the config is a file in your session, and you edit it with the same
commands you use on any other file.

## Who can do this

Config administration is gated on the `lore:config:edit` capability, granted
through a role:

```json
"roles": {
  "admin": {
    "allow": { "capabilities": ["lore:config:edit"] }
  }
}
```

Identities holding such a role get two things in their session:

- the config mount: `/opt/openlore/lore.json` (everyone else gets permission
  denied on `/opt` — it isn't even listed)
- the `lore config reload` command

A delegated identity (e.g. an OAuth client acting through your identity) can be
stripped of this power with `"deny_capabilities": ["lore:config:edit"]` on the
delegate entry — recommended for any third-party client.

## The editing loop

```shell
# 1. Read the current config
cat /opt/openlore/lore.json

# 2. Edit it — full rewrite, patch, or in-place edit
echo "$NEW_CONFIG" | write /opt/openlore/lore.json
cat change.diff | patch /opt/openlore/lore.json
sed -i 's/"roles": \["reviewer"\]/"roles": ["reviewer", "admin"]/' /opt/openlore/lore.json

# 3. Apply it to the running server
lore config reload
```

Every write is guarded:

- **Validated** — the write is rejected unless the content parses as JSON and
  passes full config validation. You cannot save a broken config.
- **Compare-and-swap** — if someone else changed the config after you read it,
  your overwrite fails instead of clobbering theirs. Re-read and re-apply.
- **Audited** — successful edits, reloads, and rejected attempts are recorded
  as `config.edit`, `config.reload`, and `config.reject` audit events with the
  acting identity.

## What you can change live (edit + `lore config reload`)

- **Roles** — create or remove roles, change their capability `allow`/`deny`
  lists (`spawn`, `lore:config:edit`, …)
- **Identities** — add or remove identities, public keys, role membership,
  `home`, token `match` rules, and `delegates`
- **Docsets** — add, remove or change paths, aliases, projections and their
  store/subpath/filters, access grants, read-only locks, and per-docset rules
- **Rules** — change top-level rules as well as docset rules

Example — create a role and put an identity in it:

```shell
cat /opt/openlore/lore.json          # read (also arms compare-and-swap)
# … produce the updated JSON with the new role + membership …
echo "$NEW_CONFIG" | write /opt/openlore/lore.json
lore config reload
```

Reload validates the complete candidate before replacing the running policy,
projection mounts and rules together. Existing file-backed sessions observe
new readable roots, aliases, grants and docset listings on subsequent operations.
An already-started read may finish with the previous view. Content writes are
serialized with reload and rechecked at commit. `$HOME`, the current working
directory and command visibility remain session settings; reconnect to refresh
those. External authorization stores retain their stable read-policy contract.

Invalid edits through the config mount leave both the file and running policy
unchanged. A rejected reload of a host-edited file keeps the previous running
configuration and leaves the supplied file on disk for the operator to fix.

Example — create a docset mounted at `/kb/research`, readable by `reviewer`
and writable by `engineer`:

```json
"docsets": {
  "research": {
    "paths": ["/kb/research"],
    "access": {
      "allow": { "reviewer": "ro", "engineer": "rw" }
    }
  }
}
```

After saving and running `lore config reload`, verify from the same session:

```shell
lore docsets      # the new docset appears with its grants and path
```

## What requires a server restart

Changes to **`allow_keyless`**, **`unknown_identity`** and **`default_cwd`** are
rejected when written through the session mount. Edit the host or volume-mounted
`lore.json` and restart the process to change them. `openlore.yml` also remains
startup-only configuration.

## Quick reference

| Change | How | Applies |
|---|---|---|
| Role capabilities | edit mount + `lore config reload` | immediately |
| Identity keys / roles / home / delegates | edit mount + `lore config reload` | immediately |
| Create / mount / remove a docset or projection | edit mount + `lore config reload` | immediately |
| Docset ACLs, aliases, projection filters | edit mount + `lore config reload` | immediately |
| Docset and top-level rules | edit mount + `lore config reload` | immediately |
| `allow_keyless`, `unknown_identity`, `default_cwd` | edit host `lore.json` + restart | on restart |

See `auth.md` for the full `lore.json` shape and RBAC model, and `writes.md`
for the write commands used in the editing loop.
