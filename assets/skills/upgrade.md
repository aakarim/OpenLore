# Upgrade an OpenLore Project

Prepare a reviewable OpenLore base-image version change. Do not build, test,
commit, push, or deploy it.

## Preconditions

1. The current directory must contain root `openlore.yml`. That file alone
   identifies an OpenLore project; stop otherwise.
2. Require a root `Containerfile` for this operation. If it is absent, explain
   that this project has no container base version to upgrade.
3. Inspect Git status and preserve every unrelated or user-modified change.

## Resolve the change

Find exactly one OpenLore base line of this form:

```dockerfile
FROM ghcr.io/aakarim/openlore:1.2.3
```

If the reference is `:latest`, report that the project already follows the
floating latest channel and make no change. If it is an exact digest, branch,
range, build argument, or otherwise ambiguous, stop and ask rather than
rewriting it heuristically.

When the user requests a version, verify that the corresponding public GHCR
container tag exists. Otherwise look up the latest stable semantic tag that
actually exists on `ghcr.io/aakarim/openlore`, excluding prereleases. Do not
infer image availability from GitHub releases alone. Compare semantic versions,
not strings.

## Prepare only

Change only the tag on the OpenLore `FROM` line. Preserve every custom package,
copy, environment, label, and command in the Containerfile. Then:

1. show old and new versions;
2. show the exact diff;
3. report release notes or the release URL when available;
4. mention that the user's existing CD may deploy after commit.

Do not edit `openlore.yml`: its `version: "1"` is the configuration schema, not
the OpenLore release. Do not modify files under `.local` or `deploy`. Do not
offer an automatic deployment as part of this skill. Leave the prepared change
for the user to review and commit.
