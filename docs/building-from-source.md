# Building from source

Most people should install a release instead: see the
[Quickstart](quickstart.md#install). Releases already include the web UI (the
dashboard).

When you build OpenLore yourself, build the web UI **before** the Go binary.
Go embeds whatever is in `assets/dashboard/dist` at build time. If that
directory does not exist, you get a binary without the web UI and
`/dashboard/` returns 404.

## Requirements

- Go 1.26 or later
- Node 24 and npm, needed only while building the web UI

The repository's Nix flake provides both. Run `nix develop` to enter that
environment, or install them yourself.

## Build with the web UI

```bash
git clone https://github.com/aakarim/go-openlore.git
cd go-openlore
nix develop        # optional: provides the pinned Go and Node
make distribution  # builds the web UI, then ./openlore
```

`make distribution` runs these two steps, which you can also run yourself:

```bash
npm --prefix dashboard ci
npm --prefix dashboard run build   # writes assets/dashboard/dist
go build -trimpath -o openlore ./cmd/openlore
```

The binary does not need Node at runtime. Rebuild the web UI after you change
anything in `dashboard/`, because Go embeds the generated files and not the
source.

## Check the build

The startup banner shows whether the web UI was embedded:

```text
  HTTP:       http://localhost:8080
  Dashboard:  http://localhost:8080/dashboard/
```

A build without the web UI prints
`Dashboard:  not included in this build` instead and logs a warning.

## Build without the web UI

`go install`, `go build`, and `make build` on a clean checkout skip the web UI
and do not need Node:

```bash
go install github.com/aakarim/go-openlore/cmd/openlore@latest
```

SSH, MCP, the JSON API, and published file links all work in these builds.
Only the dashboard is missing. To go back to a build without the web UI in a
checkout, delete `assets/dashboard/dist`.

## Embed your docs

Put documentation in `assets/lore/` before you build. Then build the same way
as above, using `make distribution` if you want the web UI. See
[Embed docs in a binary](usage.md#embed-docs-in-a-binary). The
[GitHub Action](usage.md#build-with-the-github-action) builds the web UI for you.

For toolchain pinning and CI details, see
[Dashboard build and distribution](dashboard-build.md).
