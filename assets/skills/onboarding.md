# Onboard Initial OpenLore Identities

Customize identities, roles, docsets, and initial SSH-visible folders in an
OpenLore project's local bootstrap state before its first deployment.

## Preconditions

1. Work only from a directory containing root `openlore.yml`.
2. Require `.local/lore.json` and `.local/filesystem`; if `.local` was discarded
   after deployment, stop. Live administration belongs on the server and this
   initial-onboarding skill must not recreate local state from guesses.
3. Inspect `deploy/` for a verified authoritative deployment. If one exists,
   stop and direct the user to edit the live policy through OpenLore or the
   provider administrative shell.
4. Parse and validate the current policy before proposing changes. Never remove
   or weaken existing access as an incidental side effect.

## Interview for each identity

Ask for:

- identity name;
- an existing SSH public key;
- home path, defaulting to `/user/<identity>`;
- roles;
- additional readable or writable docsets;
- initial folders or documents.

Offer the existing `agent` role by default so the identity can write the shared
`general` docset at `/channel/general`. A unique home docset is implicitly
writable by its owner. Explain administrator capabilities and require explicit
confirmation before adding `administrator` or `lore:config:edit`.

Public-key comments are allowed. Validate keys by parsed key material. Detect
duplicate identity names, keys, homes, role names, path collisions, nested
docset boundaries, and invalid grants before writing anything. Never access or
store a private key.

## Apply coherently

Edit `.local/lore.json` as one validated change and create matching paths under
`.local/filesystem/`, for example:

```text
.local/filesystem/user/alice/
.local/filesystem/channel/general/
```

Initial files mirror their final OpenLore virtual paths. Do not put generated
host keys, signing material, audit logs, or runtime databases in `.local`.

Use OpenLore's policy validation and existing identity/role commands where they
cover the operation. For policy structures those commands do not create, make a
minimal JSON edit, validate the complete result, and preserve stable formatting.

## Verify locally

Start or restart `.local/compose.yml`, then prove:

1. the new public key resolves to the intended identity;
2. login starts in the intended home;
3. allowed docsets can be read or written at the requested grant;
4. denied/ungranted paths remain inaccessible;
5. changes survive a container restart.

Remove only disposable verification files. Report the exact SSH command and
the resulting identity, home, roles, and grants. `.local` is gitignored, so do
not offer to commit its policy or filesystem contents.
