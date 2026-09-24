# Dashboard build and distribution

The dashboard source lives in `dashboard/`. Its generated files are build
artifacts and are not committed.

## Toolchain and build contract

The checked-in Nix flake pins Node for every frontend build: local development,
CI, releases, containers, and the distribution GitHub Action. It also pins Go
for local development, CI, and release binaries; Railpack keeps its Go provider.
Enter that environment with:

```bash
nix develop
```

The frontend build contract is:

```bash
npm --prefix dashboard ci
npm --prefix dashboard run build
```

It writes the production assets to `assets/dashboard/dist`. `make
dashboard-build` runs the same commands. `make dashboard-ci` additionally runs
the frontend checks and tests.

## Go builds

On a clean checkout, `go build`, `go install`, and `make build` are backend-only
workflows. They work without generated frontend files, and `assets.Dashboard()`
returns `nil` in such builds. If `assets/dashboard/dist` already exists from a
frontend build, Go embeds it automatically; rebuild it after changing frontend
source. Remove that generated directory to return to a backend-only build.

Use `make distribution` from `nix develop` to build the dashboard and then an
OpenLore binary with those files embedded. CI release binaries, containers, and
the GitHub Action use this same ordering. Node and npm are build-time tools only;
the resulting binary and container have no Node runtime dependency.

For authentication, permissions, telemetry coverage, and viewer behaviour, see
[Dashboard](dashboard.md).
