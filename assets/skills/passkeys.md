# Passkeys — Browser Access for Humans

Register a passkey so humans can browse docs in a web browser via WebAuthn
(Face ID, Touch ID, security keys).

## Register a passkey

```bash
ssh <server> passkey register --identity <identity> --name "Adil MacBook"
```

`--identity` (required) names an identity from the server's auth config; the
passkey signs the person in as that identity and inherits exactly its docset
access. `--name` labels the device (default: "default").

The command outputs a one-time URL (expires in 5 minutes). Give it to the human
to open in their browser to complete registration. If it expires, rerun the
command.

## Manage passkeys

```bash
# List all registered passkeys
ssh <server> passkey list

# Revoke a passkey by device label
ssh <server> passkey revoke "Adil MacBook"
```

## After registration

The human can browse docs at `https://<domain>/lore/`. They can also go
directly to a file, e.g. `https://<domain>/lore/docs/README.md` — if not logged
in, the passkey login flow starts automatically and redirects back to the file.
`https://<domain>/settings/permissions` shows what their delegated identities
may access.

## Server requirements

Passkeys must be enabled in `openlore.yml` with the server's public HTTPS
origin:

```yaml
passkeys:
  enabled: true
  rp_id: docs.example.com
  rp_origins: ["https://docs.example.com"]
```
