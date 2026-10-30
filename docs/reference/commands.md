# openlore CLI reference

`openlore` is the server binary: it serves a knowledge repository, administers identities, roles and tokens, and provides the shell that agents use. The separate `lore` client that syncs external sources has its own [lore CLI reference](lore-cli.md).

## Server binary

Start a server or run an administrative subcommand:

```text
Usage: openlore [command] [flags] [directory]

Commands:
  version           Print version
  export -o <dir>   Export embedded docs to a directory
  mcp [dir]         Run an MCP server over stdio
  mcpb [-o file]    Package the binary as an MCPB desktop extension
  identity add      Add an identity or public key to lore.json
  identity role     Add or remove an identity's role membership
  role              Manage roles, docset grants, denies, and capabilities
  oauth keys rotate Rotate the ES256 token signing key
  token mint        Mint an access token for an identity
  token verify      Verify an access token and print its claims
  inbox token       Create, list, or revoke HTTP inbox upload credentials

Flags:
  -p, --port           SSH server port (default 2222)
  --http-port          HTTP port (default 8080, 0 to disable)
  --mcp-path           MCP-over-HTTP path (default /mcp)
  --metrics-port       Prometheus metrics port (default 3000, 0 to disable)
  --host-key           Host key path (default .ssh/openlore_ed25519)
  --motd               Inline MOTD
  --motd-file          MOTD file
  --auth               Path to lore.json
  -c, --config         Config path (default ./openlore.yml)
  --allowed            Comma-separated allowed file patterns
  --ignore             Comma-separated ignored patterns
  --tls-cert           HTTP TLS certificate
  --tls-key            HTTP TLS key
  --ca-keys            Trusted CA public keys for SSH certificate auth
  --host-cert          CA-signed SSH host certificate
  --skills-dir         Runtime skills directory
  --readonly           Global write lock (default true; use --readonly=false to enable writes)
  --debug              Enable debug-level server logging
```

Run `openlore --help` or a subcommand with `--help` for its current flags.

---

## Agent shell

OpenLore implements a bash-like command language over its virtual filesystem.
Run `help` in a session for the command list available on that server.

Interactive SSH sessions support Tab completion for permitted command names,
registered `lore` subcommands, and virtual filesystem paths. The first Tab
extends a shared prefix; a second Tab lists matching candidates in columns.
Directories retain a trailing `/`, hidden entries appear only after an explicit
`.`, and candidate layout follows terminal resize events.

## Filesystem

| Command | Description |
|---|---|
| `ls` | List directory contents (`-l`, `-a`, `-R`, `-S`, `-t`, `-F`, `-1`, `-h`, `-d`) |
| `cat` | Display file contents (`-n`, `-A`) |
| `head` | First N lines or bytes (`-n N`, `-c N`) |
| `tail` | Last N lines or bytes (`-n N`, `-c N`, `+N`) |
| `tree` | Directory tree visualisation (`-L depth`, `-a`, `-d`, `-f`) |
| `stat` | File metadata |
| `wc` | Count lines, words, bytes (`-l`, `-w`, `-c`, `-m`) |
| `du` | Estimate file space usage (`-a`, `-h`, `-s`, `-c`) |
| `diff` | Compare two files (`-u`, `-q`) |
| `cd` / `pwd` | Navigate the virtual filesystem |

## Search

| Command | Description |
|---|---|
| `grep` | Search patterns (`-E`, `-F`, `-i`, `-n`, `-r`, `-R`, `-o`, `-h`, `-c`, `-v`, `-l`); use `-E 'a|b'` for multiple patterns |
| `find` | Find files (`-name`, `-type f\|d\|l`). Alias docsets are links: `find` lists each file once under its canonical path; add `-L` to also walk aliases |

## Text processing

| Command | Description |
|---|---|
| `sort` | Sort lines (`-r`, `-n`, `-u`, `-f`, `-k N`, `-t SEP`) |
| `uniq` | Filter duplicate lines (`-c`, `-d`, `-i`, `-u`) |
| `cut` | Select fields or characters (`-d DEL`, `-f FIELDS`, `-c CHARS`, `-s`) |
| `sed` | Stream editor (`-n`, `-e`, `s/pat/repl/flags`, `-i`); replacement text supports `\n` (newline), `\t` (tab), `\\` (backslash), escaped delimiters, and backslash-newline continuations |
| `awk` | Pattern scanning (`-F SEP`, `-v VAR=VAL`); supports regex and expression patterns, variables, arrays, `if`/`for`/`while`, `next`, and common built-ins such as `sub`, `gsub`, `split`, and `substr`. Unsupported constructs (`nextfile`, `exit`, `getline`, output redirection, range patterns, user functions) print an error and exit 2 |
| `tr` | Translate characters (`-d`, `-s`, `-c`) |
| `rev` / `tac` | Reverse characters per line / reverse line order |
| `nl` | Number lines (`-b`, `-n`, `-w`, `-s`) |
| `fold` | Wrap lines (`-w N`, `-s`) |
| `paste` | Merge file lines (`-d DEL`, `-s`) |
| `column` | Columnate lists (`-t`, `-s SEP`) |
| `join` | Join sorted files (`-1`, `-2`, `-t`) |
| `comm` | Compare sorted files (`-1`, `-2`, `-3`) |
| `expand` / `unexpand` | Convert tabs and spaces (`-t N`, `-a`) |

## Data and utilities

| Command | Description |
|---|---|
| `jq` | JSON processing (`-r`, `-c`, `-e`, `-s`, `select`, `map`, `sort_by`, `add`, `length`, and more) |
| `xargs` | Build commands from stdin (`-I REPL`, `-d DEL`, `-n N`, `-0`) |
| `seq` | Print a number sequence (`-s SEP`, `-w`) |
| `printf` | Format and print data |
| `date` | Display date/time (`-u`, `+FORMAT`) |
| `basename` / `dirname` | Strip directory or filename components |
| `tee` | Pass stdin through to stdout or a writable file |
| `base64` | Base64 encode/decode (`-d`) |
| `md5sum` / `sha1sum` / `sha256sum` | Compute checksums (`-c`) |
| `expr` | Evaluate arithmetic expressions |
| `which` / `type` | Locate or identify a command |
| `time` / `timeout` | Time a command or run with a timeout |
| `whoami` / `hostname` | Print identity or host information |
| `true` / `false` | Exit with status 0 / 1 |
| `sleep` / `clear` | Sleep (stub) / clear screen |
| `command` | Run or locate a command (`-v`) |
| `version` | Print the OpenLore version |

## Shell builtins

| Command | Description |
|---|---|
| `echo` | Print text (`-n`, `-e`) |
| `export` / `unset` | Set or remove variables |
| `env` / `printenv` | Print the environment |
| `set` | Set or list shell variables (`--`) |
| `test` / `[` / `[[` | Conditional tests |
| `read` | Read stdin (`-p`, `-r`, `-a`, `-d`, `-n`) |
| `source` / `.` | Execute commands from a VFS file |
| `eval` | Evaluate a command string |
| `help` | Show available commands |
| `skills` | List available skill commands |
| `exit` / `quit` | Close the session |

## Writes and publishing

These commands are available only where configuration and identity grants permit
them.

| Command | Description |
|---|---|
| `write` / `>` / `>>` | Overwrite or append to a file |
| `tee` | Write stdin to a file while returning it |
| `patch` | Apply a unified diff atomically |
| `sed -i` | Edit a file atomically |
| `mkdir` | Create a directory (`-p` for parents) |
| `mv` | Move a file |
| `rm` | Remove a file or tree (`-r`) |
| `publish` | Contribute stdin into a docset inbox; does not make the docset public or change who can read it |
| `spawn` | Run configured external work asynchronously when explicitly trusted |

The `publish` grant is contributor inbox-write access, not public read access.
To serve documents publicly, [grant `guest` `ro` on their docset](../concepts/auth.md#serving-public-and-internal-docs-from-one-corpus).

## Introspection

`lore` is the dispatcher for the commands below. Run it with no arguments to list them.

### `lore docsets`

List the docsets you can access with their grants, paths and attributes.

```shell
lore docsets
```

### `lore sources`

List the docstore stores you can read, the docsets that project them and their latest snapshots. See the [knowledge gateway](../concepts/knowledge-gateway.md).

```shell
lore sources
```

> **Note:** This runs in the agent shell. The `lore source list` command of the [lore client](lore-cli.md) lists the sources defined in your knowledge repository.

### `lore meta [path]`

Emit document frontmatter as NDJSON, scoped to the current directory or the given path. Pipe it into `jq` to query it.

```shell
lore meta /docs/backend | jq -r 'select(.type == "Metric") | .path'
```

Add `--filter skills` to list Agent Skills collections without scanning unrelated documents.

### `lore package list` and `lore package doc <path>`

List the compiled-in rule members, or show one member's parameters and example. The same content is in the [rules standard library](rules-stdlib.md).

```shell
lore package doc size/kilobytes
```

### `lore validate [dir]`

Run folder rules over a docset or folder without writing anything. File rules run per file; bundle rules such as OKF bundle structure, local links and alias portability run once at the root. Bundle rules never run on write, so a file that saved successfully can still fail here; see [Saved is not validated](../concepts/writing.md#saved-is-not-validated).

```shell
lore validate /docs/backend/decisions
```

Exit status is 1 when any `error` finding is reported and 0 when there are only warnings or none. Name the docset root: links to files outside the named folder are reported as `openlore/link-outside-bundle`.

### `lore size baseline <path>`

Show a file's size-baseline history and its current cap under `max: initial` rules.

```shell
lore size baseline /docs/backend/decisions/adr-001.md
```

### `lore size baseline reset <path> [--note <text>]`

Append a new baseline from the file's current content. Needs a write grant on the path and a role listed in the docset's `config.edit`.

```shell
lore size baseline reset /docs/backend/decisions/adr-001.md --note "split into two ADRs"
```

See [Folder rules](../concepts/folder-rules.md) for the rules model. `lore validate` refuses
to run from a directory above docsets that declare bundle rules
(`run lore validate per docset`); name the docset or folder instead.

Example `lore size baseline` output:

```text
/docs/backend/decisions/adr-001.md  (size/lines via adr-length @ /docs/backend/.lore/config.yaml, growth 1.1)
  2026-08-30T09:12:04Z  create      agent:claude@acme.com   412 lines  12 KiB  3900 tokens (estimate/v1)
  2026-09-02T14:01:37Z  reset       adil                    812 lines  21 KiB  6400 tokens (estimate/v1)  "ADR grew after review"
current cap: 893 lines
```

A file with no history prints `no baseline recorded`. A reset prints the
previous and new baseline and the resulting cap, and is also recorded in the
server audit log as `rules.baseline.reset`:

```text
baseline reset: previous 412, new 812, new cap 893 lines
```

### `history [--principal <name>] [--actor <name>]`

Print the commit journal as NDJSON, newest first, with one object per write to a docset you can read. `--principal` keeps writes by one identity and `--actor` keeps writes made through one delegated client.

```shell
history --actor claude-code@claude.ai | jq -r .target | sort | uniq -c | sort -rn
```

Each object has `commit_id`, `time`, `attribution`, `target`, `action` and `hash`. `attribution` is the principal, or `principal/actor` for a write made through an OAuth client. The journal is kept only when `readonly` is `false`; `analytics.history.retention` bounds how far back it reaches.

## Skills management

These commands require `plugins.skills.enabled: true`. Mutating commands also
require a named `rw` grant on the destination docset.

Run `lore config plugins` to inspect registered plugin names and versions (the
Skills plugin is listed as `agent_skills`). This read-only view does not require
configuration-edit permission. Failed Skills operations return non-zero and
include an actionable `message` in their NDJSON result. In particular, failure
to read collection markers is an error, not a successful `no_collections`
validation; an accessible scope with no enabled collections still warns.

| Command | Description |
|---|---|
| `skills status [folder]` | Report collection state, linked remotes, gateway snapshot links, detach ancestry and rejected updates as NDJSON |
| `skills enable [folder]` | Enable recursive Skills collection behaviour |
| `skills disable [folder]` | Disable collection behaviour without deleting skills |
| `skills validate [scope]` | Validate accessible Skills collections |
| `skills import <github-spec> [parent-dir]` | Import and link a public GitHub skill |
| `skills update [skill-folder]` | Force an imported skill to check and apply its remote |
| `skills remove-remote [skill-folder]` | Keep imported files and derived-from ancestry, stop tracking upstream and allow local edits |

Run bare `skills` for GitHub spec syntax, automatic branch tracking, pinning,
and collection recovery.

Imports and refreshes retain unmodified public upstream files in the
[knowledge gateway](../concepts/knowledge-gateway.md#imported-skills), separate
from each installation's normalized local copy. Installations advance
independently; a rejected update keeps serving the last valid copy. Startup
migrates existing links locally without fetching or changing installed files.
Detaching or deleting an installation does not delete source history.

Discover installed skills with the `skills` metadata filter, then read the
chosen `SKILL.md`:

```shell
lore meta --filter skills | jq -r 'select((.name + " " + .description) | test("pdf"; "i")) | .path'
```

Example `lore docsets` output:

```text
DOCSET    GRANTS      ATTRIBUTES   PATH             TARGET
public    ro          -            /docs/public     -
backend   publish,rw  inbox        /docs/backend    -
home      rw          home         /home/backend    -
home      rw          alias        /backend         /home/backend
```

`GRANTS` contains `ro`, `publish`, `rw`, or plugin-defined grants.
Here `publish` means read access plus create/edit access inside the docset's
inbox, never delete; it does not expose the docset to other readers.
`ATTRIBUTES` may include `home`, `inbox`, and `alias`; aliases point to their
canonical path in `TARGET`. `ls -l` and `stat` show an alias as a link
(`backend -> /home/backend`), and `find` does not descend into it unless you
pass `-L`.

## Shell syntax

| Feature | Example |
|---|---|
| Pipes | `grep pattern file \| sort \| head -5` |
| Redirections | `>`, `>>`, `2>`, `2>>`, `2>&1`, and `<<'EOF'` heredocs are supported. Input redirection (`< file`) is rejected before execution; use `cat file \| cmd` instead |
| AND / OR | `test -f x && echo yes \|\| echo no` |
| Semicolons | `echo a; echo b` |
| Subshells | `(echo a; echo b)` |
| For loops | `for x in a b c; do echo $x; done` |
| If/else | `if test -f x; then cat x; else echo missing; fi` |
| While/until | `while test $i -lt 5; do echo $i; i=$(expr $i + 1); done` |
| Variables | `FOO=bar; echo $FOO` |
| Expansion | `${VAR:-default}`, `${VAR:+alt}`, `${#VAR}`, `$(cmd)` |
| Quoting | Single quotes are literal; double quotes allow expansion |
| Globbing | Unquoted `*` and `?` patterns expand; bare relative patterns use the current directory, and absolute and `./` forms are supported. Quote patterns to pass them literally |
| Negation | `! false` returns 0 |

## Intentionally unsupported

A normal session cannot invoke `cp`, `chmod`, `wget`, `curl`, `bash -c`, or
`exec`. The shell is an interpreter, not a real bash process. The write surface
is limited to the whole-file operations listed above; there are no streaming,
partial, or offset writes.
