# Onboard Initial OpenLore Identities

Before its first deployment, customize who can connect and what they can use.
Open with “👋 Let's add someone to your lore server.” Ask one question at a time
and wait; explain why it matters, define jargon just in time, and give the
recommended default. Use restrained emojis, validate quietly, mention only
failures, and end each identity with a plain-language ✅ summary.

## Preconditions

Require root `openlore.yml`, `.local/lore.json`, and `.local/filesystem`. If
`.local` was discarded or `deploy/` records a verified deployment, stop: edit
the authoritative server instead. Validate current policy before changes; never
incidentally remove or weaken access.

Expect the setup record: docset `infrastructure` at `/channel/infrastructure`
holding `index.md`, `openlore-server.md`, and `log.md` in the Open Knowledge
Format (OKF: markdown with a small YAML header). If the project predates it,
create the docset and the three files exactly as `setup` describes under
“Record the setup” (`ssh openlore.sh setup`) before continuing, using facts
from the existing project and `lore.json` only.

## Interview for each identity

Ask in order, one per turn:

- identity name (their OpenLore account name; required, no safe default);
- SSH public key and matching local private-key path (only used to prove login;
  never copied or displayed);
- home folder (default `/user/<identity>`, their private login location);
- roles (permission bundles; default `agent`);
- additional knowledge folders and read/write needs (default only the shared
  `/channel/general` and `/channel/infrastructure` through `agent`);
- initial folders/documents (default a short `README.md` in each empty docset).

Offer the existing `agent` role by default so the identity can write the shared
`general` docset at `/channel/general` and the setup record at
`/channel/infrastructure`. A unique home docset is implicitly writable by its
owner. Explain administrator capabilities and require explicit confirmation
before adding `administrator` or `lore:config:edit`.

Validate parsed key material, duplicates, homes, roles, path collisions, nested
docset boundaries, and grants before writing. Never access or store private keys.

## Apply coherently

Edit `.local/lore.json` atomically and create matching virtual paths (for example
`.local/filesystem/user/alice/` and `channel/general/`). Never
put host keys, signing material, logs, or databases in bootstrap state.
Put a short `README.md` in every newly created empty docset so its purpose and
successful visibility are obvious; never overwrite existing content.

Use OpenLore policy validation and commands where applicable; otherwise make a
minimal JSON edit, validate the whole result, and preserve formatting.

Update the setup record in the same step, so it never lags the policy:

- add or amend the identity's row in `# Identities and access` of
  `.local/filesystem/channel/infrastructure/openlore-server.md` (identity,
  roles, home, docsets and grants, delegates); list new docsets there too;
- refresh `generated.at` (UTC, `date -u +%Y-%m-%dT%H:%M:%SZ`) and set
  `generated.by` to yourself as `<agent name>/<model or version>`;
- add `**Update** — onboarding added <identity> (<roles>) …` under today's
  `## YYYY-MM-DD` heading in `log.md`, newest date first, reusing the day's
  heading when it exists.

Never record public-key contents, private-key paths, or other key material in
the record. Keep the frontmatter OKF-valid; the docset's `okf` rule rejects a
malformed header and names what to fix.

## Verify locally

Restart Compose. Refresh only this project's `.local/known_hosts` entry if the
host key changed; extend `.local/ssh_config` for the key with strict checking.
Quietly connect directly and prove:

1. the new public key resolves to the intended identity;
2. login starts in the intended home;
3. allowed docsets can be read or written at the requested grant;
4. denied/ungranted paths remain inaccessible;
5. `lore validate /channel/infrastructure` reports no errors and the record
   shows the new identity;
6. changes survive a container restart.

Remove only disposable verification files. Report the exact SSH command and
finish with a friendly ✅ summary of the identity, home, roles, visible folders,
and what read/write means in practice. Give a ready-to-run terminal command and
an example that reads the new home `README.md`. `.local` is gitignored, so do
not offer to commit its policy or filesystem contents. Ask whether to add
another identity; if not, offer to continue with “run deploy” — to share the
server with the team — or later via `ssh openlore.sh deploy | <agent-cli>`.

If the person stops here, open the setup record for them and say so:

```bash
open .local/filesystem/channel/infrastructure/openlore-server.md   # macOS
xdg-open .local/filesystem/channel/infrastructure/openlore-server.md   # Linux
```

If neither opener exists, give the absolute path instead.
