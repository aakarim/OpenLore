# Configure the server

OpenLore separates server configuration (`openlore.yml`) from identity, role,
and docset policy (`lore.json`). This page sets up `openlore.yml`; the policy
side is explained in [Auth](auth.md).

## Create `openlore.yml`

Create `openlore.yml` in the directory you run `openlore` from, or pass
`--config`. Every key is listed in the [openlore.yml reference](openlore-yml.md).

An explicitly loaded config file takes precedence over an embedded
`openlore.yml` and replaces it rather than merging with it. If no file is
loaded, OpenLore uses the embedded config when present, then built-in defaults;
command-line flags always win.

```yaml
version: "1"

# Enables verbose logs. Unknown shell commands and parser failures are recorded
# as structured debug events to support command/syntax gap analysis.
debug: false

port: 2222
metrics_port: 3000
http_port: 8080
host_key_path: .ssh/openlore_ed25519
default_cwd: /docs

mcp:
  enabled: true
  path: /mcp

motd: |
  Welcome to Acme Corp docs.
  Type 'tree -L 1 /' to get started.

files:
  allowed:
    - "*.md"
    - "*.txt"
    - "*.yml"
    - "*.json"
  ignore:
    - ".git"
    - "node_modules"
    - ".env"

# Folder rules (see docs/folder-rules.md). `growth` is the default multiplier
# for `max: initial` size rules and must be at least 1. `rules.tokenizer` is
# reserved and rejected at boot until a real tokenizer ships; size/tokens uses
# the built-in estimator.
rules:
  growth: 1.25

# skills_dir: ./skills
# auth_file: ./lore.json
# tls_cert: ./cert.pem
# tls_key: ./key.pem
```

## Analytics

Analytics uses SQLite by default for both aggregation materialisations and the
per-file current-facts cache:

```yaml
analytics:
  enabled: true
  dir: analytics
  aggregations:
    store: sqlite # use file for the legacy materialisation store (no facts cache)
  pipeline:
    enabled: true
```

`analytics.enabled: false` disables the complete analytics application,
including durable event logging. To retain events, metrics, and stored views
while pausing analytics processing, leave analytics enabled and set
`analytics.pipeline.enabled: false`. Re-enabling the pipeline catches the
durable event index up from its checkpoint.

One bounded processor handles both content-fact reconciliation and requested
dashboard views. It executes one expensive unit at a time; dashboard demand is
promoted ahead of routine warming without canceling in-flight work. SQLite
stores file facts, ownership-aware directory totals, durable events, completed
dashboard views, and checkpoints. OpenLore detects external edits by comparing file size and
modification time. An edit that changes neither is picked up at the next full
reconciliation.

## Debug logging

Debug logging can also be enabled with `openlore --debug`. Unknown-command
events include only the command name, not its arguments. Parser-failure events
include a syntax sample capped at 512 bytes and the parser error.

## Distributed tracing

OpenLore can export OpenTelemetry traces over **OTLP/HTTP protobuf** to a
collector or a compatible backend such as Langfuse, Phoenix, or LangSmith.
Export is off unless an endpoint is explicitly configured. This applies to
both the regular server and `openlore mcp` (stdio).

Configure tracing in `openlore.yml`; no environment variables are required:

```yaml
tracing:
  endpoint: http://localhost:4318/v1/traces
  service_name: openlore
  sample_ratio: 0.1
  resource_attributes:
    deployment.environment.name: production
  # For an authenticated backend, set headers in a private config file:
  # headers:
  #   Authorization: "Bearer YOUR_TOKEN"
```

`endpoint` is the full trace URL, including `/v1/traces` or the vendor-specific
path. Set `tracing.enabled: false` to disable export even when an endpoint is
configured. Otherwise, an endpoint opts in; `enabled: true` alone does not
select a default collector. `sample_ratio` controls new root traces (0 to 1)
and honours parent sampling decisions. With no sampler configured, sampling
is parent-based always-on. The binary version is attached as `service.version`.

For credentials, use a private file outside the served content directory,
restrict its permissions (for example, `chmod 600`), and select it with
`openlore --config /etc/openlore/openlore.yml ./docs` or
`openlore mcp --config /etc/openlore/openlore.yml ./docs`. Do not commit or
embed a config containing credentials. The file replaces the embedded config;
it is not an overlay.

Standard `OTEL_*` environment variables remain optional fallbacks. Explicit
file settings take precedence: the file endpoint beats both environment
endpoints, `headers` replaces environment headers (`{}` clears them), and file
resource attributes override matching environment attributes. `service_name`
also overrides `service.name` in resource attributes. Omitted service/sampler
settings use the SDK's environment configuration and defaults. The environment
kill switch `OTEL_SDK_DISABLED=true` always prevents export. Advanced timeout
and TLS certificate environment settings remain supported. Only HTTP protobuf
export is implemented, not gRPC or HTTP/JSON.

### What is traced

- HTTP requests, including authentication failures and invalid requests.
- MCP `tools/call` and `tools/list`, shell invocations, and individual commands,
  including nested `eval`/`source` and `xargs` execution.
- Durations, exit codes, output byte counts, pipeline positions, and invocation
  IDs. Nonzero shell exits are marked as trace errors without changing the
  tool's result or exit-code semantics.

HTTP/MCP callers can send W3C `traceparent` and `tracestate` headers to join an
existing agent trace. For MCP transports without HTTP headers, clients may
send string-valued `traceparent` and `tracestate` entries in request `_meta`,
not tool arguments. A valid HTTP trace context takes precedence. Each request
uses its current parent, including requests within long-lived sessions.
Without a parent, OpenLore starts a new trace. SSH commands produce standalone
invocation traces; SSH does not automatically propagate the caller's trace.

Both the agent and OpenLore must export to the same tracing backend for a
complete agent-to-tool view. Token usage, model cost and model calls belong
to the agent's instrumentation, not OpenLore. Vendor-specific tool/retrieval
presentation may require attribute mapping in a collector or backend.

### Privacy, correlation and reliability

Exported spans omit command source/arguments, search patterns, paths, document
contents, tool output, HTTP URLs/queries, credentials and user identities.
Unknown command/tool names are normalized to `unknown`. There is no content
capture switch in this initial implementation. Trace metadata is not an
authentication or authorization mechanism; only trace context is propagated,
not arbitrary baggage.

Existing analytics and Prometheus metrics remain available independently.
Analytics events recorded with an active trace context persist `trace_id` and
`span_id`. Context-aware JSON logs carry the same IDs; `--debug` adds a
content-free command-completion log. Existing debug syntax samples and local
analytics retain their existing content policy; enabling tracing does not
forward those logs or analytics payloads to the OTLP endpoint.

Spans export asynchronously through a bounded queue. When a collector is
unavailable or the queue fills, serving continues and telemetry may be lost.
Normal server shutdown and stdio EOF attempt a flush with a five-second
deadline. Initialization/export failures emit content-free warnings.

Embedded Go applications retain ownership of their telemetry: configure an
OpenTelemetry provider with `otel.SetTracerProvider` before serving, and flush
or shut it down in the host application. OpenLore's library constructors never
install or shut down a provider or read exporter environment configuration.

## Next steps

- [Auth](auth.md) covers `lore.json`: authentication posture, roles, docsets,
  identities and credentials.
- [openlore.yml reference](openlore-yml.md) documents every key, including
  `tokens`, `passkeys`, `inbox` and `analytics`.
- [Dashboard and metrics](dashboard.md) explains what the analytics pipeline
  feeds.
