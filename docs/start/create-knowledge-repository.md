# Create a knowledge repository

Set up the folder that holds your server's settings, commit it, and start a server from it.

A knowledge repository is a Git repository with two kinds of file in it: the settings that describe a server, and, under `home/`, what each person syncs into it with the `lore` client. Documents live on the server; the repository tells the server who can read which of them. Committing it means a teammate can clone it, run `lore login` and have the same server, docsets and sources as you.

```text
acme-lore/
├── openlore.yml                  server settings
├── lore.json                     identities, roles and docsets
├── home/
│   └── alice/sources.yaml        what alice syncs with lore
├── content/                      documents a local server serves; ignored by Git
└── .gitignore
```

`openlore.yml` is the marker: `lore` finds the repository by walking up from the working directory until it meets that file.

---

### 1. Create the folder

Name it after the team and initialise Git:

```shell
lore init acme-lore
cd acme-lore
git init
```

`lore init` creates the folder and a default `openlore.yml`; Git reports an
empty repository. Everything below goes into this folder. For an existing
folder without `openlore.yml`, run `lore init` inside it instead.

---

### 2. Describe the server

Replace the generated `openlore.yml` with the settings below to add the address clients use, access policy and token issuance for `lore login`. Every key is listed in the [`openlore.yml` reference](../reference/openlore-yml.md).

```yaml
version: "1"

port: 2222
http_port: 8080
external_url: http://localhost:8080

auth_file: lore.json
data_dir: .local/data
host_key_path: .local/openlore_ed25519

api:
  enabled: true
  path: /api

tokens:
  issuer: http://localhost:8080
  audience: http://localhost:8080
```

`external_url` is the address `lore login` opens; for a hosted server it is the public HTTPS address, and `tokens` lets the server mint the tokens `lore` signs in with. [Configure the server](../how-to/configure-the-server.md) covers HTTPS and the other blocks.

---

### 3. Describe who reads what

Create `lore.json` with one role, one [identity](../concepts/auth.md) and the [docsets](../concepts/docsets.md) the server mounts. Each docset names a folder under the directory you serve and the path agents see it at.

```json
{
  "roles": { "engineers": {} },
  "identities": [
    { "name": "alice", "roles": ["engineers"], "home": "alice-home" }
  ],
  "docsets": {
    "alice-home": { "paths": [{ "user/alice": "/user/alice" }] },
    "notes": {
      "paths": [{ "notes": "/notes" }],
      "access": { "allow": { "engineers": "rw" } }
    }
  }
}
```

`alice` gets a home at `/user/alice` and can read and write `/notes`; anyone else sees neither. Add a `public_key` to the identity to let it sign in over SSH, or leave it out and sign in with a passkey or a token.

---

### 4. Keep local state out of Git

The server writes its data directory, host key and per-machine caches under `.local/`, and a local server serves documents from `content/`. Ignore both:

```text
/.local/
/content/
```

Save that as `.gitignore`. What remains is settings only: no keys, no tokens, no documents.

---

### 5. Start a server from it

Create the folders the docsets point at:

```shell
mkdir -p content/notes content/user/alice
```

Then serve them:

```shell
openlore ./content
```

You should see the startup banner with `config: loaded ./openlore.yml`, SSH on port `2222` and HTTP on port `8080`. The server reads `lore.json` from the repository and mounts `/notes` and `/user/alice`.

> **Tip:** `ssh openlore.sh setup | claude` builds the team version of this repository for you, with a `Containerfile`, a `deploy/` folder and a local verification run.

---

### 6. Commit it

Record the settings:

```shell
git add openlore.yml lore.json .gitignore && git commit -m "Create knowledge repository"
```

Later pages add `home/<identity>/sources.yaml` beside these files; commit it the same way. `.local/` and `content/` never appear in a commit.

Congrats! You have a knowledge repository that describes a server, and a server running from it.

---

## Next steps

- [Import knowledge](import-knowledge.md) into the server with the `lore` client.
- [Deploy a shared server](deploy.md) from the same repository.
- Learn how [auth](../concepts/auth.md) turns `lore.json` into who sees what.
