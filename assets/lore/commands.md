# Available Commands

## Filesystem
- `ls` — List directory contents
- `cat` — Display file contents
- `head` / `tail` — First/last N lines
- `tree` — Directory tree visualization
- `find` — Find files by name or type
- `stat` — File metadata
- `wc` — Count lines, words, bytes
- `du` — File space usage
- `diff` — Compare two files

## Search
- `grep` — Search for patterns (`-E`, `-F`, `-i`, `-n`, `-r`, `-R`, `-o`, `-h`, `-c`, `-v`, `-l`); use `-E 'a|b'` for multiple patterns

## Text Processing
- `sort`, `uniq`, `cut`, `sed`, `awk`, `tr`
- `rev`, `tac`, `nl`, `fold`, `paste`, `column`
- `join`, `comm`, `expand`, `unexpand`

## Data
- `jq` — JSON processor

## Utilities
- `xargs`, `seq`, `printf`, `date`
- `basename`, `dirname`, `tee`
- `base64`, `md5sum`, `sha256sum`

## Shell Features
- Pipes: `grep pattern file | sort | head -5`
- Logical operators: `test -f x && echo yes || echo no`
- Loops: `for x in a b c; do echo $x; done`
- Variables: `FOO=bar; echo $FOO`
- Command substitution: `echo $(wc -l file.md)`
