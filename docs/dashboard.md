# Dashboard

Distribution builds replace the landing page and `/lore/<path>` browser with
the **Analytics / Files** dashboard. Existing published file URLs keep working;
`passkeys.lore_path` is respected. See [building the dashboard](dashboard-build.md)
for the source-only frontend build and backend-only Go builds.

## Authentication and authority

The HTML/JavaScript shell contains no document or analytics data and can be
loaded publicly. All dashboard data requires configured authentication and a
valid passkey session or bearer token. An authless instance does **not** expose
dashboard data, even when its shell/API otherwise allows anonymous access.
The dashboard has dedicated GET endpoints, not a wrapper around `/api/shell`.
It cannot write files, change permissions, or restore revisions.

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
return the latest compatible committed view, enqueue or promote missing/stale
work, and poll while it runs. Cold, stale/updating, disabled, failed, and partial
coverage are distinct states. Activity keeps the last complete requested time
window rather than publishing an arbitrary event prefix. Durable event indexing
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

This is a fragment to merge into `lore.json`, not a complete configuration.
There is no built-in administrator role. Capability denials win, and the
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
Analytics is buffered telemetry, not an audit-proof record of every operation;
delayed or dropped events and configured retention affect what can be observed.

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
