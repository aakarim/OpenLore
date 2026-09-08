# Editing OpenLore Files

OpenLore exposes its virtual filesystem through SFTP, so a compatible editor
can browse the remote directory tree and save individual files without cloning
or synchronizing the whole tree into a local project folder. An editor may
cache files while they are open, but OpenLore remains the source of truth.

OpenLore is read-only by default. To save from an editor, the server must run
with `readonly: false` (or `--readonly=false`). When authentication is enforced,
the connected identity must also have an `rw` grant for the target docset. See
[Writing and Publishing](writing.md) for configuration details. Read-only
deployments and identities can still browse and open files.

## VS Code

Use an SFTP filesystem extension rather than Microsoft's **Remote - SSH**. For
example, install
[SFTP FS](https://marketplace.visualstudio.com/items?itemName=LewLie.sftpfs):

```bash
code --install-extension LewLie.sftpfs
```

Then:

1. Verify the connection from a terminal, for example with
   `ssh -p 2222 user@server`.
2. In VS Code, open the Command Palette and run **SFTP FS: Add Remote**.
3. Enter the same hostname, SSH port, username, and private key used by the
   terminal connection. Set the remote path to `/` or to one docset such as
   `/docs`.
4. Connect from the SFTP FS view, expand the tree, and open a file. Saving the
   editor writes that file directly back to OpenLore.

SFTP FS saves an existing file using the SFTP operations OpenLore supports:
stat, open with create/truncate, offset writes, and close. It does not need a
remote shell, remote temporary file, or rename for an ordinary save.

Microsoft's **Remote - SSH** extension is not compatible with OpenLore. It
expects a general-purpose remote operating system where it can install and run
VS Code Server, extensions, terminals, and port forwarding. OpenLore instead
provides a restricted shell and virtual filesystem with no arbitrary process
execution.

## Other editors

These editors can access individual files through SFTP without installing an
editor server on the OpenLore host:

| Editor | Platform | SFTP workflow |
|---|---|---|
| Vim or GVim with netrw | Windows, macOS, Linux | Open an `sftp://` file or directory |
| [Nova](https://help.nova.app/remote-files/servers/) | macOS | Use its built-in SSH/SFTP remote sidebar |
| [UltraEdit](https://www.ultraedit.com/support/tutorials-power-tips/ultraedit/configure-ftp/) | Windows, macOS, Linux | Use its built-in SFTP browser and remote open/save |
| WinSCP's built-in editor | Windows | Open and save a file from an SFTP session |
| Kate or KWrite through KDE KIO | Linux | Open an `sftp://` location |
| Editors opened from GNOME Files/GVfs | Linux | Connect to an SFTP location and open an individual file |

For Vim installations that include netrw, open a file with:

```bash
vim 'sftp://user@server:2222//docs/notes.md'
```

The second slash before `docs` selects an absolute remote path.

Except for the documented VS Code workflow, these clients are not tested by
the OpenLore project. Some editors implement a “safe save” by uploading a
temporary file and renaming it over the destination. OpenLore does not currently
support SFTP rename, so disable atomic/safe-save behavior if the editor offers
that setting. A client that cannot save directly to the final path will not yet
work.

## Save behavior and limitations

An SFTP save is staged privately and submitted when the file handle closes as
one governed whole-file write. The same authorization, validation, approval,
size-limit, ordered-write, and conflict checks used by shell writes still
apply. An interrupted transfer is discarded. If a file changed after the
editor opened it, the save fails rather than overwriting newer content; reload
the latest version and retry the edit.

Editors can update existing files and create files in existing folders.
OpenLore deliberately rejects SFTP rename, directory creation, deletion, and
metadata changes such as `chmod`. Continue to use OpenLore shell commands for
governed namespace changes.

## Troubleshooting

- **Permission denied on save:** confirm `readonly: false` and, when
  authentication is enforced, a named identity with an `rw` grant for the
  target docset.
- **Save fails after uploading:** the file may have changed concurrently or a
  validation/approval rule may have rejected or deferred the write. Re-open the
  file and inspect the server logs or pending requests.
- **The editor can browse but not save:** check whether it uses a temporary
  remote file followed by rename. Select direct overwrite or disable safe save.
- **Remote - SSH tries to install VS Code Server:** use an SFTP filesystem
  extension instead.
