# Dashboard

Distribution builds replace the landing page and `/lore/<path>` browser with
the **Analytics / Files** dashboard. Existing published file URLs keep working;
`passkeys.lore_path` is respected. See [building the dashboard](dashboard-build.md)
for the source-only frontend build and backend-only Go builds.

## Is the dashboard ready?

SSH and MCP work as soon as `openlore` starts. Dashboard data does not: it
needs an `auth_file` with identities and a registered passkey to sign in with.
The startup banner says which state you are in and what moves you forward:

```text
  Dashboard:  no authentication is configured; dashboard data needs identities to sign in as
              next step: set `auth_file: ./lore.json` in openlore.yml and restart (see docs/configuration-and-identity.md)
```

The checks run in the order you fix them: `http_port` enabled, a build with the
frontend embedded (see [Building from source](building-from-source.md)),
`auth_file` set, passkeys not turned off, and at least one passkey registered.
Once all pass, the banner prints the dashboard URL instead.

Passkeys are on by default. The banner only mentions `passkeys.enabled` when
you set it to `false` yourself, and the step is to remove that line and
restart. There is nothing to enable on a fresh install.

The last state, no passkey registered yet, is a short guide rather than one
line, because registration happens in two places (an SSH shell and a browser):

```text
  Dashboard:  no passkey is registered yet
              next steps:
              1. from any SSH shell, run `ssh -p 2222 localhost passkey register --identity adil` (any identity in lore.json, with or without an SSH key)
              2. within 5 minutes, open the printed link in a browser on the device whose passkey you want to use; the link must be opened at an origin listed in `passkeys.rp_origins` (currently http://localhost:8080)
              3. open http://localhost:8080/dashboard/ and sign in with that passkey; the session lasts `passkeys.session_ttl`
```

If `auth_file` exists but holds no identities, the guide starts with
`openlore identity add` so there is an identity to register the passkey for.
The passkey login page (`/passkey/login`) shows the same steps, with the `ssh`
command filled in for the address you opened it at, plus a prompt you can give
an agent that runs the command for you and hands back the link.

Opening the dashboard before it is ready shows the same steps in the browser,
and `/dashboard/api/*` returns them as a `next_steps` array in the JSON error
body. When sign-in is possible, the 401 body carries `login_url` instead and
the dashboard sends you to the login page.

## Authentication and authority

The HTML/JavaScript shell contains no document or analytics data and can be
loaded publicly. Every dashboard data endpoint requires configured
authentication and a valid passkey session or bearer token, including on
instances whose shell and API allow anonymous access. The dashboard reads
through dedicated GET endpoints and is read-only: it shows files, permissions
and revisions and changes none of them.

On authenticated instances, missing or invalid credentials return 401 from
dashboard and analytics data endpoints so the UI can recover expired sessions.
Authenticated resource permission denials remain 404.

Files and current content facts use the same identity-scoped canonical
filesystem as other transports. A grant on a parent docset does not cross into
a separately governed nested docset. Policy is resolved again on each request;
an open browser is not a new source of authority.

With the default SQLite analytics store, per-file facts and ownership-aware
directory totals are durable in `<analytics.dir>/aggregations.sqlite`. Each file
belongs to its most-specific configured docset, so a parent docset's own total
does not absorb a nested docset. System mounts and synthetic session files are
excluded unless explicitly rooted in a content docset. Identity filtering still
happens before visible indexed files are folded into a response, and restricted
docsets are reported only as omitted coverage—never as their counts or sizes.
Facts are computed from raw on-disk bytes rather than display transforms.

Dashboard requests do not walk document bodies or retained log files. They
return the latest compatible committed view and enqueue missing or outdated
work. Only a view with no result yet is reported as updating; a published result
stays ready while it refreshes in the background, and the dashboard checks for
the refreshed result each minute. Cold, updating, disabled, failed, and partial
coverage are distinct states. Activity keeps the last complete requested time
window rather than publishing an arbitrary event prefix. Activity totals and
activity tables are built from cached per-day results, so refreshing a long
range rescans only the current day and the partial day at the start of the
range, and every time range shares the same cached days; a day is rebuilt if
late events arrive for it. Cached results survive restarts and are discarded
only when the caller's access policy or the docset configuration changes.
Session counts, ranges longer than a year, and tables from plugins that do not
support per-day results are rebuilt in full, at most every 15 minutes for long
ranges. Building views that have no result yet runs
first, but background work such as event indexing still gets a regular turn. Durable event indexing
streams from the append-only event log with idempotent event keys and a durable
checkpoint. The legacy `analytics.aggregations.store: file` keeps the older
synchronous compatibility path and does not provide durable dashboard views.

Analytics is shared among readers of a docset. `lore:analytics:view` is no longer
required for these scoped views. Historical events must also satisfy current
docset permissions and the selected path. Mixed-scope searches and ambiguous
unscoped legacy commands are omitted rather than revealing another docset's
queries, paths, or attribution. Scoped results are keyed by the caller's current
policy and docset configuration, so policy changes make incompatible views
immediately unreachable. Browser previews do not count as agent reads.

The shell `analytics` command is an instance-wide operator interface, not a
docset-scoped reader interface. All subcommands require the explicit
`lore:analytics:admin` capability and full token scope; current policy is checked
on every invocation, including revocation and deny rules. Neither docset access
nor the former `lore:analytics:view` capability grants this authority. Operators
may grant it through their chosen role's `allow.capabilities`. Scoped readers
use the dashboard/API instead. Standalone shell hosts must explicitly install
an analytics authorizer; supplying the service alone does not grant access.

### Access tab

Operators can grant the read-only **`lore:access:view`** capability to whichever
roles they designate as administrators:

```json
{
  "roles": {
    "knowledge-admin": {
      "allow": { "capabilities": ["lore:access:view"] }
    }
  }
}
```

Merge this fragment into your `lore.json`; there is no built-in administrator
role. Capability denials win, and the
capability never grants access to another docset. A read-scoped token retaining
this capability can inspect Access but still cannot mutate anything.

Access displays actual role grants/denials for visible governing docsets.
The number of roles with a grant is not a count of individual users: role
combinations, delegation, and token scopes can further restrict an identity.
Folder validation-rule layers are shown separately from ACLs. They are not
folder permissions and this view does not edit them.

## Facts versus activity

**Current facts** describe existing visible content: bytes, lines, Unicode
characters, and context estimates. **Time range** controls recorded activity,
not those facts or Access. No recorded read in a range means exactly that; it
does not prove a file has never been used. Period totals can include activity
on files since deleted, while current-file rankings describe the live corpus.

Most-used and least-used line rankings support files up to 100,000 lines.
Larger files return an explicit error rather than a partial ranking, bounding
per-request line-tracking memory even for newline-heavy content.

Analytics settings are display preferences. Selecting four or six characters
per token changes the estimate, not the backend's configured tokenizer or
validation rules. Context-window percentage compares current content with the
selected window; period token totals estimate cumulative input across recorded
reads. They are not monetary costs or model billing records. Unknown historical
read sizes remain explicitly incomplete rather than using the current revision.
Human, agent, and unknown attribution are distinct. A delegated actor is counted
as an agent, a named principal acting directly is counted as human, and activity
without either attribution remains unknown.

Refresh promotes visible stats and exposes their committed computation time.

> **Note:** Analytics is best-effort telemetry. Events are buffered and flushed
> periodically, so a crash can drop the most recent batch and retention limits
> how far back you can look. Use the write log when you need a complete record.

## File viewer and browser state

Files use the production GFM Markdown renderer, with a source view for text.
Raw HTML, scripts, and SVG are downloads rather than executable same-origin
documents. Markdown does not enable raw HTML; remote embedded images are blocked
by the dashboard's content policy. Timeline is metadata only. It may be
unavailable on read-only deployments or when history storage is disabled; it
does not promise historical bodies, diffs, or restoration.

Desktop supports open file tabs and an Info/Timeline panel. Mobile uses native
page scrolling with Folders, Open files, and Details sheets; Analytics replaces
the Open files control with its contextual category picker.

Preferences, open paths, and reading positions are browser-local and separated
by origin and identity. Document bodies and analytics responses are not
persisted. Restored paths must be revalidated. Data responses use `private,
no-store`; do not configure an external proxy to cache them.

The lazy folder tree supports large folders. Full context walks are bounded to
10,000 nodes and 64 levels, while each file read is limited to 64 MiB. A
workspace may contain more than 64 MiB in total and still receive exact root
analytics. If a selected scope exceeds a structural limit, choose a narrower
folder; a truncated total is never presented as a complete one. File
previews/downloads also have a 64 MiB per-file limit.

## Next steps

- [openlore.yml reference](openlore-yml.md#passkeys) documents the
  `passkeys` and `analytics` keys the dashboard depends on.
- [Building the dashboard](dashboard-build.md) explains how the web assets are
  built and embedded.
- [Write system internals](write-system.md) describes the write log that the
  activity view reads from.
