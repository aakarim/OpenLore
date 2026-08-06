# Command Reference

OpenLore implements a bash-like command language over its virtual filesystem.
Run `help` in a session for the command list available on that server.

## Filesystem

| Command | Description |
|---|---|
| `ls` | List directory contents (`-l`, `-a`, `-R`, `-S`, `-t`, `-F`, `-1`, `-h`, `-d`) |
| `cat` | Display file contents (`-n`, `-A`) |
| `head` | First N lines or bytes (`-n N`, `-c N`) |
| `tail` | Last N lines or bytes (`-n N`, `-c N`, `+N`) |
| `tree` | Directory tree visualization (`-L depth`, `-a`, `-d`, `-f`) |
| `stat` | File metadata |
| `wc` | Count lines, words, bytes (`-l`, `-w`, `-c`, `-m`) |
| `du` | Estimate file space usage (`-a`, `-h`, `-s`, `-c`) |
| `diff` | Compare two files (`-u`, `-q`) |
| `cd` / `pwd` | Navigate the virtual filesystem |

## Search

| Command | Description |
|---|---|
| `grep` | Search patterns (`-i`, `-n`, `-r`, `-v`, `-c`, `-l`, `-o`, `-L`, `-w`, `-x`, `-m`) |
| `find` | Find files (`-name`, `-type f\|d`) |

## Text processing

| Command | Description |
|---|---|
| `sort` | Sort lines (`-r`, `-n`, `-u`, `-f`, `-k N`, `-t SEP`) |
| `uniq` | Filter duplicate lines (`-c`, `-d`, `-i`, `-u`) |
| `cut` | Select fields or characters (`-d DEL`, `-f FIELDS`, `-c CHARS`, `-s`) |
| `sed` | Stream editor (`-n`, `-e`, `s/pat/repl/flags`, `-i`) |
| `awk` | Pattern scanning (`-F SEP`, `-v VAR=VAL`) |
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
| `publish` | Publish stdin into a docset inbox |
| `approve` | Approve a pending changeset when authorized |
| `spawn` | Run configured external work asynchronously when explicitly trusted |

## Introspection

| Command | Description |
|---|---|
| `lore` | Introspection dispatcher |
| `lore docsets` | List accessible docsets, grants, paths, and attributes |
| `lore meta [path]` | Emit document frontmatter as NDJSON, scoped to the current directory or path |
| `lore meta --filter skills [path]` | Discover Agent Skills collections without scanning unrelated docs |
| `lore validate [bundle]` | Validate an OKF bundle, local links, and aliased-path portability |

## Skills management

These commands require `plugins.skills.enabled: true`. Mutating commands also
require a named `rw` grant on the destination docset.

| Command | Description |
|---|---|
| `skills status [folder]` | Report collection state and linked remote skills as NDJSON |
| `skills enable [folder]` | Enable recursive Skills collection behavior |
| `skills disable [folder]` | Disable collection behavior without deleting skills |
| `skills validate [scope]` | Validate accessible Skills collections |
| `skills import <github-spec> [parent-dir]` | Import and link a public GitHub skill |
| `skills update [skill-folder]` | Force an imported skill to check and apply its remote |
| `skills remove-remote [skill-folder]` | Keep imported files but stop tracking GitHub |

Run bare `skills` for GitHub spec syntax, automatic branch tracking, pinning,
and collection recovery.

Discover installed skills with the `skills` metadata filter, then read the
chosen `SKILL.md`:

```bash
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
`ATTRIBUTES` may include `home`, `inbox`, and `alias`; aliases point to their
canonical path in `TARGET`.

## Shell syntax

| Feature | Example |
|---|---|
| Pipes | `grep pattern file \| sort \| head -5` |
| AND / OR | `test -f x && echo yes \|\| echo no` |
| Semicolons | `echo a; echo b` |
| Subshells | `(echo a; echo b)` |
| For loops | `for x in a b c; do echo $x; done` |
| If/else | `if test -f x; then cat x; else echo missing; fi` |
| While/until | `while test $i -lt 5; do echo $i; i=$(expr $i + 1); done` |
| Variables | `FOO=bar; echo $FOO` |
| Expansion | `${VAR:-default}`, `${VAR:+alt}`, `${#VAR}`, `$(cmd)` |
| Quoting | Single quotes are literal; double quotes allow expansion |
| Negation | `! false` returns 0 |

## Intentionally unsupported

A normal session cannot invoke `cp`, `chmod`, `wget`, `curl`, `bash -c`, or
`exec`. The shell is an interpreter, not a real bash process. The write surface
is limited to the whole-file operations listed above; there are no streaming,
partial, or offset writes.

## OpenLore CLI

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
  token mint        Mint an access token for an identity
  token verify      Verify an access token and print its claims

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
```

Run `openlore --help` or a subcommand with `--help` for its current flags.
