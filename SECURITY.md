# Security Evaluation

This document describes the security properties of OpenLore and the design decisions behind them.

## Threat Model

OpenLore is a **primarily read-only SSH server** that serves a virtual filesystem. The optional `publish` command provides controlled write access to configured docsets. It is designed to be safe to expose on internal networks and, with public key auth enabled, on the public internet. The primary threat actors are:

1. **Malicious SSH clients** attempting to escape the sandbox
2. **Agents or users** attempting to access files outside their allowed scope
3. **Denial-of-service** attacks against the SSH server

## Architecture Layers

### 1. Command Parser (`splitArgs`)

Commands are parsed using a structural tokenizer, not by passing strings to a shell. There is no shell interpolation, no backtick expansion, no `$(...)` substitution, and no glob expansion by the parser.

**No shell injection is possible** because:
- Commands are tokenized into `[]string` arguments
- Each command name is matched against a fixed allowlist of built-in functions
- Unknown commands return an error — they are never passed to `os/exec` or any system shell
- Pipe chains are handled by splitting on `|` and connecting in-memory buffers

### 2. Virtual Filesystem (VFS)

The VFS layer provides a read-only view over an `fs.FS` (either `os.DirFS` or `embed.FS`).

**Path traversal protection:**
- All paths are processed through `path.Clean` to normalize `.` and `..` components
- Resolved paths are checked to ensure they remain within the VFS root
- Symlinks pointing outside the root are not followed

**Read-only enforcement:**
- The VFS interface only exposes `Open`, `ReadDir`, and `Stat`
- No write, rename, delete, or permission-change operations exist in the interface
- File handles returned are read-only
- The `publish` command is the sole exception: it writes to a configured `publish_dir` on disk, not to the VFS directly
- Write access is opt-in per docset via `publish_dir` in `lore.json` — by default, no docsets are writable

**File filtering:**
- Allowed patterns (e.g., `*.md`, `*.txt`) restrict which files are visible
- Ignore patterns (e.g., `.git`, `.env`) hide directories and files entirely
- Filtering is applied at the VFS level — filtered files don't appear in `ls`, `find`, `tree`, or `cat`

**HTML, CSS, and JavaScript in default allowed patterns:**
- The default allowed patterns include `*.html`, `*.htm`, `*.css`, and `*.js` to support serving rich documentation (e.g., generated API docs, interactive diagrams, single-page doc sites)
- Over SSH, this is low-risk — agents receive raw file content as text, not rendered HTML
- Over the HTTP lore browser (passkeys), HTML files are rendered by the browser. Since publishing requires SSH authentication and only trusted identities should have access (`unknown_identity: "deny"`), the content itself is trusted
- The primary risk is **MITM on the HTTP side**: if the lore browser is served over plain HTTP, an attacker could modify HTML/JS in transit to inject malicious scripts. The SSH upload path is encrypted and not vulnerable
- **Mitigation:** Serve the HTTP lore browser over TLS (`tls_cert` / `tls_key`) to prevent tampering in transit

### 3. SSH Transport

SSH transport is handled by [charmbracelet/ssh](https://github.com/charmbracelet/ssh), which wraps [golang.org/x/crypto/ssh](https://pkg.go.dev/golang.org/x/crypto/ssh).

**Properties:**
- Standard SSH key exchange and encryption (curve25519-sha256, aes256-gcm, etc.)
- Host key identity via ed25519 key (auto-generated on first run)
- Public key authentication validated by `golang.org/x/crypto/ssh` key parsing
- No password authentication (even in keyless mode — the server accepts all keys, it doesn't use passwords)

### 4. SFTP Subsystem

The SFTP server provides read-only file access for `sshfs` mounting.

**Read-only enforcement:**
- Write operations (`Create`, `Remove`, `Rename`, `Mkdir`, `Chmod`, etc.) return `os.ErrPermission`
- Only `Open` (read), `ReadDir`, and `Stat` are functional
- The SFTP handler wraps the same VFS used by the shell, so file filtering and path protection apply

### 5. In-Memory Bash Interpreter

The bash interpreter is **not** bash. It is a set of Go functions that implement common read-only Unix commands:

| Command | Implementation |
|---------|---------------|
| `ls`    | Calls `fs.ReadDir` on the VFS |
| `cat`   | Calls `fs.Open` and reads the file |
| `grep`  | Compiles a `regexp.Regexp` and scans lines |
| `find`  | Walks the VFS with `fs.WalkDir` |
| `tree`  | Recursive `fs.ReadDir` with formatting |
| `head`/`tail` | Reads lines from a file buffer |
| `wc`    | Counts bytes/lines in a buffer |
| `stat`  | Calls `fs.Stat` on the VFS |
| `cd`/`pwd` | Tracks working directory in session state |

**No process execution:**
- No use of `os/exec`, `syscall.Exec`, or any process spawning
- `bash`, `sh`, `/bin/sh`, `exec`, and similar commands are not recognized
- Environment variables are not expanded (no `$HOME`, `$PATH`, etc.)

### 6. Identity & Auth

**Keyless mode (default):**
- All SSH connections are accepted regardless of key
- The user field from the SSH session is available but not enforced
- Suitable for local development and trusted networks

**Public key mode:**
- Public keys in `auth.json` are matched against the client's presented key
- Per-identity folder restrictions limit which VFS paths are accessible
- Keys are validated by `golang.org/x/crypto/ssh.ParseAuthorizedKey`
- Unknown keys are rejected at the SSH handshake level

**Certificate authority mode (user certificates):**
- When `ca_keys_file` is configured, the server accepts SSH user certificates signed by trusted CA keys
- Analogous to OpenSSH's `TrustedUserCAKeys` directive
- The CA's public key is stored on the server; users present certificates signed by that CA
- Certificate validity (expiry, principals) is enforced by `golang.org/x/crypto/ssh`
- Can be combined with public key auth — certificates and raw keys are both accepted

**Host certificates (server identity):**
- When `host_cert_file` is configured, the server presents a CA-signed host certificate to clients
- Analogous to OpenSSH's `HostCertificate` directive
- Clients configure `@cert-authority` in `known_hosts` to trust all hosts signed by the CA
- **Limitation: still relies on TOFU for CA trust distribution.** The client must obtain and configure the CA public key *before* the first connection. If an attacker intercepts the first connection before the client has the CA key, they can present a fraudulent server. SSH has no equivalent of the TLS WebPKI or Certificate Transparency — there is no global, independently-auditable registry of host CA keys. In practice, this means:
  - The CA public key must be distributed through a trusted out-of-band channel (e.g., internal wiki, config management, signed package, or a well-known HTTPS endpoint)
  - Publishing the CA public key at a stable, publicly-accessible HTTPS URL (e.g., `https://example.com/.well-known/ssh-ca.pub`) and documenting it in your README is strongly recommended. HTTPS provides the independent trust anchor that SSH lacks
  - For automated agent deployments, bake the CA public key into the agent's image or configuration rather than relying on first-connection trust

### 7. Passkeys / WebAuthn (HTTP Browser Access)

Passkeys provide browser-based access to documentation for humans, using the WebAuthn standard (Face ID, Touch ID, security keys).

**Registration flow:**
- Registration is initiated from the SSH shell (`passkey register`), which creates a one-time token stored in memory
- The token is embedded in a URL (e.g., `/passkey/r/{token}`) with a 5-minute TTL
- The URL serves a page that calls `navigator.credentials.create()` — the WebAuthn browser API
- The server validates the attestation using [go-webauthn/webauthn](https://github.com/go-webauthn/webauthn), a FIDO2-conformant library
- On success, the credential (public key, credential ID, sign count) is persisted to a JSON file on disk

**Authentication flow:**
- Uses WebAuthn discoverable credentials (resident keys) — no username required
- The server generates a random challenge via `BeginDiscoverableLogin()`
- The browser calls `navigator.credentials.get()` and the authenticator signs the challenge
- The server verifies the signature against stored public keys and checks the sign count to detect cloned authenticators
- On success, an HMAC-signed session cookie is set

**Session cookies:**
- Payload: `lore_name:expiry_unix`, signed with HMAC-SHA256
- The HMAC key is derived from the SSH host key material via `SHA-256("openlore-passkey-session:" || host_key_bytes)` — this means sessions are invalidated if the host key changes
- The key derivation ensures raw host key material is never used directly as an HMAC key
- Cookies are `HttpOnly`, `SameSite=Lax`, and have an expiry matching the configured session TTL (default 24h)
- No server-side session state — validation is purely cryptographic

**Credential storage:**
- Credentials are stored in a plain JSON file (`./config/passkeys.json`) with `0600` permissions
- The file contains WebAuthn credential data (public keys, credential IDs, sign counts) — no secrets
- Designed for agent editability: an agent can manage passkeys by reading/writing this file

**Pending registrations:**
- Stored in-memory only (a `sync.Map` with TTL) — not persisted across restarts
- Expired tokens are garbage-collected every 60 seconds
- Each token is 32 bytes of `crypto/rand` output, hex-encoded (256 bits of entropy)

**WebAuthn security properties:**
- Requires a secure context: `https://` or `localhost` (enforced by browsers, not by OpenLore)
- The Relying Party ID (`rp_id`) must match the domain the user visits — browsers enforce this
- `RPOrigins` must be explicitly configured and match the HTTP server's actual origin
- Resident key requirement is set to `Required`, ensuring discoverable credentials for usernameless login
- Attestation is validated but not filtered by format (any authenticator is accepted)

**Threat considerations:**

| Threat | Mitigation |
|--------|-----------|
| Registration link interception | Tokens expire in 5 minutes; one-time use; should be served over TLS |
| Session cookie theft | `HttpOnly` prevents JS access; `SameSite=Lax` prevents CSRF; TLS prevents network sniffing |
| Credential file tampering | File has `0600` permissions; an attacker with disk write access could add credentials, but they'd need a corresponding authenticator private key to actually authenticate |
| Replay attacks | WebAuthn challenges are single-use random values; sign count tracking detects cloned authenticators |
| Phishing | WebAuthn is origin-bound — the browser will not sign challenges for a different domain |

**Limitations:**
- No built-in CSRF protection on the WebAuthn API endpoints beyond `SameSite` cookies and WebAuthn's own origin checking
- The login session cookie (used during the begin/finish ceremony) is stored in server memory — high concurrency could grow this map. Entries expire after 5 minutes.
- No revocation propagation — revoking a passkey from `passkeys.json` takes effect on the next authentication attempt, but existing session cookies remain valid until they expire

### 8. Docsets & Lore (Access Scoping)

The access control model separates **docsets** (atomic document collections with path lists) from **lore** (named compositions of docsets). Each identity — whether an SSH key or a passkey — references a lore name.

**Path isolation:**
- When a lore resolves to multiple docsets, each docset is served as a separate subdirectory in the HTTP browser (e.g., `/lore/backend/`, `/lore/frontend/`)
- Path traversal between docsets is not possible because each docset's VFS paths are resolved independently against the server's root filesystem
- The VFS path cleaning (`path.Clean`) applies before docset resolution

**Naming:** Docset names and lore names occupy separate namespaces in `lore.json`, so naming conflicts are not possible.

### 9. Denial of Service

OpenLore does **not** include built-in rate limiting or connection limits. This is by design — these concerns are better handled at the infrastructure level:

- Use a reverse proxy or load balancer for connection rate limiting
- Use OS-level limits (`ulimit`, `systemd` resource controls) for per-process constraints
- Use firewall rules to restrict source IPs

**Resource considerations:**
- Each session maintains an in-memory working directory path (minimal memory)
- File reads are streaming (not buffered entirely in memory)
- `grep -r` and `find` on large filesystems will consume CPU proportional to the number of files
- The VFS does not cache file contents — each read goes to disk (or embed.FS)

### 10. Publish Command (Controlled Writes)

The `publish` command takes two arguments (`<docset>` and `<path>`), reads content from stdin, and writes it to a directory on disk.

**Write scope:**
- Publishing is disabled by default — docsets must explicitly set `publish_dir` to enable writes
- Each writable docset maps to a single directory on disk
- The `publish` command is the only write path in the system; no other command can create, modify, or delete files

**Path traversal protection:**
- Input paths are cleaned with `path.Clean("/" + path)` and `..` segments are rejected
- The resolved disk path is checked with `filepath.Abs` to ensure it remains within the target `publish_dir`
- Both the VFS-level cleaning and the disk-level prefix check apply (defense in depth)

**Content handling:**
- Content is read entirely from stdin before writing (no streaming writes)
- Parent directories are created with `os.MkdirAll(dir, 0755)`
- Files are written with `os.WriteFile(path, content, 0644)`
- No content validation is performed — the `publish` command writes whatever is piped to it. File type filtering (allowed/denied patterns) applies when the file is *served*, not when it is written

**Identity scoping:**
- Publish targets are registered at server startup from the auth config
- Currently, any connected identity can publish to any writable docset. Per-identity write scoping is not yet implemented — use network-level controls and `unknown_identity: "deny"` to restrict who can connect.

**Threat considerations:**

| Threat | Mitigation |
|--------|-----------|
| Path traversal | `path.Clean` + `..` rejection + `filepath.Abs` prefix check |
| Disk exhaustion | Per-docset `max_publish_size` (default 2.5MB) limits individual writes. Use OS-level disk quotas for aggregate limits |
| MITM tampering with served HTML | If the HTTP lore browser is not TLS-secured, an attacker could modify HTML/JS in transit. Serve over TLS |
| Overwriting existing files | Allowed by design — `os.WriteFile` overwrites |
| Writing executable content | Files are only served through the VFS with file type filtering. The server does not execute uploaded files |
| Unauthorized writes | Requires SSH authentication. Use `unknown_identity: "deny"` to restrict connections |

### 11. Remote Skill Imports (Outbound HTTPS)

`skills import` and the Agent Skills sync path are the only features that make outbound network requests. They fetch public git repositories (ref advertisements and source archives) from GitHub, GitLab, Bitbucket, Codeberg, and self-hosted GitLab/Gitea/Forgejo instances.

**SSRF protection (egress policy):**
- All requests use a restricted HTTP client (`NewPublicHTTPClient`) whose dialer resolves DNS and connects only to public IP addresses — loopback, RFC 1918 private, link-local, CGNAT, and other special-purpose ranges are rejected
- The vetted IP is dialed directly, so a DNS rebind between check and connect has no effect; TLS server name verification still applies
- Redirects are followed only to HTTPS URLs, and every hop passes through the same public-IP dialer
- Only `https://` repository URLs are accepted; userinfo, query strings, and fragments are rejected during spec parsing

**Content handling:**
- No credentials are ever sent — only public repositories are reachable
- Archives are bounded by compressed-size, decompressed-size, file-count, and entry-count limits before extraction
- Archive entries are checked for path traversal, duplicates, and file/directory conflicts; symlinks and hard links are skipped
- Fetched `SKILL.md` content is normalized and fully validated before anything is written to a collection; advertised ref object IDs must be 40-hex SHAs

**Threat considerations:**

| Threat | Mitigation |
|--------|-----------|
| SSRF against internal services | Public-IP-only dialer applied to every connection and redirect hop |
| Redirect to internal host or plain HTTP | `CheckRedirect` enforces HTTPS; dialer re-applies IP policy per hop |
| Decompression bombs | Compressed and decompressed size limits, entry scan limits |
| Malicious archive paths | Path cleaning, traversal rejection, duplicate detection, symlink/hardlink skipping |
| Malicious skill content | Frontmatter normalization and strict validation before any write; skills are data, never executed by OpenLore |

## Known Limitations

1. **No rate limiting** — must be handled externally
2. **No TLS termination** — not needed, SSH provides encryption
3. **No audit logging** — connection events are logged via slog, but no detailed command audit trail is built in (use the `OnConnect` hook in library mode for custom auditing)
4. **Symlink handling** — symlinks within the served directory are followed; symlinks pointing outside are not. A determined attacker with control over the served directory could create symlinks to sensitive files. Only serve directories you trust.
5. **Large file reads** — `cat` on a very large file will stream the entire content. There is no built-in size limit. Consider using `head` for large files.
6. **No built-in CA key distribution** — SSH host certificates shift trust from individual host keys to a CA, but clients must still obtain the CA public key out-of-band before the first connection. Unlike TLS (where browsers ship with trusted root CAs), SSH has no pre-installed trust store. Publish your CA public key at a well-known HTTPS URL so clients can fetch and verify it independently.
7. **Passkey session cookies are not revocable** — once issued, a session cookie is valid until its TTL expires. Revoking a passkey from `passkeys.json` prevents new logins but does not invalidate existing sessions. Set a shorter `session_ttl` if this is a concern.
8. **WebAuthn requires secure context** — passkey registration and login only work over `https://` or `localhost`. If you expose the HTTP server without TLS on a non-localhost address, browsers will refuse to call the WebAuthn API.
9. **In-memory login sessions** — the WebAuthn begin/finish ceremony state is held in a Go map. Under extreme concurrent login load this could grow; entries auto-expire after 5 minutes.
10. **No per-identity publish scoping** — any authenticated user can publish to any writable docset. If you need identity-level write restrictions, use separate server instances.
11. **No aggregate publish quota** — individual files are capped by `max_publish_size` (default 2.5MB), but there is no limit on the total number or aggregate size of published files. Use OS-level disk quotas for untrusted environments.
12. **No content validation** — the `publish` command writes any content. Malicious content will be served as-is if it matches the allowed file patterns.
13. **No repository host allowlist** — `skills import` can fetch from any public HTTPS host. The egress policy blocks internal addresses, but if you need to restrict which forges are reachable, use network-level egress filtering. Conversely, repositories on private networks (e.g., internal GitHub Enterprise) are unreachable by design.

## Recommendations

- **Enable public key auth** in production: set `allow_keyless: false` and configure `lore.json`
- **Use file filtering** to avoid serving sensitive file types
- **Use ignore patterns** to exclude `.git`, `.env`, `node_modules`, and other non-doc content
- **Run behind a firewall** or VPN for internal documentation servers
- **Use `go:embed`** for public-facing docs to eliminate filesystem access entirely
- **Publish your SSH CA public key over HTTPS** if using host certificates. Host it at a stable URL (e.g., `https://example.com/.well-known/ssh-ca.pub`) so clients can verify the CA independently before their first SSH connection. This closes the TOFU gap that SSH certificates alone cannot solve.
- **Serve the HTTP server over TLS** when using passkeys in production (`tls_cert` / `tls_key` in `openlore.yml`). WebAuthn requires a secure context, and TLS protects registration URLs and session cookies in transit.
- **Use short session TTLs** for passkeys in high-security environments. Since session cookies are not server-side revocable, a shorter TTL (e.g., `1h`) limits the window of exposure if a cookie is compromised.
- **Use docsets to scope passkey access** — give each passkey the narrowest lore collection needed rather than `full-access`.
- **Only enable `publish_dir` on docsets that should be writable** — leave it unset on read-only documentation collections.
- **Set `unknown_identity: "deny"`** when `publish_dir` is enabled to ensure only known identities can write.
- **Serve the lore browser over TLS** when serving HTML/JS content to prevent MITM tampering in transit.
- **Review imported skills before enabling agents on them** — skill files are validated structurally, not semantically. Treat third-party skills like code: read `SKILL.md` and any referenced scripts, and prefer pinning imports to a tag or commit SHA over tracking a branch.
