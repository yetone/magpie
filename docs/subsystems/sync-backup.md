# Sync and backup

`internal/backup` collects magpie's portable setup into a passphrase-sealed
bundle. `internal/davsync` keeps that bundle in a WebDAV folder, an S3 bucket
or a GitHub repository. It merges changes between computers; it does not
clone a working tree or synchronize arbitrary files.

## Sources of truth

- [`Config`, `Configure`, `Status` and `syncOnce`](../../internal/davsync/davsync.go)
  keep the active server in `sync.json`, and the last local/remote hashes,
  versions, notices and undo file in `sync-state.json`. These files live beside
  magpie's settings. Secrets are local, never in the status response.
- The [`remote`](../../internal/davsync/dav.go) interface reads a version and
  conditionally replaces it. WebDAV and S3 use ETags; GitHub uses the file's
  blob SHA. S3's conditions (`If-Match`, `If-None-Match`) go outside the
  SigV4 signature ([`unsigned`](../../internal/davsync/s3.go)): a server that
  reads one back otherwise than it arrived refused the signed request with
  403 SignatureDoesNotMatch once a sync had an ETag to ask with. [`github.go`](../../internal/davsync/github.go)
  interprets `github://owner/repo[/prefix]`, with `Config.Branch` selecting a
  branch or using the repository's default.
- [`backup.go`](../../internal/backup/backup.go) owns bundle contents and
  sealing. Sync divides the setup into providers, settings, profiles, agents
  and library. Provider keys, agent models, library and usage have their own
  inclusion switches.
- [`usage.go`](../../internal/davsync/usage.go) shares separately sealed usage
  and quota files through a remote's `files` interface.
- [`restore.go`](../../internal/davsync/restore.go) replaces local parts from
  the remote bundle, first keeping the local setup for Undo.
- [`backup.go`](../../internal/gui/backup.go), the Settings form in
  [`app.js`](../../internal/gui/assets/app.js) and
  [`webdav_cli.go`](../../webdav_cli.go) reach the same sync operations.

## What stays on each computer

The settings part carries the user's preferences, but not what is only about
one computer. [`Settings.KeepOwn`](../../internal/settings/settings.go) puts
those back from this computer's settings whenever a sync, a join, a Restore,
an Undo or a backup file brings settings in, and `hashes` blanks them, so
changing one is no change to push. They are: the window's size and whether it
was maximised, the fonts, the proxy, the gateway's port, sharing on the local
network (`LAN`) and the addresses let in to it (`AllowIPs`), the private
hosts magpie may download from (`FetchPrivate`), the Dock, gateway
mode, keeping awake, lightweight windows, WSL lookups, account discovery, when
allowances are read, recording gateway conversations, the Mac terminal a
session resumes in (`SessionTerminal`) and what the menu bar shows. Launch at
login is the system's own setting (`internal/autostart`) and the listen
address is `MAGPIE_ADDR`, so neither is in a bundle.

The Usage page's clears (`usageCleared`, see
[gateway sessions](gateway-sessions.md)) are a preference, not a computer's
own, so they ride the settings part: a model cleared on one computer is
cleared on the others, their shared usage days included. The usage files
themselves are never rewritten by a clear.

The providers part works the same way for which account an agent is kept
signed in to (`KeepLogin`, `KeepLoginAs`, `SwitchOnly`): one person keeps a
personal account at home and a work account at the office, and sync set
each computer to the other's every day (YsiCing on X). `provider.Mirror` keeps
this computer's values for a provider it has, and brings a new one without
`KeepLoginAs`, an account signed in on the other computer; `hashes` blanks
all three (`TestMirrorKeepsKeepLogin`, `TestKeepLoginIsNoChange`).

The agents part keeps a computer's own pick the same way, field by field
(hayden4608 on Discord: two computers on one GitHub sync, each wanting its own
Codex model, set each other's on every sync). Each field a sync changes here
is recorded in sync-state.json (`Brought`), with the value it wrote over when
that value was picked here, not brought by an earlier sync (`Replaced`). A
field the user then sets back to that value is this computer's own (`Own`,
[`noticeOwn`](../../internal/davsync/own.go)). Setting a new value is a pick
like any other: it goes up, so a change made on either computer still reaches
the other. For an own field, `hashesOwn` leaves it out, so picking it is no
change to push and the server's value is no change to bring; `bringAgents`
brings the agents' other fields and puts an own field back if a provider moved
it; `keepServersOwn` keeps the server's value of it in what this computer
pushes. Settings → Sync lists the own fields under Kept on this computer, with
**Follow the others** (`davsync.Follow`): the next sync brings the server's
values in and clears `Own`. A Restore brings everything and clears it too. The
three maps survive a change of server (`TestOwnModelStaysOnEachComputer`,
`TestNewPicksStillGoBothWays`, `sync-own-fields.test.cjs`).

Sharing turned on at one computer never turns it on or off at another
(Lullaby on Discord). A backup restored onto a new computer leaves sharing as
that computer has it, off on a fresh one: sharing opens the gateway to the
network, and the user turns it on there by hand, as the port and proxy are
set there. The default sharing key is different: it is in the gateway-key
store, which rides the settings part with `LANKey` and `LANKeyID`, so every
computer holds the same one, and sharing turned on at a second computer takes
it up rather than making another (`TestLANSharingStaysOnItsComputer`).

## Runtime and state

The gateway runs sync every three minutes by default. `SetAuto` can select a
longer interval or manual sync only. Saving in Settings syncs immediately.
Each operation holds the sync lock, so configuration changes cannot race a
sync's state write.

A first computer creates a remote backup. Another computer joining takes
the remote setup and keeps displaced local parts in its sync folder.
Subsequent syncs compare each part with the hashes from the previous sync:
local-only changes go up, remote-only changes come down, and a part changed
on both sides keeps the newer version while retaining the replaced copy.
A conditional write refused because another computer committed in between
is retried through this same merge.

When a remote file is empty, damaged or another app's format, sync stops and
describes it through `ServerFile`. Settings and the backend's `upload` command
can replace it with this computer's setup, keeping the old file first.
HTML and XML are not offered for replacement. GitHub diagnostics name the
repository and `magpie github upload`, rather than WebDAV's recovery path.

A local file that is there but can't be read is unknown, never empty or the
defaults. `backup.Collect` stops on an unreadable providers.json (with its groups
and order), settings.json (`settings.LoadChecked`: unreadable, or
not parsing with no good `.bak`) or library.json, and on a gateway key
store that fails to export. A backup, an export and a sync all end with that
error, nothing sealed or pushed; the sync's status shows it. A missing or
empty settings.json is still none (`TestCollectStopsOnUnreadableSettings`,
`TestSyncStopsOnUnreadableProviders`, `TestSyncStopsOnUnreadableSettings`,
#2269).

Switching services keeps all inactive kinds in `Config.Servers`. `Other`
remains the most recently left server for compatibility with older saved
configurations and clients. Only the active server is used. Empty credential
fields reuse a saved credential only for its matching account; GitHub
restricts reuse to the same owner/repo. Branch or prefix changes within that
repository can keep the token. The branch is part of the sync state key.

## Backups by themselves

[`auto.go`](../../internal/backup/auto.go) writes the setup into a folder on
this computer on a schedule (jwaitq on Discord). `RunAuto` runs while the
gateway serves (`gateway.WhileServing`, wired in `internal/cli/main.go`) and
calls `AutoTick` every few minutes; tests call `AutoTick` with their own
clock.

- **Config:** `autobackup.json` beside the settings holds only what the user
  set (`Off`, `Hours`, `Keep`, `Dir`). Missing means on, every 24 hours,
  keeping 7, in `settings.Dir()/auto-backups` (not `backups`, which the
  library prunes). A config that can't be read or parsed makes no backup and
  shows the error; it is never read as the defaults.
- **Content:** `Collect(false, app)`, the manual export without keys, with
  the proxy settings blanked too, since nothing seals it: no passphrase is
  there on a schedule. Files are 0600 in a 0700 folder.
- **Format:** `magpie-YYYYMMDD-HHMMSS[-n]-{auto,restore}.json`, holding the
  bundle and its sha256. Each is written to a `.magpie-backup-*.tmp` and
  renamed into place. A file whose sum doesn't match, a temp file or a name
  that doesn't match is never listed, restored or pruned.
- **Schedule:** a backup is due `Hours` after the newest, so a restart
  doesn't make one early; one whose bundle equals the newest's is not
  written. After a write the oldest `-auto` files past `Keep` are removed
  (and `-restore` ones past `Keep` after a restore), with its own temp files
  over an hour old; other files in the folder are never touched.
- **Restore:** `AutoRestore(name)` first writes a `-restore` backup of what
  is here (and stops if that fails), then `Restore(bundle, All)`, which is
  additive: providers added since stay, each keeps its local key. Restoring
  the `-restore` file undoes it.
- **API:** `GET /api/autobackup`; `POST /api/autobackup/{set,choose,now,
  reveal,restore}`. `set` changes only the fields sent; `dir ""` is the
  default folder. Settings draws it under Sync's Export and Import from the
  lazy [`autobackup.js`](../../internal/gui/assets/autobackup.js).

## GitHub constraints and errors

- The repository must exist and be private: `prepare` refuses one whose
  repository read doesn't say `"private": true`, since a public history hands
  every sealed version to anyone to guess the passphrase against
  (`TestGitHubPublicRepositoryRefused`). In an empty repository, the first encrypted
  backup initializes its default branch through the Contents API. When that
  API refuses it as too large, a short `README.md` in magpie's folder makes
  the first commit and the backup follows as the second. A selected
  branch other than the default must already exist. The token needs repository
  Contents read and write.
- Backups are `<prefix>/magpie/magpie.magpie-backup`; optional usage files
  live beside them under `usage/`. Each write or removal is one commit, and
  unchanged setup does not create another backup commit.
- A repository with commits is written through the Git Data API: a blob
  (up to 100 MB), a tree on the head commit's tree, a commit with the head
  as its parent, and a ref update without force. The Contents API's PUT
  refuses files well under that with 422 "the file is too large to be
  processed" (a 102-skill backup past ~19 MB, Discord). The file must still
  be at the version sync read, checked at the head commit, before the
  commit is built. A ref update that is not a fast-forward is built again on
  the new head, up to three times; if the new head changed the file it is
  `errChanged`. A branch deleted meanwhile is an error, never created.
- A blob goes up as base64 in one JSON body, a third larger than the file,
  and GitHub refuses a body well under the 100 MB a blob can hold: a
  50.8 MB backup (about 68 MB of body) got 422 "Sorry, your input was too
  large to process" (Zhang-Wei, Discord), while a 19 MB file's Contents
  PUT (about 25 MB of body) went through; GitHub documents no request
  size. So a backup over 16 MiB (`githubPart`) goes up in parts, in the
  same one commit: `magpie.magpie-backup.parts/1`, `/2`, … beside it, and
  at the backup's own path a short list of them (`githubParts`: format
  `magpie-backup`, version 2, `split: github-parts`, the total size and
  each part's blob SHA and size). The list's blob SHA is the version, and
  it names each part by blob SHA, so one version pins one whole set; a
  read downloads each part by that SHA, checks it, and returns the backup
  whole. A list that doesn't add up or a part that isn't the one named is
  an error, never a backup put together wrong. A write removes the parts
  the list at the head named and the new backup doesn't use, in the same
  commit, so going back to one file leaves no part behind. Backups up to
  16 MiB stay one file, as before.
- A magpie from before parts opens the list as a backup with version 2
  and no key derivation, and stops with "made by a newer magpie; update
  magpie" — not `ErrCorrupt` or `ErrNotBackup`, so it neither rebuilds
  over it nor offers to replace it. A backup in one file, from any
  magpie, still reads.
- Tokens are separate from the library's GitHub token, sent only to
  `https://api.github.com`, and must differ from the encryption passphrase.
  Repository history retains older encrypted versions.
- Repository and branch access is checked before a file's 404 can mean
  "no backup yet." Failed reads and malformed metadata fail sync.
- Only the ref endpoint's explicit `409: Git Repository is empty.` allows
  initialization, after repository access and the default branch are checked.
  The initial write omits `branch` so GitHub creates its default branch; other
  conflicts and missing branches are not treated as an empty repository.
- Large files and parts are downloaded by blob SHA, preserving the version
  that was read. Sync files are limited to 64 MiB in all (four parts), the
  bound the library's skill budget is sized for. Usage listing fails at the Contents
  API's 1,000-file limit rather than treating a truncated list as complete.
- A stale SHA, a backup another commit changed, or a racing initial
  creation returns `errChanged`. Other 422
  responses stay actionable errors. Authentication, permission and branch
  failures remain distinct from missing files. Rate limits use sync backoff.
- The request archive goes to S3 only when sync has a bucket. Its objects are
  separate from the encrypted setup and usage files. With no bucket the same
  files are kept on this computer instead (`gateway.DiskArchive`, in
  `<settings.Dir()>/request-archive/<date>/<id>.json`, written through a
  temporary file and renamed, 0600), under its own switch,
  `RequestArchiveLocal`. That switch and its size, `RequestArchiveLocalMB`
  (1 GiB unless set; the oldest calls are removed first past it), are in
  `KeepOwn`, so sync never turns on writing bodies to another computer's
  disk, and `RequestArchive` synced from a computer with a bucket keeps
  nothing on one without (`TestArchiveNeverOnDiskUnasked`,
  `TestRequestArchiveLocalIsThisComputers`).

A library skill turned off on one computer (`OffHere`, see
[library](library.md)) stays off there alone: the library part never carries
it, and putting another computer's library keeps it, so the others keep the
skill on and a sync from them doesn't turn it on here
(`TestSkillOffHereThroughSync`, two computers through the fake S3).

An MCP server kept to one computer (`Server.Local`, see
[library](library.md)) is never in the library part, so it never reaches
another computer by sync or backup file. Putting another computer's library
keeps it, and a server of the same name from there goes to `Library.Away`,
given to no agent here and carried on unchanged, so neither computer's sync
removes or replaces the other's (`TestLocalServerThroughSync`, two computers
through the fake S3; `TestRestoreKeepsLocalServer` for a backup file).

The library part carries the library's subagents (`library.Bundle.Subagents`,
see [library](library.md)), by sync and backup file alike: the receiving
computer writes them into its agents when it puts the library, and records
what it wrote there itself, so `Applied` never travels and a file the user
edited stays theirs. A bundle from a magpie before subagents were carried
has no list and keeps this computer's; an empty list takes them out. A
library with none hashes as it did before, so upgrading syncs nothing
(`TestSyncSubagents`, `TestNoSubagentsHashAsBefore`,
`TestBackupFileCarriesSubagents`).

## Verification

```sh
go test -tags nogui ./internal/davsync
go test -tags nogui ./internal/cli -run 'TestGitHubCmd|TestWebdavCmd|TestS3Cmd'
node --test internal/gui/tests/github-sync.test.cjs internal/gui/tests/s3-sync.test.cjs internal/gui/tests/sync-other-kind.test.cjs
```

`TestGitHubEmptyRepository` covers the first encrypted backup initializing an
empty repository and an unchanged second sync. `TestGitHubEmptyRepositoryFailures`
covers missing branches, unrelated ref conflicts and another computer winning
the initial write. `TestGitHubLargeBackup` writes a 21 MB backup past a fixture whose Contents
API refuses it, as one commit, and reads it back by blob SHA.
`TestGitHubBranchMovedMeanwhile` covers a head that moved before the ref
update, and `TestGitHubEmptyRepositoryLargeBackup` a large first backup.
`TestGitHubSplitBackup` writes a 50.8 MB backup past a fixture that refuses
a blob over 24 MiB, as four parts in one commit, reads it back byte for
byte, then goes to two parts and back to one file with no part left.
`TestGitHubOneFileBackupStillReads` reads a 30 MB backup in one file and
replaces it with parts; `TestGitHubPartsOlderMagpie` checks the list stops
an older magpie as a newer magpie's backup; `TestGitHubPartsDamaged`,
`TestGitHubPartsBranchMovedMeanwhile` and
`TestGitHubEmptyRepositoryPartsBackup` cover a bad list or part, a moved
head (each blob sent once) and an empty repository;
`TestGitHubSyncLargeLibrary` syncs a library that seals past one request
to a second computer.
`TestGitHubNotBackup` covers backend-specific recovery
advice when the repository holds an empty or foreign file.

Go tests use `testenv` homes and HTTP fixtures. Browser tests use isolated
API fixtures in Chromium and WebKit. A live GitHub write requires a test
repository and token; fixture tests do not establish that a particular
account's permissions or repository rules allow commits.
