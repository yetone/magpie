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
  and library. Each part, provider keys, memory, usage and sign-ins have
  their own inclusion switches (`Config.syncs`).
- [`plan.go`](../../internal/davsync/plan.go) previews a sync (`Preview`,
  `PreviewWith`), runs the one previewed (`SyncPreviewed`) and undoes the
  last sync that brought parts in (`UndoSync`).
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

A Remote magpie's **Use it anyway** (`KeepSelf`) is kept the same way. Sync
carries a Remote magpie provider to the computer it names (hanslin1666 on
Discord: B uses A through one, and on A it points at A), and there it is
found to be this magpie and left out unless kept (see
[providers and accounts](providers-accounts.md)). Whether it is this magpie
is about one computer only, so it is kept in `remote-self.json`, which no
backup or sync carries, and `KeepSelf` is kept by `Mirror` and `Restore`
(false for a provider new here) and blanked by `hashes`
(`TestMirrorKeepsKeepSelf`, `TestKeepSelfIsNoChange`).

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

A whole agent's picks can be kept to one computer on purpose, as an MCP
server can (chenxiang0010 on Discord): the Agents row's menu has **Don't sync
picks to your other computers** (`POST /api/agents/here`; the row says
"Not synced"; it said "This computer only", which read as sharing, bin.90
on Discord), which adds the agent's id to
`Settings.AgentsHere`, a computer's own kept by `KeepOwn` and by a save of
the Settings page (`settingsapi.Apply`). It is off by default, so an upgrade
changes no pick anywhere. For each such agent, `withHere` adds `<id>.*` to
the own set (`isOwn` matches every field of it), so a sync neither pushes its
fields nor brings the server's in, a Restore from the server leaves them,
and `backup.Restore` skips the agent in a backup file. sync-state.json
records the list as of the last sync (`Here`): the hashes before a sync use
it and those after use the current list, so turning it on or off is no
change on either side, and the list changing forces the sync past its early
returns. Turned off, the next pick here goes up again; the other agents
sync both ways throughout. A kept agent is not listed under Kept on this
computer, which is the implicit own fields only (`TestAgentHereThroughSync`,
`TestAgentsHereIsThisComputers`, `agent-here.test.cjs`).

A provider can also be kept on one computer
([`Provider.ThisDevice`](../../internal/provider/provider.go): the provider
row's menu, `magpie provider this-device <id> yes|no`), for a local model that
exists only there (treydtw on X). `collect` leaves it, and its place in the
provider order, out of what sync sends, so changing it is no change to push
and the other computers lose their copy of it at their next sync. When a
bundle is brought in, `provider.Mirror` neither replaces nor removes a provider
kept here, whatever the other computers have under its id, and takes in none
that a bundle marks as kept on its computer
(`TestProviderOnThisDeviceStaysOnIt`). It is off by default for every provider,
local addresses included: a copy of a `127.0.0.1` provider on a computer that
runs the same local server works, and turning it on by default would take the
copies off the computers that have one. A file backup (`backup.Collect`) still
carries the provider, with the mark, and a Restore keeps the mark a provider
here has.

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
the remote setup and keeps displaced local parts in its sync folder. A
part the file holds nothing of its own in (`unset`: no providers or
routing groups, the default settings, no profiles, an empty library) is
the folder never having had one, not a removal: on a join, a computer
with that part set up keeps it and sends it up (#5642: an empty computer
that synced first emptied the next one's providers and groups). After the
join an emptied part is a removal and spreads as before
(`TestSyncEmptyFirstKeepsLaterSetups`).
Subsequent syncs compare each part with the hashes from the previous sync:
local-only changes go up, remote-only changes come down, and a part changed
on both sides keeps the newer version while retaining the replaced copy.
A conditional write refused because another computer committed in between
is retried through this same merge.

A computer can follow the server instead (`Config.FollowServer`, set only
by `SetFollowServer`: Settings → Sync → This computer → Follows the
server, `/api/davsync/follow-server`, or `magpie <webdav|s3|github>
follow on|off`; Ramon317317 on X). It takes every changed part from the
file whichever side is newer, never writes the file (not a merged
sign-in, not a rebuild of a damaged file), and a change made on it is
kept in the sync folder like a join's and said in the notice
(`Notice.Followed`). The join rules still hold: an `unset` part on the
server leaves this computer's, and own agent fields and agents only here
stay. Following a server with no file yet is an error, nothing sent.
The Library page says so above its Instructions, MCP, Skills, Hooks,
Subagents and Plugins tabs (and Memory while memory is synced), with a link
to that setting (`followNote` in `plugins/library/client.js`,
`library-follow-server.test.cjs`).
`Configure` keeps the choice, as it keeps `AutoEvery`. The computer
followed is an ordinary two-way one (`TestFollowServer`,
`TestFollowServerSendsNoSignIn`, `sync-follow-server.test.cjs`).

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

## What syncs, preview and undo

#6428 (jinpwng on X): sync only the library and memory, keep each
computer's providers, settings and models, see what a sync changes before
it does, and take it back.

- **What syncs.** `Config.Providers`, `Settings` and `Profiles` are
  `*bool`, nil meaning synced, so a `sync.json` from before the choice
  syncs every part as it did. `Agents`, `Library`, `Memory`, `Usage` and
  `SignIns` were switches already. A part left out (`Config.syncs` false)
  is neither read from nor written to the file: this computer keeps its
  own, the server keeps the others' (a first computer puts none of it up,
  which a joining one reads as `unset`). The Settings form shows
  Providers (with their API keys under it), Settings, Agents' models,
  Library and Memory; Profiles, Usage and Sign-ins sit under a folded
  Advanced, open when one of them is set away from its default. The form
  sends a part's choice only when it is off or was off, so saving an old
  setup leaves it nil. The setup text carries the choice, and the CLI's
  `set providers=no settings=no profiles=no` sets it. The Sync row says
  which parts stay on each computer (`sync-left`).
- **Turned back on.** `state.Off` is the parts the last sync left out. One
  turned back on is joined like a first sync (`rejoin`, `again` in
  `syncOnce`): the server's comes here, this computer's kept in the sync
  folder, and an `unset` one on the server leaves this computer's
  (`TestSyncPartTurnedBackOn`).
- **Preview.** `syncOnce` with a `*Plan` runs the same merge and returns
  before any write, here or on the server. A part's plan says `in`, `up`,
  `same` or `off`, whether it replaces a change made here (`replaces`), and
  its items added, removed or changed; memory, sign-ins and plugins removed
  by another computer are counted. `Plan.Token` hashes what the sync would
  do; `SyncPreviewed(token)` runs only while a new plan has the same token,
  else `ErrPlanChanged` and the GUI shows the new plan. A preview from the
  form (`PreviewWith`) is of the unsaved setup, and its Save and sync is
  the form's ordinary save, without a token. Reached by Settings → Sync →
  Preview a sync, the form's Preview, `/api/davsync/preview`,
  `/api/davsync/sync-previewed` and `magpie <webdav|s3|github> preview`.
- **Undo.** A sync that brings parts in keeps this computer's setup first
  (`keepSealed`, as a join does) and records `state.Applied`. `UndoSync`
  puts those parts back from that file, after keeping what is here now.
  A part changed here since the sync is `*ChangedSince` (HTTP 409 with
  `changed`), written over only with `force` (Undo anyway, `undo-sync
  anyway`). The undo stays on this computer: `state.Local` is set to the
  undone parts' hashes, so the next sync sends nothing and the server keeps
  the others' (`TestUndoSync`). A change made here later is an ordinary
  local change, and that whole part goes up. `/api/davsync/undo-sync`,
  Settings → Sync → Undo the last sync, `magpie <kind> undo-sync`.
  Restore's Undo (`davsync/undo`) is unchanged: it undoes a Restore.

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
  Its Versions row says how many and the newest (a state, kept in sight
  where Settings' hints rest out of it) and folds the versions, each with
  Restore…, until its name or that line is clicked; the fold is kept over
  redraws for the window's life (chen2652 on Discord,
  `auto-backup.test.cjs`). The status's `inside` says the folder is `settings.Dir()` or in it (letter
  case aside off Linux), the default included; Settings then says under the
  folder that a reinstalled system or a lost disk takes the backups too, and
  to pick one on another disk or a cloud drive's (two_2025. on Discord,
  magpie on C:). Keeping everything elsewhere is the Data folder's move
  (below) or portable mode (`internal/appdir`); docs/start's
  `#data-folder` gives all three (`TestAutoInsideMagpiesFolder`,
  `auto-backup.test.cjs`).
- **Sign-ins:** off unless picked (`SignIns`, with `Passphrase`); see
  [Sign-ins, when picked](#sign-ins-when-picked).
- **Plugins:** ride the providers part. `AddPlugins` only adds, so a removal is
  `syncOnce`'s: it keeps the names both sides had after the last sync
  (`state.Plugins`), and a name one side has since lost is removed on the other
  (`pluginPlan`, `removePlugin`), however the providers part came out. One that
  can't be removed (its agent is on magpie) is tried again next sync. A plugin
  added on one side stays (dolacmeo on X; `TestSyncPluginRemovalSpreads`). Not
  yet: an older sync with no record (`state.Plugins` empty) removes nothing.
- **Memory:** on unless turned off (`Memory`), merged memory by memory in
  `syncOnce` after the sign-ins, never replaced as a part; see
  [Memory's backups and sync](memory.md#backups-and-sync).

## Data folder on another disk

Settings › Sync's Data folder (CHENGJIAHEGOXIS and buhaodeshiyehao on X)
moves magpie's own files and cache to another disk, and back.
[`internal/appdir/move.go`](../../internal/appdir/move.go) does it:

- **What moves:** `appdir.Config()` and `appdir.Cache()` only, into
  `<picked>/magpie/config` and `<picked>/magpie/cache`. The agents' own
  folders (`~/.claude`, `~/.codex`…) and their session histories stay where
  each agent reads them. A link is left at each old path (a symlink, or a
  junction on Windows when symlinks need a privilege, as in #973), so every
  path magpie and the agents were given still works. A portable magpie is
  never moved.
- **Refused before anything quits** (`Target`): a relative path, a folder
  that isn't there, a `magpie` folder there with files in it, or a folder
  inside magpie's own folders.
- **Never under a running magpie.** `AskMove` writes `move-to.json` in the
  config folder. The GUI then starts itself again (`restartSelf`, the
  update path's relaunch) and quits; when another magpie serves the gateway
  (`magpie serve`, a launch agent), or for magpie web, it doesn't restart,
  and the row says the move waits. The next start runs `MovePending` in
  `cli.Main` before anything reads a file, after the old process is gone
  and only when no gateway answers. It is made once: a failure is not asked
  for again, and why is kept in `move-result.json` for the row.
- **How:** within one disk, a rename. Across disks, a copy, and the old
  folder is put aside (`.moved`) until the link is made, then removed; a
  failed copy removes the half copy and leaves the old folder as it was.
  Moving back takes the link away and renames the folder home; across
  disks it puts the link back, copies to `<config>.moving` and renames that
  into place. The empty `magpie` folder on the disk is then removed. The
  config moves first: a cache that fails after it says so and leaves the
  config moved.
- **Disk gone:** a link that leads nowhere is `UnreachableError`. `cli.Main`
  stops on it, with a native alert from the app (as #989), rather than
  starting an empty magpie that would write a new config over the link.
- **CLI:** `magpie data` says where they are; `magpie data move <folder>`
  moves at once with no gateway up, otherwise asks for it;
  `magpie data move --home` and `magpie data cancel`.

Routes: `GET /api/datafolder`, `POST /api/datafolder/{move,home,cancel,reveal}`
([`datafolder.go`](../../internal/gui/datafolder.go)). Tests:
`internal/appdir/move_test.go` (both disks, refused targets, a failed copy,
disk gone, once only), `move_windows_test.go` (the junction),
`TestDataFolderMoveRestarts` and `data-folder.test.cjs`.

## Sign-ins, when picked

A sign-in (a subscription account in logins.json, Codex's auth.json, a
plugin's sign-in in plugin-auth.json) stays on its computer unless the user
picks it, in each place on its own: Sync's **Sign-ins** tick, Export's
**With the sign-ins**, and Back up by itself's **Sign-ins in backups**. All
three are off by default, so an upgrade carries nothing new (qianxunji and
curtinp118 on Discord: a VPS restored from a backup had to sign in to every
subscription again). Import's **Bring in the sign-ins too** is on, since a
file has them only when it was made with them. Each place says beside the
switch that anyone with the file and the passphrase can use those accounts.

- **What goes:** [`provider.CollectSignIns`](../../internal/provider/signins_carry.go)
  takes magpie's own copies only (`carried`): not an agent's own account
  (`own`), not one in an agent's own home (`Home`), not a hidden row, not a
  `plugin:` row, not a retired built-in. Codex's live auth.json goes as its
  own record. Plugins' sign-ins come from [`plugin.CarriedAuth`](../../internal/plugin/carry.go).
  Each record has an id that names the account (agent, lower-case email and
  organisation; or the account key), its time of renewal and a hash; ids two
  rows share are left out on both sides, as no one can tell which is which.
- **Never carried:** a plugin's sign-in whose vendor spends a refresh
  token once. Two computers holding one copy race to renew it, and the
  vendor turns the loser away for good with no newer copy anywhere
  (#5583, skalitz-wolf). Such a plugin says so with its auth hook's
  `rotating` (`Provider.Rotating`); MiniMax Code and Qoder are known to
  (`rotatesKnown`). `CollectSignIns` leaves them out, and `records` leaves
  out one an older file carried, so a sync neither sends nor takes one
  and a Restore writes none. Each computer signs in to them itself
  (`TestCollectSignInsLeavesARotatingSignInOut`,
  `TestKnownRotatingSignInsStayWithNoPluginListed`).
- **Sealed:** a backup or export seals them with the rest of the file. An
  automatic backup, which has no passphrase on a schedule, seals them
  alone (`SealData`) with the passphrase asked for as they are turned on,
  kept in autobackup.json (0600) beside the settings and never sent in the
  view (`Passphrase` says only that one is set). A backup whose sign-ins
  another passphrase sealed restores without them (`SignInsLocked`). Sync's
  file is sealed with sync's passphrase; nothing reaches the server in the
  clear (`TestSyncSignIns`, `TestExportCarriesSignInsWhenAskedSealed`,
  `TestAutoBackupSignInsSealedAndDeduped`).
- **Restore and import** ([`RestoreSignIns`](../../internal/provider/signins_carry.go))
  add an account missing here and update one only when the incoming copy
  was renewed later. Nothing here is removed. Before writing over a row it
  finds every row that could be the same account (same login, same refresh
  token, same id): with none it adds, with more than one it keeps them all,
  and with one it keeps it when it isn't magpie's own copy, is held, or is
  the account the agent itself is signed in with. Codex's auth.json is
  written (temp and rename, 0600) only where Codex is signed out
  (`TestRestoreSignInsNeverWritesAnOlderTokenOver`,
  `TestRestoreSignInsLeavesAnAmbiguousAccount`,
  `TestRestoreSignInsSignsCodexInOnlyWhereSignedOut`).
- **Sync** merges per record against the hashes of the last sync
  (`SyncState.SignIns`, [`MergeSignIns`](../../internal/provider/signins_carry.go)):
  the later renewal wins; a record one side removed is removed on the other
  only when that side still has it as of the last sync; otherwise the side
  that changed since wins, this computer when both did. `ApplySignIns`
  checks each row's hash again before it writes, so a renewal in between
  is kept. A computer with the tick off sends no sign-ins, so the others
  remove nothing for it; turning it off, a Restore or a replaced server
  file clears the record of the last sync. An account used on two computers
  can be signed out on one when the other renews its refresh token, and the
  form says so (`TestMergeSignInsTheLaterRenewedStays`,
  `TestMergeSignInsRemovedOnlyWhenTheOtherSideRemovedIt`,
  `TestApplySignInsKeepsWhatChangedSinceTheMerge`, `sync-signins.test.cjs`).

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

The hooks go the same way (`library.Bundle.Hooks`): each computer writes
them into its agents beside the user's own hooks and records its own
entries, an older bundle with no list keeps this computer's, and a library
with none hashes as before (`TestSyncHooks`, `TestNoHooksHashAsBefore`,
`TestBackupFileCarriesHooks`).

billion-context's own config, `billion-context.json` (`library.Bundle.Bili`,
see [library](library.md)), goes in the library part by sync and backup file
alike: the text as written, comments kept, with magpie's summary recipe at
the canonical gateway address, put back at this computer's gateway when it
is written, so two computers on different ports don't trade it back and
forth. A file bili can't read, or one over 1 MiB, is not carried, so it
never arrives as empty; a bundle without it leaves this computer's alone.
Before a carried file replaces a different one here, this computer's is
kept with the backups. Its credential store, `<config>.summary-credentials.json`,
is never carried: the computer that writes magpie's recipe saves its own
gateway key there. A file naming a DSN is left out of a keyless backup
file (`WithoutSecrets`). A library with no such file hashes as before, and
running bilis are asked to reload after one is written
(`TestBiliFileCarried`, `TestBiliFileNotCarriedUnreadable`,
`TestBiliFileDSNNotInKeylessBackup`, `TestBiliFileInLibraryPart`). A sync's preview names it as
the file `billion-context.json`, added, changed or removed
(`TestBiliFileInPreview`).

## Verification

```sh
go test -tags nogui ./internal/davsync
go test -tags nogui ./internal/cli -run 'TestGitHubCmd|TestWebdavCmd|TestS3Cmd'
node --test internal/gui/tests/github-sync.test.cjs internal/gui/tests/s3-sync.test.cjs internal/gui/tests/sync-other-kind.test.cjs internal/gui/tests/sync-follow-server.test.cjs
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
