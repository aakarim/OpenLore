---
name: maintaining-changelog
description: Maintains OpenLore's changelog, release notes, and documentation version guidance. Use when preparing a release, recording user-facing changes, or syncing published release history into the docs.
---

# Maintaining the OpenLore changelog

Keep `docs/changelog.md` complete and useful to users running different versions.
Keep maintainer instructions in this skill, not on the public changelog page.

## Record upcoming changes

- Add user-facing changes under **Unreleased**, including compatibility,
  migration steps, and known limitations. Do not describe unreleased behavior as
  available in the current release.
- Mark version-specific or unreleased behavior on the documentation pages that
  describe it.

## Publish a release entry

1. Read `assets/config/VERSION` and the published GitHub release. Confirm the tag,
   publication date, and availability; a Git tag alone is not a published release.
2. Move the relevant Unreleased entries into a dated version section, newest
   first. Include the full published release notes, not just a summary or link.
   Preserve migration guidance, limitations, examples, contributor attribution,
   and source links. Nest headings beneath the version heading without changing
   code blocks. Include a link to the release and downloads.
3. Update the current release, documentation baseline, version-command example,
   and tagged docs snapshot in `docs/changelog.md`, `docs/introduction.md`, and
   the README documentation section. Keep the release baseline aligned with
   `assets/config/VERSION`. Do not label development docs as an immutable release
   snapshot.

## Backfill release history

Fetch all pages, not only the latest releases:

```bash
gh api --paginate repos/aakarim/OpenLore/releases \
  --jq '.[] | select(.draft == false) | {tag_name, published_at, prerelease, body, html_url}'
```

Include every published release exactly once, with its publication date and full
notes. Label prereleases, exclude drafts, and never invent notes for an unpublished
tag. If a release has no notes, say so explicitly. Historical notes describe that
release, not current behavior; retain that context on the page.

## Verify and integrate with openlore.sh

- Compare the changelog's version sections and dates with the complete GitHub
  release list. Check that notes have not been lost, local links resolve, code
  fences balance, and the current version agrees across documentation entry points.
- Run `git diff --check` and applicable repository checks before publishing.
- The website is in `lix-it/openlore.sh`, with OpenLore as its `go-openlore`
  submodule. Keep canonical Markdown here, not in the website's `site/` directory.
- After the OpenLore commit is published, update the website submodule pin. If a
  PR was squash-merged or rebased, pin the resulting published commit. Check
  `site/src/docs.ts` navigation and the shared docs layout, whose version notice
  reads the pinned `assets/config/VERSION`.
- In the website repository, run `npm --prefix site run build` and
  `npm --prefix site test`; verify `/docs/changelog/`, search, navigation, and the
  version notice. Do not push, merge, or deploy without user authorization.
