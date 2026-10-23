# Embedding Documentation

OpenLore uses Go's `embed` package to bake your documentation into a single binary.

## Why Embed?

- **Single binary distribution** — no external files needed
- **Agent-friendly** — agents can spin up their own documentation server
- **Knowledge sharing** — distribute lore as easily as distributing a binary
- **Version control** — each binary contains a specific version of docs

## How It Works

1. Place your docs in `assets/lore/`:
   ```
   assets/lore/
   ├── api/
   │   ├── endpoints.md
   │   └── authentication.md
   ├── guides/
   │   └── getting-started.md
   └── README.md
   ```

2. Build:
   ```shell
   go build -o my-docs-server ./cmd/openlore
   ```

3. The binary contains everything. Run it anywhere:
   ```shell
   ./my-docs-server
   # SSH server starts, serving embedded docs
   ```

## Using the GitHub Action

Automate builds with the OpenLore GitHub Action:

```yaml
- uses: aakarim/openlore@v1
  with:
    docs-dir: ./docs
    config: ./openlore.yml
```

## Exporting

Extract docs from an existing binary:

```shell
openlore export -o ./extracted-docs
```
