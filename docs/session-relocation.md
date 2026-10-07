# Relocate Claude Code project sessions

When a project folder is renamed or moved, Claude Code's saved sessions remain
under the old encoded path in its `projects` directory. In **Sessions**, choose
**Claude Code**, then **Relocate project** beside the old folder.

1. Close Claude Code and its editor sessions. Wait at least one minute after
   their last write.
2. Enter the new, existing project directory's absolute path.
3. Preview the session count, size, and source and destination paths.
4. Confirm the move. Keep the backup path shown in the dialog.
5. Reopen the new folder in the editor and check its Claude Code session list.

The operation moves the entire stored project, including subagents, tool
results and project memory. Session IDs and message content are preserved.
Top-level transcript `cwd` fields beneath the old project are mapped to the
new path; references to other projects and external worktrees stay unchanged.
An existing `sessions-index.json` has its project and transcript paths updated.
Historical paths in messages, tool arguments and memory text are not rewritten.

On Windows, a WSL project's destination is interpreted inside the same running
distribution. The CLI can also run directly inside WSL:

```sh
magpie sessions relocate-claude --from /home/me/old-project --to /home/me/new-project
magpie sessions relocate-claude --from /home/me/old-project --to /home/me/new-project --confirm <token-from-preview>
```

The first command only previews and prints JSON. The confirmation token binds
the operation to those paths and the source files; changes require a new preview.
`CLAUDE_CONFIG_DIR`, when set for the CLI, selects its Claude Code store.

## Scope and safety

- The target Claude project store must not exist. Merging projects, selecting
  individual sessions, and cross-distribution moves are not supported.
- Recently modified files, malformed transcripts, symlinks, ambiguous encoded
  paths, and long hashed project names are refused.
- Close clients before migration. A one-minute write check cannot detect an
  idle client that still holds a session open.
- Claude Desktop's independent stores, account settings, global prompt history,
  and global files keyed by session ID are left in place.

## Recovery

Original project files are kept byte-for-byte under
`<Claude config>/magpie-relocations/project-*/original`, outside the session
scanner. `manifest.json` records the source, destination and backup paths.
Staged files live beside that backup until published.

To undo a completed migration, close clients, preserve the destination project
store outside `projects` (especially if conversations have continued), then move
the backup's `original` directory to the manifest's `from` path. Do not overwrite
an existing directory or leave two copies of the same session under `projects`.

After an interrupted operation, inspect the manifest and the source,
destination and `original` directories before restoring. A failed publish
attempt restores the original source; if that restoration also fails, the error
identifies the backup location. Backups are retained until you remove them.
