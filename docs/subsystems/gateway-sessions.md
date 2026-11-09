# Gateway Session Aggregation

The Sessions API combines file-backed native sessions with a read-only
projection of gateway usage records. A gateway session identity is exactly
`AgentOf(record.Agent) + record.Session`, where the session is the one the
client sent (`X-Magpie-Session`, or its own session header), whatever it starts
with. The response returns that id in `X-Magpie-Session`; listed browser
origins can read this header through CORS. magpie never makes up an id for a
request without one, so such a request has no session and its text is not
recorded: text kept under an id nothing lists couldn't be opened. Prompt text,
caller keys and network addresses are never used to guess conversation ownership,
with one exception, Qoder's below, where the prompt is matched against the
session files the agent itself wrote.
So the configs magpie writes have each agent name its session: Pi's provider
block sets `compat.sendSessionAffinityHeaders` (`x-session-affinity` on Chat and
Messages; Responses sends `session_id` anyway), and omp 16.0.6 and later gets
`compat.promptCacheSessionHeader: x-session-affinity`. A user's own
`sendSessionAffinityHeaders` is kept (`piBlockKept`). Verification:
`TestPiNamesItsSessionToTheGateway`, `TestOmpNamesItsSessionToTheGateway`.
Older ledger records without `Session` remain excluded. The projection is
computed from the usage ledger and is not persisted. Window projections and
routing summaries reuse versioned log snapshots; appends and rewrites invalidate
them, and price/provider changes also invalidate priced projections. Single-session
lookups use the cached identity index. Native exclusions are applied per caller.

WorkBuddy and CodeBuddy Code name their conversation in `X-Conversation-ID`.
The gateway reads it as a native session header, after the existing native
headers; `X-Magpie-Session` still overrides the recorded session without
changing its native identity. The per-turn `X-Conversation-Request-ID`,
per-message `X-Conversation-Message-ID` and `X-Parent-Conversation-ID` do not
identify the current session. This lets Routing group requests by the same id
the native WorkBuddy session reader already knows; old requests without a
recorded session remain unnamed. See `TestWorkBuddySessionHeader`,
`TestWorkBuddySessionThroughGateway` and `TestRemoteMagpieKeepsTheSession`.

Qoder's CLI (qodercli, qoderclicn) named its session in `session-id` up to
1.1.81; 1.1.82 sends no session at all (coderlee233 on Discord). For a request
from Qoder or Qoder CN (by its agent token) on this computer that names no
session, `qoderSession` ([`qoder_session.go`](../../internal/gateway/qoder_session.go))
finds the session in Qoder's own folder (`sessions.QoderDir`): of the session
files written in the last 24 hours (`projects/<project>/<id>.jsonl` and a
recent one's `<id>/subagents/agent-*.jsonl`, whose lines name the parent
session), the one whose typed prompts (`user` lines with `humanInput`, or
`origin.kind` human) appear most in the request's user text, Qoder's
`<system-reminder>` blocks left out; of equal ones, the file written last. A
turn holds every prompt of its session, a subagent's turn the Agent tool's
prompt, a title call the latest prompt or a transcript, a suggestion call
"User: …". The id goes on the request as `session-id`, and in the caller
passed to a remote magpie, so it is recorded, titled and grouped as 1.1.81's
was. Files are read incrementally (whole lines only) and listed at most once
a second, again at once when nothing matches; a side call (no tools) nothing
matches is held up to 600 ms, since Qoder asks the first title as the prompt
is sent, before it writes the file. A request that names a session is left as
it came. Verification: `TestQoderRequestsFindTheirSession`,
`TestQoderSamePromptTwoSessions`, `TestQoderFirstTitleBeforeItsFile`,
`TestQoderSessionThroughGateway`, on qodercli 1.1.82's own requests and files
(`testdata/qoder-1.1.82`).

DSH names its session in `x-deepseek-harness-session-id`, read the same way
after the others, and marks a request that compacts its context with
`x-deepseek-harness-compact` (#2505). `callKind` records such a call as
`compact`, the Requests list's Compaction; a value of `0`, `false`, `no` or
`off` is no mark, and a request without one is never taken for a compaction
by its prompt. `passOnCaller` sends the mark on to a remote magpie beside
the session, and no vendor is sent either (`TestDshMarksSessionAndCompaction`).
Both are read on Messages and Responses as on Chat Completions, which a
user's own llm-deepseek row pointed at magpie relies on (#3298,
`TestDshMarksOnEveryRoute`).

When the same identity is present in a native session, the native session wins
and the gateway projection is omitted. A native session can stand for more
than its own key (`sessions.GatewayKeys`): Raft's (slock's raft-computer)
agents run Pi in their folders under `$SLOCK_HOME` (else `~/.slock`,
`%USERPROFILE%\.slock` on Windows), `agents/<id>/.pi-sessions/` and
`.builtin-sessions/`, Pi's SDK files read as agent `raft`
([`raft.go`](../../internal/sessions/raft.go)). Raft's Pi reads the user's
own Pi folder, so through magpie it says it is Pi, and a Raft session is
the gateway's `pi|<id>` too: in `MergeExternal`, the stats' native keys,
the GUI's list and Via, and `NativeIDs` for `pi`. Raft's sessions are
listed, read and deleted as Pi's (one file to the trash, nothing beside
it; Raft starts afresh without it), with no resume command, no carry and
no trace export (the gateway's Pi identity has those calls). Verification:
`TestRaftSessions`, `TestRaftIsPiToTheGateway`, `TestRaftDeleteRestore`,
`TestRaftSlockHome`. An omp session stands for its subagents and advisor
too (`Session.Subs`, `subIDs` in
[`sessions.go`](../../internal/sessions/sessions.go)): each one's file in
the session's artifacts folder (`Kid.jsonl`, `__advisor.jsonl`) has a
header id of its own, and omp sends that id, not the main session's, in
`x-session-affinity`. The file's calls are counted in the main session, so
the gateway's `omp|<sub id>` is its own in the same places as Raft's `pi|`
key, and is never a second session counted again (Jumy, Discord 10-30:
`TestOmpSubagentsAreTheSessionsToTheGateway`,
`TestOmpSubagentCountedOnceWithTheGateway`). A Claude Code session's
copies (`Copies`) are among its keys the same way. A ZCode session stands
for its bare id too: a request names it by the id its database gives with
a prefix stripped (`sess_…`, a subagent's `sess_subagent_agent_…`:
`zcodeBareID`), its own as well as its forks' and subagents' (their files
are in its group, `Subs`, and `idInFile` reads their ids). Without it a
ZCode session whose files are here was a row under its folder and again a
row under Seen through the gateway, and its spend counted twice
(flandern1211_58540 on Discord, #5754;
`TestZCodeGatewayRowIsTheSessionItself`). `requestIDs` is the one place
that says which ids a request names a session by: `GatewayKeys` (the
list, its spend, the stats' native set, `viaOf`, the management
page's search) and `NativeIDs` (its count and delete) both read it. A Claude Code
session Claude Desktop's Code tab ran is in Claude Code's folder, and only its lines' `entrypoint`
(`claude-desktop`, `claude-desktop-3p`) tells it apart: its parse keeps the
entrypoint of the line its folder came from (`state.Entry`), and the
session is listed under `claude` with `App` `claude-desktop`. It is the
gateway's `claude-desktop|<id>` too, as the gateway knows the Code tab by
Desktop's user agent, in `MergeExternal`, the stats' native keys, the GUI's
list and Via. Usage counts it under Desktop, as its calls are: the stats'
rows and its summary carry `claude-desktop`, its key stays `claude:<id>`,
and Usage › Sessions files it under its `app` (`TestStatsDesktopCodeTab`,
`usage-sessions-desktop-app.test.cjs`). `NativeIDs` for `claude-desktop`
doesn't read Claude Code's files yet (#4333). On `/api/sessions/manage`,
in its
counts as in its list, and on `/api/sessions/delete`, a native session is any
session file the agent keeps, one nothing was said in too, with no limit
(`sessions.NativeIDs`); one pass over the agents' files (`sessions.Manage`)
reads only the agent shown and the agents with gateway sessions (#2776).
It answers before a first read of a long history (hey_boyce on X: the page
stood on its placeholder rows on its first open). A file it would read whole
(no parse kept, an older index, a file written over, or a Claude Code or
Codex parse that can't be read on after a restart) is read behind the
answer once more than `quickBytes` (32 MB) of them is waiting; until then its
row stands on the parse kept, or on a glance at its first 256 KB (id,
folder, first prompt, start) and, for Claude Code, the titles in its last
64 KB, with its last activity the file's time. Such a row has `pending:
true` and no counts, the page asks again every 1.5 s until none is, and a
glance is never saved in the index (`savable`) nor taken by a refresh as a
parse (`parsed`). See
[`internal/sessions/manage_quick.go`](../../internal/sessions/manage_quick.go),
`TestManageAnswersBeforeFirstReads` and `sessions-pending-fill.test.cjs`.
A Claude Code session's folder is its first line's `cwd`, unless
Claude Code's `/cd` moved it: the file goes to the new folder's project
directory with a `{"type":"relocated","relocatedCwd":…}` line, while the
lines before it keep the old `cwd`, and the last such line names the folder
(`ccRelocated`; a glance reads it in the file's last 64 KB too). A parse
saved by a magpie that didn't read those lines (`state.CdRead` unset) whose
folder isn't the one the file is in is read again (`ccStale`). Magpie's own
project move rewrites `relocatedCwd` and judges the session by its last
move. See `TestClaudeCdListedUnderNewFolder` and the fixtures in
`internal/sessions/testdata/claude-cd` (Claude Code 2.1.316's own lines,
usrname1833 on Discord).
Gateway projections have no path,
resume command or terminal action: the agent's own files of them are not on
this computer. Their rows carry `gateway: true`; the Sessions page groups them
under "Seen through the gateway" rather than "No folder", and their size is the
text magpie recorded of them (`sessions.GatewaySizes`), shown only when there
is some. The overview's top summaries (`MergeExternalStats`) carry
`gateway: true` too, and Usage › Sessions names such a row "Gateway session"
where a folder would be, with "(prompt not recorded)" when no text was kept,
so it doesn't read as a native session with an empty prompt (#2285: a Pi
subagent whose session file is in a temp folder). Verification:
`TestTopSessionsMarkWhatOnlyTheGatewaySaw`, `sessions-overview.test.cjs`.
`/api/sessions` makes a row only of the gateway sessions its limit can
show: `usage.EachGatewaySession` walks the projection newest first without
a copy, `listedExternal` stops at the limit, and `sessions.EachMerged`
makes each row when the merge reaches it. Every one of ~10k gateway
sessions past the natives' cap was copied three times and made a row on
each poll while a session grew. Verification:
`TestSessionsListMakesOnlyTheGatewayRowsItShows`,
`TestEachMergedMakesOnlyTheRowsItReaches`. Their
models, token totals, start/last timestamps and effective prices come from the
same usage records used by Usage and stats endpoints.

A model the user counts under another id (`usageAliases` in settings.json,
`magpie model alias`; [`provider.UsageAlias`](../../internal/provider/usagealias.go))
is merged after pricing, never before: `buildGatewaySessions` prices each
record by its own model and keys its day and model rows by the alias, and
`sessions.List`, `Get`, `Find` and the stats (`aliasStats` in
[`alias.go`](../../internal/sessions/alias.go)) merge a native session's rows
the same way, into new slices so the memo is never changed. The alias is read
before any lock, and an unreadable settings.json keeps the last aliases read.
Requests, the Requests ledger and model lists keep each call's own id.
Verification: `TestUsageAliasCountsTwoIdsAsOne`,
`TestSessionsCountAliasedModelsAsOne`, `TestAliasStatsLeavesItsInputAlone`.

The Usage page can clear its figures (CrazyMouse_TT on X): a model's row's
"…" has "Clear usage…", and "Clear all usage…" sits under the lists. Each
asks first, then `POST /api/usage/clear` adds a mark to `usageCleared` in
settings.json ([`settings.ClearUsage`](../../internal/settings/settings.go)):
a provider and model as the Models list names them (the alias, the
provider's shown id), or neither for everything, and the time in Unix
milliseconds. usage.jsonl is never rewritten. The usage pages leave out
each call at or before a mark that matches it
([`cleared.go`](../../internal/usage/cleared.go)): the Overview
(`summarizeFrom`), and the Requests page, its heatmap and the CSV through
`Filter.keepsRow`; the request index keys its meta on the marks
(`clearedKey`), so a cached page is read again. Gateway keys' spending
limits, the allowances' window holds and Codex's reads go by `usage.Visit`
and the raw log, so they still count every cleared call: a clear never
lifts a limit, and the confirmation says so. The marks ride the settings
part of a sync, so a clear holds on the computers it syncs with, over
their shared days and the agents' session-file rows too, and a later clear
of the same model or of everything takes an earlier one's place. Usage ›
Sessions is not cleared. Verification: `TestClearedUsageLeavesThePages`,
`TestClearUsageKeepsOneMarkAModel`, `TestUsageClearRoute`,
`usage-clear.test.cjs`.

Claude Desktop gateway rows can have a local name even without a CLI
transcript (#1675). [`DesktopTitles`](../../internal/sessions/desktop_titles.go)
reads `claude-code-sessions/*/*/local_*.json` and
`local-agent-mode-sessions/*/*/local_*.json` in Desktop's data folders
(`DesktopDataDirs`, the same `desktopdir.Everywhere` the session readers use,
Claude-3p and Windows' MSIX folders included). The record's `title` names the
CLI ids Desktop's own LocalSessionManager counts as the session's: its live
handles `cliSessionId` and `unarchivedCliSessionId`, its lineage
`preClearCliSessionId` and `priorCliSessionIds`, and, with no `cliSessionId`,
its `sessionId` without `local_`. Prior runs share the current name but remain
separate usage identities. A live handle takes precedence over a lineage one,
and conflicting names at the same rank are unknown. A record that is there but
doesn't read or parse (Desktop midway through writing it) keeps its last
parse; one gone takes its names with it; with none, the row keeps the recorded
first-prompt title or its id. Files are read through `filememo`, and the names
stand for a second (`desktopTitlesHeld`) before the files are looked at again,
so a list that assembles hundreds of native Desktop sessions stats them once
and a rename in Desktop shows within a second. The names reach the native
Desktop sessions (`assemble`), every gateway list (`MergeExternal`), the stats
summaries (`MergeExternalStats`), Routing's `/api/gateway/session-titles`
(`Titles`) and the Context page. It does not grant resume or native transcript
capabilities to a gateway row. Verification: `TestDesktopSessionTitles`,
`TestDesktopSessionTitleIdentity`, `TestDesktopSessionTitleHandles`,
`TestDesktopSessionTitleInStats`, `TestDesktopTitlesReadOncePerList`,
`TestDesktopSessionTitlesOnPages`.

A ZCode request names its session without the `sess_` prefix ZCode's
database gives it, so `Titles` looks a `zcode:` key up under both and
answers under the id asked for (#5195). Verification:
`TestZCodeTitlesByRequestID`.

Routing's stage names the session an account is serving (connor_yang on
Discord): a row answering, or that answered, the request shown adds that
request's session title after its status, on one line cut short with the
whole line under the pointer. The title is what the list's By session heads
use, from the same `/api/gateway/session-titles`: `sessions.Titles` by
agent:id for every agent but Codex, Codex's name index by thread id (its
parent's, for a subagent Codex names one for). The page keeps both by session
id (`otherTitles`, `codexNames` in
[`client.js`](../../internal/gui/assets/plugins/routing/client.js)), since each
trace update brings its routes anew; a session not seen yet is asked about
once (`askUnnamed`, skipped when a read since has taken it), and the rest
with the list's 15-second refresh, never on the stage's per-second passes. A
names reply silent on a Codex session (unknown to the index, or the index
unread) leaves the title the trace brought, and only a reply that names it
changes it. A session with no title says nothing more, and
neither does a Codex request whose parent was only matched by its prompt
(`parentMatched`), since that match is a guess. Verification:
`routing-session-title.test.cjs`.

A By session head reads as its title (flandern1211_58540 on Discord: a
folder and an id told nothing): the agent, then the session's title on one
line (whitespace folded), then its folder in a quieter `.dir`; with no title
the folder takes its place, and with neither the id. The tooltip keeps the
title, the folder and the id. Where `sessions.Titles` and Codex's name index
find nothing, the same endpoint falls back to the first prompt the gateway
recorded for that session (`sessions.GatewayTitler`, the title its list
shows), looked up under the agent the page names and the one the gateway
records it under (`usage.AgentOf`): `withGatewayTitles` and
`codexReadableTitles` in [`trace.go`](../../internal/gui/trace.go). Codex
ids the index doesn't name go to `sessions.Titles` as `codex:` keys first,
so a rollout's own first prompt names it. Nothing is read that the page
couldn't already show on the Sessions page, and nothing leaves the local
GUI. Verification: `TestSessionTitlesFallBackToFirstPrompt`,
`routing-session-folder.test.cjs`.

A gateway projection can be deleted (lc on Discord). `sessions.DeleteGateway`
moves its recorded text folders (`gateway-conversations/<date>/<identity hash>`)
into magpie's session trash under `trash/sessions/gateway/`, with a note
marked `gateway`, and adds `{agent, id, last}` to
`gateway-sessions-hidden.json` in the config directory. `/api/sessions` and
`/api/sessions/manage` leave out a hidden session up to that last request; a
newer request lists it again. The usage ledger is not changed, so Usage,
`/api/sessions/one` and the stats endpoints keep what it spent. A session with
a request in the last minute is refused as active. Restore moves the text back
(items that expired or were cleared meanwhile are skipped; a session recorded
again on the same day is refused rather than overwritten) and unhides it; it
accepts only items that are that session's identity folders in the store.
Delete forever keeps it hidden. The recording's promises hold in the trash:
the hourly cleanup erases trashed gateway text past the seven days, "Delete
saved conversations…" erases it too (the notes stay, so Restore can still
list the session), and `HasGatewayConversations` counts it.
Implementation: [`internal/sessions/gateway_delete.go`](../../internal/sessions/gateway_delete.go),
`Restore` in [`internal/sessions/manage.go`](../../internal/sessions/manage.go), and
[`internal/gui/sessions_manage.go`](../../internal/gui/sessions_manage.go).
A deleted Codex session also leaves the Codex app's list (zhuddan on Discord).
The app (ChatGPT.app's Codex, 26.x) lists threads from its own catalog,
`<CODEX_HOME>/sqlite/codex.db` (`codex-dev.db` for a non-prod build), table
`local_thread_catalog`, and drops a row only on the app-server's
`thread/deleted` or `thread/archived`; after its first full sweep it takes in
only threads newer than its watermark. Delete removes the local host's rows of
threads whose rollout is gone from that Codex folder's `sessions` and
`archived_sessions` (named by Codex's state database, neither the file nor its
packed twin there; a file that can't be looked at counts as there) and raises
`catalog_revision`, in one `BEGIN IMMEDIATE` transaction that waits for the
app's own. The removed rows go into the trash note (`codex_catalog`), and
Restore puts them back. `/api/sessions/manage` does the same once a run
(`TidyCodexCatalog`) for threads deleted before this. Implementation:
[`internal/sessions/codex_catalog.go`](../../internal/sessions/codex_catalog.go);
verification: `TestDeleteForgetsCodexAppCatalog`,
`TestTidyCodexCatalogForgetsEarlierDeletes`,
`TestTidyCodexCatalogKeepsWhatItCantJudge`, with schemas from Codex 26.1023 in
`internal/sessions/testdata/codex-app/`.
All-time statistics include gateway history older than native files. Calendar
activity and output totals use each session's actual request dates, including
intermediate dates; extending the range rebases existing native calendar indices.

Implementation: [`internal/usage/gateway_sessions.go`](../../internal/usage/gateway_sessions.go),
[`internal/sessions/external.go`](../../internal/sessions/external.go), and
[`internal/gui/sessions.go`](../../internal/gui/sessions.go).

## Which agents' sessions magpie reads

An agent's own session files are read only when the user allows that
agent (RustCats on X: a new install showed every agent's history before
anyone agreed). `sessions.Reads(agent)` in
[`access.go`](../../internal/sessions/access.go) is the one gate, from
`settings.SessionReads` (`Asked`, `All`, `Agents`; an agent named in
`Agents` wins, the others follow `All`). Every lister asks it at its
entry (`ccFiles`, `codexFiles`, `openCodeFiles`, `piFilesIn`, ... down to
`geminiTraceFiles`), the aggregators filter by it again (`allFiles`,
`callFilesWith`, `callSourcesWith`, `traceLineFiles`, `wslFiles`, and
`DesktopDataDirs`, which Usage's Desktop readers take), and the readers
that take a session already listed (`TranscriptOf`, `ContentOf`,
`SearchSession`, `SubagentTranscript`, `CarryOver`, `ReadCallSource`)
return `ErrNotRead`, so a cache from before a switch was turned off is
read no further. So the Sessions page and list, stats, the overview, the
call and trace indexes, Usage's agent-file calls and the Codex and Pi
title readers all go through it.

`SettleReads` decides once, for an install with no `SessionReads`: one
that kept a sessions cache (`sessions.json`) or a call cache
(`calls-v*`) read sessions before, and keeps reading every agent
(`Asked`, `All`); a fresh one reads none, not asked. The decision is
saved, so a cache written later never turns it into "all", and a value
already saved is never replaced. `settings.save` keeps the stored value
and sync and restore keep it (`KeepOwn`); only `SetSessionReads` writes
it. A settings.json that can't be written leaves this run reading
nothing. In a test binary nothing settled reads everything and nothing
is written (`readsAllInTests`).

`/api/sessions/reads` (GET; POST `{all, agents}`) in
[`sessions_reads.go`](../../internal/gui/sessions_reads.go) gives the
choice and the agents found (`sessions.Found`, by a stat of each agent's
folder, nothing in it read; WSL distros are not looked into, Allow all
covers them), and `sessions/manage` carries the same as `reads`. While
`asked` is false the Sessions page shows the consent (`consentCard` in
the sessions plugin): each agent found with a switch, off; Allow selected
saves `all: false` with each agent picked or not, Allow all `all: true`.
Settings › Privacy, *Agents' session history* (`renderSessionReads` in
the sessions plugin's `settings.js`), has *Agents found later* (`all`, sent with each listed agent
at the value its row shows) and a row per agent.
`magpie sessions read [on|off all|<agent>…]` shows and changes the same
choice from the CLI (`sessionReadsTo`); while it is not asked,
`magpie sessions` says so on stderr and the TUI's empty Sessions page
says how (`sessNothing`). Tests: `TestSessionsRead`,
`TestSessionsNotAskedSaysHow`.

In gateway mode (`magpie web`'s page, [`gatewaymode.go`](../../internal/gui/gatewaymode.go))
no agent's own session files are read, whatever `SessionReads` says
(paultian on Discord: a server put in gateway mode with agents installed
listed its own sessions beside those other computers sent). The gui sets
`sessions.OwnOff` to `ownSessionsOff`, and `currentReads` answers "none"
while it holds, so `Reads`, `allowed` and every route above (the list,
`one`, `transcript`, `markdown`, `export`, stats, the overview, search,
`manage`, delete, carry, the Codex provider move, and Usage's agent-file
calls) see no local session; `Found` and `Dirs` are empty, the
trash lists and restores only what the gateway kept, and moving Claude
Code's project folder is refused (`ErrNotRead`). Gateway-recorded
sessions (the ledger's projection and the recorded conversations) are not
behind this gate and still show, a local agent's that went through this
gateway among them. `ownSessionsOff` asks `settings.Shared()` and an
agents look kept for 30 s, as it is asked on every read, and when its
answer turns it resets the sessions' caches and the stats
(`forgetOwnSessions`), as `POST /api/settings/gateway-mode` does at once.
`/api/sessions/reads` says `gateway: true` then: the Sessions page asks
nothing, its empty state says the sessions shown are those of other
computers' agents that use this gateway, and Settings › Privacy leaves
*Agents' session history* out. A session is listed only when its
requests name one in a header (`sessionOf`: `X-Magpie-Session`, or the
agent's own, `sessionHeaders`), under the agent its User-Agent names
(`usage.AgentOf`, magpie's agents and plugins' agents; another client by
its product name).
The stats' `keys` name these sessions too (`MergeExternalStats`), since
Usage › Sessions lists only the keys it is given (linkaww on Discord: a
Docker magpie in gateway mode listed almost none;
`TestGatewayStatsNameTheGatewaySessions`,
`TestGatewayModeStatsNameTheGatewaySessions`). Usage › Context's Tuned for
you is left out (`/api/tune` says `gateway: true`, and Use and Undo are
refused): it replays this computer's agent files, so it said Claude Code
made 0 requests (`TestTuneInGatewayModeIsLeftOut`,
`context-tune-gateway.test.cjs`).
The app's windows are never in gateway mode. Tests:
`TestGatewayModeListsNoOwnSessions`, `gateway-mode-sessions.test.cjs`.

What is read either way: the gateway's own usage ledger, its
projections and the conversations it recorded (they are magpie's own),
the agents' config and sign-in files, Claude Code's and Codex's
`history.jsonl` last-used time and a session file's mtime for a quota's first use, and
Codex's newest rollout's last line before magpie restarts Codex
(`codexTurnOpen`), none of which lists or shows a session. Tests:
`TestFreshInstallReadsNoAgentUntilAllowed`,
`TestSettleReadsKeepsWhatWasThere`, `TestSessionReadsRoutes`,
`sessions-reads.test.cjs`.

## Local Gateway Conversation Recording

The Sessions page offers an explicit, default-off recording switch. Confirmation
discloses that prompts, replies and tool results can contain private files and
code. The switch is a card above the session list that says what is kept and
where (this computer only, up to 7 days and 256 MiB). It shows only where it
applies: on an agent whose list has gateway sessions (`gateway` on a
`/api/sessions/manage` row), in gateway mode, or while recording is on or text
is kept. Its "Delete saved conversations…" shows while
`/api/sessions/manage`'s `recorded` says some text is kept
(`sessions.HasGatewayConversations`, true when the folder can't be read), and
asks in magpie's own dialog first. `Settings.GatewayConversations` is machine-local consent:
`settings.SetGatewayConversations` updates it under the settings file lock.
Other Settings saves retain the latest stored consent under that lock, so an
older snapshot cannot re-enable recording after it is stopped. `KeepOwn`
also prevents sync/restore from enabling it.
`POST /api/sessions/recording` changes this switch; `clear: true` disables it and
erases only locally recorded gateway content (the store and deleted sessions'
text in the trash), leaving usage and native files.
The existing browser-mode authentication guard protects these APIs; this is an
administrator view, not per-caller-key access control.

[`recordConversation`](../../internal/gateway/conversations.go) wraps
`serveAgent` outside protocol translation and gateway middleware. It records
one client-facing exchange, not each retry, only under the session the client
named, using the same `usage.AgentOf` identity as the usage projection, so
every recorded conversation has a Sessions row that opens it. Codex's direct `/backend-api/codex/responses` relay is captured too.
Internal helper calls, token counting, embeddings, model listings, and compaction
paths that do not pass through these entry points are not recorded.
Recording adds no header and changes none, so routing and prompt-cache
affinity see the request as the client sent it. The existing protocol
readers handle Chat, Anthropic, Responses and Gemini; streaming replies use the
existing decoders, with completed Responses items retaining custom tool calls.
Only content actually sent through the gateway is available. Images are shown
as placeholders, their base64 taken out before the 8 MiB input cap
(`leanImages`), so a turn of pictures keeps its text (matrix0487 on Discord;
`TestFileIDConversationStillShows`); hidden reasoning, client-only operations and unsupported wire
items cannot be reconstructed. This is not a raw request archive or remote
session synchronization, and existing S3 archives are not imported.

[`SaveGatewayTurn`](../../internal/sessions/gateway.go) stores normalized,
scrubbed exchanges in one append-only `turns.jsonl` per
`gateway-conversations/<UTC date>/<identity hash>/` under
the Magpie config directory, using 0700 directories and 0600 files. Each line is
one bounded turn; legacy per-turn JSON files remain readable until they expire.
An incomplete final append is omitted from reads (marked truncated) and removed
before the next append; complete earlier lines remain available. No headers are
stored. Known secrets and secret-named JSON
fields are scrubbed, but arbitrary sensitive text is not guaranteed removable.
Response capture uses an 8 MiB temporary spool; input is bounded to 8 MiB too.
Normalized turns are bounded to 1 MiB on disk, individual parts to 64 KiB,
and each daily session file to 256 MiB. A full daily session file rejects further
recording that day. Hourly background cleanup removes expired buckets and the
oldest files above 256 MiB; this is a soft store limit between cleanup passes.
Saving a turn does not rescan the whole store, and background traversal and
sorting do not hold the save lock.
Capture, parsing or persistence failure does not change the model response;
filesystem failures are logged without conversation bodies.

Content older than seven days is excluded from reads. The serving gateway
cleans up expired UTC-day buckets at startup and hourly, including with recording
off; the daily layout means physical cleanup can lag the read cutoff by one day.
Disable-and-clear and writes share a lock and recheck the consent generation,
preventing an in-flight request from refilling a cleared store even if recording
is enabled again before that request completes.

`GET /api/sessions/transcript` still prefers a matching native session. Otherwise
it reads a known gateway session's last 128 retained exchanges. A full exact
previous-history prefix is omitted from the next request, while repeated new
messages are preserved. System/tool-definition context is separately collapsed
when unchanged. Compacted, edited or incremental contexts may repeat material;
they are never guessed into a single canonical history. The existing transcript
display bounds apply and truncation is marked. Empty/expired/missing content
has an explicit empty state and all gateway transcripts identify their source.
The page invalidates pending transcript reads on reload or clear. A completed
read updates the current open transcript after redraw, and an older response
cannot replace content fetched after clearing the store.

`GET /api/sessions/markdown` and `POST /api/sessions/export` take the same
choice: a native session is written from its file (`sessions.WriteMarkdown`),
and otherwise a known gateway session from what its transcript reads
(`sessions.WriteGatewayMarkdown`, "Recorded by: magpie's gateway", with a note
when turns or a part's end weren't kept). A magpie that others reach as a
remote magpie has no file of their agents' sessions, so this is the only way
it can export them. Verification: `TestGatewaySessionExportsMarkdown`.

A gateway session without a saved Desktop name is titled by what it was
first asked (YOU on Discord), not its `ses_…` id: `sessions.GatewayTitle` reads the first 8 complete turns of
the oldest recorded file of it in the 7 days kept, takes the first user text
part that isn't a `<…>` context tag, `# AGENTS.md` or an image, and makes it one
line (`untagged`). A finished file's answer is cached by path; a growing
`turns.jsonl` with no title yet is read again. With nothing recorded at all
(the store's folder missing) it costs one stat. `gatewayListed` applies it in
every list (`/api/sessions`, `/manage`, `/one`, the stats summaries); a session
with no saved Desktop name or recorded text keeps its id as title.

`GET /api/sessions/search?agent=&q=` finds an agent's sessions by what was said
in them, for the Sessions page's filter. The query is lowered and its spaces
made one (`sessions.NeedleOf`); under 2 runes it searches nothing. Native
sessions of an agent whose files are read (`HasTranscript`) are read from their
own file (`SearchSession`), first through a byte prefilter (`mayHold`: the
query's longest word, ASCII case folded without a copy, in 1 MiB chunks;
skipped for zstd files, for queries JSON may escape, and for a session whose
path isn't the file its conversation is in, `linesFile`: an OpenCode session's
info file or database, a Cursor chat's SQLite store, whose newest pages sit in
its `-wal`; `search_db_test.go`), then decoded only to
the first match in what the user or the model said. Of a Claude or Codex file
only the lines holding every word of the query are decoded (`readTranscriptOf`,
a part being one line's); Pi's tree is read whole. Without that, one 1.3 GB
Claude session took the whole 8 seconds; with it, all 58 of the owner's (3 GB)
are searched in 3–7 s on a loaded machine. Gateway sessions, and the native sessions of an agent whose files aren't
read (then `unread: true`), are searched in their recorded transcript
(`SearchGateway`). System prompts, tool definitions (`role: system`, `kind:
context`) and images are never matched. Sessions are read newest first by 4
workers at most, stopping at 200 hits or 8 seconds (`cut: true`, with
`searched` of `total`); the request's context cancels the read, so the page's
next query ends the last one. Each hit carries the match with up to 36 runes
before and 120 after, cut ends marked "…". The page waits 300 ms after the
last keystroke, aborts the request in flight, shows a title, folder, id or
model match at once, adds the sessions the search found with their snippet
(the match in a `<mark>`, built as text nodes), and says when it is
searching, cut short or limited to recorded text.
Implementation: [`internal/sessions/search.go`](../../internal/sessions/search.go),
`searchSessions` in [`internal/gui/sessions_manage.go`](../../internal/gui/sessions_manage.go),
[`internal/gui/assets/plugins/sessions/client.js`](../../internal/gui/assets/plugins/sessions/client.js).
Verification: `TestSearchSession*` (`TestSearchSessionReadsOnlyLinesWithTheWords`
for the line filter), `TestContainsFold`, `TestMayHoldSkipsAFileWithoutTheWords`,
`TestGatewayTitleAndSearch`, `TestSessionsSearch*`; browser
`sessions-search.test.cjs`.

Verification: `TestGatewayConversation*` (`TestGatewayConversationNeedsTheClientsSession`
and `TestGatewayConversationClientRequestPrefixedSession` for the two rules above), `TestGatewaySessionHistoryAndCalendar`,
`TestGatewaySessionsNativeWinsBeforeLimits`,
`TestGatewayStatsRebaseNativeCalendar`, `TestCORSKeyThroughTheServer`,
`TestSessionsDeleteGatewaySession`, `TestGatewayDeleteTrashExpires`, `TestGatewayPurgeKeepsItHidden`, `TestGatewayDeleteNothingRecorded` and
`TestGatewayRestoreRefusesForeignItems`; browser
`gateway-conversations.test.cjs`, `sessions-talk.test.cjs`,
`sessions-gateway-delete.test.cjs`, and locale checks.

## Carrying a session on in another agent

A session row's Continue in (`carryPick` in the Sessions plugin's
`client.js`) lists the other agents that can go on from it, from
`Session.Carry` (`carries` in
[`internal/sessions/carry.go`](../../internal/sessions/carry.go)). Nothing is
offered for a read-only or WSL session, or one with no file or folder. There
are two kinds:

- **From the file as it is.** oh-my-pi forks a Pi session: `Carry.Command`
  is `omp --fork <file>` run in the session's folder (#845).
- **Converted** (`Carry.Convert`, no command). A Claude Code session can go
  on in Codex, and a Codex one in Claude Code, when the other agent is
  installed (`agentThere`: its binary on the PATH or its config folder,
  checked at most every 30s). Listing converts nothing. When the user picks
  it, `POST /api/sessions/carry` `{agent, id, to, open}` calls
  `sessions.CarryOver`
  ([`carry_convert.go`](../../internal/sessions/carry_convert.go)). It writes
  a **new** session in the target's own store and returns `{agent, id,
  command}`, the target's resume line. The page copies it, or with `open`
  (macOS app only) opens it in the session terminal.

What a conversion carries:

- **The user's and the agent's words, in order.** For Claude Code, the
  chain of `parentUuid` from the last main-thread line back to the latest
  compact boundary. Sidechains, `isMeta` lines and Claude Code's own text
  are left out: slash-command echoes, `<system-reminder>`, the local-command
  caveat. For Codex, the `response_item`s, or a `compacted` line's
  `replacement_history` in place of what came before it. Codex's
  `<environment_context>`, `<user_instructions>`, AGENTS.md and developer
  messages are left out.
- **Pasted images**, as data URLs, into the target's own image block.
- **Shell calls**, in the target's native shape with their output: Claude
  Code's Bash and Codex's `exec_command`, `shell`, `shell_command` and
  `local_shell_call`. Any other tool (Read, Edit, Write, `apply_patch` as a
  custom tool, MCP tools) is written as the agent's own text: its name,
  input and result in fences. A result is cut at 64 KiB.
- **Not carried:** thinking, reasoning, signatures and encrypted content, and
  a compaction encrypted by OpenAI. Of compactions, only magpie's own
  summary is read.

What it writes:

- **Claude Code:** `<CLAUDE_CONFIG_DIR>/projects/<cwd as Claude Code names
  it>/<uuid>.jsonl`. Lines are chained by `parentUuid`, with the field order
  of Claude Code's own and `version` from the newest Claude Code session
  listed. Assistant lines have model `<synthetic>`, so the usage reader
  counts no calls and no cost for them. `claude --resume <id>` opens it.
- **Codex:** `<CODEX_HOME>/sessions/YYYY/MM/DD/rollout-<local
  time>-<uuidv7>.jsonl`. A `session_meta` carries no `base_instructions`;
  Codex uses its own. `model_provider` is what config.toml selects now
  (`CodexProviderIn`), so the thread is listed under the provider in use.
  `response_item`s are written, plus the `event_msg` user and agent messages
  the TUI shows as history. Codex indexes the file into its state database
  itself on resume, so magpie writes no index. `codex resume <id>` opens it.

The source file is only read. The new file is written to a temp file
beside it, synced and renamed, and is never written over an existing path.
`CarryOver` keeps the result per source path, target, size and mtime while
the new file exists, so asking twice for an unchanged session gives the
same one.

Not converted: Pi, omp, OpenCode, Kimi, Cursor and the other agents, and
sessions in WSL.

Verification: `TestCarryClaudeToCodex`, `TestCarryCodexToClaude` (real
Claude Code 2.1.310 and Codex 0.162.1 files in `testdata/carry`),
`TestCarryConvertOffered`, `TestCarryImagesAndCompaction` and
`TestCarryPiToOmp`; browser `sessions-convert.test.cjs` and
`sessions-talk.test.cjs`.

## Usage over the gateway, and subagents

`GET /v1/magpie/usage` and `GET /v1/magpie/usage/requests`
([`internal/gateway/usage_api.go`](../../internal/gateway/usage_api.go)) serve
the Usage page's summary and request ledger from the same builders as the
window's `/api/usage` and `/api/usage/requests`
([`internal/usageapi`](../../internal/usageapi/usageapi.go)), read-only and
without conversation text. They follow `/v1/magpie/quotas`' guard: this
machine, or another with an enabled gateway key, behind the same Host check.
A key that is held (`heldKey`: a budget, models or accounts) is refused the
summary and gets the ledger filtered to its own `CallerKey`, with its filter
lists cut to its own rows (`usageapi.OwnPage`): the ledger's facets are drawn
from every call in the period, so without the cut they would name other keys.
`TestUsageOverGateway` covers both, and a red when either cut is removed.
A held key is refused (`refuseHeld`, 403 `provider.KeyHeldCode`) the
quotas, their history, kept cards and refresh, and `/v1/magpie/concurrency`
(#1981); `/v1/magpie/route` tells it only routes whose `callerKey` is its
own. `Route.CallerKey` (and `CallerKeyName`, the key's name then; never
its secret) is set by `trace.beginFor` from the request's caller, so a
route begun without it is told to no held key. Both go out in the GUI's
trace and the routing history, and the Routing list's By key groups by
them (`TestTraceNamesCallerKey`, `routing-by-key.test.cjs`).
`TestHeldKeyToldNoAccounts` covers every one of these from loopback and
the LAN, for a key held each of the three ways and one held to nothing.
A held key whose owner set `access.Key.Quotas` (#2216) is told the four
quota routes through `scopeQuotas` (`quotas_scope.go`) instead: cards of
providers whose every catalog model `modelAllowed` lets it use, and of
those only accounts `AllowsAccount` lets it use, matched by the card's
`User` against `AccountIDs` (a name two accounts share must be allowed
both; a card no account is known by, only where the list names none of
the provider's). Remote cards (`From`) are never told. A refresh of a
card outside the scope is refused, one naming none reads the scope's
cards only, and a scope with no card answers the cards routes as held.
`TestHeldKeyToldItsOwnQuotas` covers each route for a provider-wide key,
a key with some of a provider's models, an account-held key, a provider's
only key, and a key told nothing, each red when its check is removed.

### Calls read from the agents' own files

A call an agent made without magpie reaches Usage only when its own files
are read (`sessions.Calls`, from `callSources` in
[`calls.go`](../../internal/sessions/calls.go)). The readers are Claude
Code's, Claude Desktop's and Cowork's, Codex's (and both in WSL), and,
read a file at a time (`wholeCallReaders` in
[`call_cache.go`](../../internal/sessions/call_cache.go)), OpenCode's
(#680), ZCode's, DeepSeek Harness's (`dsh`), WorkBuddy's, WorkBuddy
AI's (the international build's own `~/.workbuddy-ai`, read as
WorkBuddy's with its own models.json, listed and deletable under its own
name, and read once as WorkBuddy's when it is a link to `~/.workbuddy`:
`TestWorkBuddyAI`, `TestWorkBuddyAIGatewayIsItsOwn`,
`TestWorkBuddyAISessionsListedUnderTheirName`), CodeBuddy
Code's (#1779; its sessions are on the Sessions page too, from the same
`projects/<cwd>/<id>.jsonl` files read as WorkBuddy's, resumed with
`codebuddy --resume <id>` and deletable, #2657: `TestCodeBuddy`,
`TestCodeBuddySessionsListedUnderTheirName`), MiniMax Code's (#1605), Kimi Code's (#1607,
[`calls_kimi.go`](../../internal/sessions/calls_kimi.go)) and Pi's,
Raft's Pi agents' and omp's, here and in WSL (#1951,
[`calls_pi.go`](../../internal/sessions/calls_pi.go)). Each skips the
calls the agent made through magpie's own entry, which the gateway logged
with the account that answered: Kimi Code's are the ones whose
`usage.record` model begins `magpie/`. Kimi Code's calls are its
`usage.record`s in every agent's `wire.jsonl` under
`$KIMI_CODE_HOME/sessions` (else `~/.kimi-code`), protocols 1.4 and 1.5;
a fork repeats its parent's records, so each is keyed by agent, time,
model and usage and counted once. Pi's and omp's calls are the session
files' assistant messages, and the calls beside them that Sessions counts
too ("usage" and omp's "model_usage" entries, a compaction's, a branch
summary's and a tool result's usage, on the model and provider in use);
the provider `magpie` (or `dial`) is magpie's entry, a failed call has no
usage, and a fork's copy of its parent's entries (written before its
header) is the parent's. A Codex fork (/fork, a spawn_agent with the
parent's context) writes every line of its parent's rollout into its own
first, token_count events included, stamped at the fork's time; its
session_meta names the parent (`forked_from_id`), and a checkpoint the
parent's rollout holds (its total and last call alike) is the parent's,
on Usage and Sessions both (`codexRolloutOf`, `codexCheckpointsOf` in
[`codex_fork.go`](../../internal/sessions/codex_fork.go)). The copies
are the head of the fork only: its first turn the parent never had (a
`turn_id` of its own) ends them, so a call of the fork's own is counted
even when its numbers are those of one the parent made after the fork.
A `rollout-<n>` turn id is not a turn of the fork's own: codex-rs names
a turn that has no id of its own that way (its canonicalizer for older
rollouts, its thread-history builder), and a copied head can hold one
the parent doesn't (`ownTurn`).
The parent is
looked for in the same Codex home, by the UUIDv7 day of its id, then
every day, then `archived_sessions`; one not found leaves the fork read
as before. A ZCode side chat, fork or restored checkpoint copies its
session's messages with new ids and the originals' times and tokens,
each naming its original in `metadata.forkOrigin.messageId`; a copy
whose original is still in the database is not counted (`zcDB.copied`
in [`zcode.go`](../../internal/sessions/zcode.go)), one whose original
is gone is. The message without a `forkOrigin` is the session's own and
is counted: copies are marked by row, since a row's id is in its own
column and not in its data (v0.1.2847 marked them by that empty id and
left out every call of a fork that held one copy). Both came from Sky
(skyhuang1994): a ZCode side chat put a
10-31 session's 0.42B tokens on 10-31 again, and a week of gpt-5.4 read
5.1B tokens; then a ZCode fork's 11,409 calls of its own (3.49B tokens)
went missing. Verification: `TestCodexForkCountsOnlyItsOwnCalls`,
`TestCodexForkWithoutItsParentCountsTheCopies`,
`TestCodexForkThatWentOnWorkingKeepsItsOwnCalls`,
`TestZCodeCopiesCountedOnce`,
`TestZCodeForkThatWentOnWorkingKeepsItsOwnCalls`. OpenCode's fork copies the same way with
no pointer to the original, and is still counted twice (#5072).
The other agents the Sessions page reads (Cline,
Qoder, Grok, Droid, Cursor, Hermes, Alma) have no reader yet (#1607). The
Context page reads the gateway's own traces, so it shows none of these
calls. Verification: `TestMiniMaxCodeCalls`, `TestKimiCode*` (the
reporter's real wire files), `TestCodeBuddyCalls`, `TestWorkBuddyCalls*`,
`TestPiCalls*` (Pi 1.1.0's and omp 18.9.1's own files) and
`TestWSLSessionsListed`.

Antigravity CLI's (agy's) conversations are sessions too (RickyWei on
Discord): one per `brain/<conversation id>/` of `~/.gemini/antigravity-cli`,
read from the same `transcript_full.jsonl` its calls are (`agyFiles`,
[`agy.go`](../../internal/sessions/agy.go)). The steps give the times, the
first prompt (out of agy's `<USER_REQUEST>` wrapper), the tool calls and the
tokens, counted under the model `agy` since no step names one; a step agy
writes again replaces its tokens, read on or read whole. Its title and
folder are its row of `conversation_summaries.db` (`title`, and the first
local `workspace_uris` entry, `agyFolder` turning `file:///C:/…` into
`C:\…` on Windows). agy writes the title after the transcript, so the row
is the file's `rev` and a new title reads the conversation again; the
database is read again only when it or its `-wal` changed, and a failed
read keeps the rows read before. A row whose conversation has its
`conversations/<id>.db` and no transcript (#1718; that database is protobuf,
not read) is listed from the row alone: title, folder, `last_modified_time`,
no tokens and no conversation to open (`readable`). Resume is `agy --conversation <id>`, which
picks it up from any folder; when agy is set to use magpie it is the
agent's launch command (the gateway's variables and `--model`) with
`--conversation` after it (`sessions.AgyStart`, set by package agent).
Subagents (`parent_conversation_id`) and agy in WSL are not read.
Verification: `TestAgySessions`, `TestAgyTitleLater`, `TestAgyGrows`,
`TestAgyTranscript`, `TestAgyFolder`, `TestAgyRowOnly`, on conversations agy 1.3.5 and 1.4.7
wrote in a sandbox.

The Antigravity desktop app's stable and Insiders stores are read by
[`antigravity_app.go`](../../internal/sessions/antigravity_app.go):
`~/.gemini/antigravity/conversations/*.db` and
`~/.gemini/antigravity-insiders/conversations/*.db`. Both use the existing
`antigravity` session-reading choice and agent on Sessions and Usage;
`Found` detects an Insiders-only install, and `Dirs` lists each existing
store. Each conversation takes its title and workspace from the
`conversation_summaries.db` beside its own store, including later title
updates. Model calls reuse the app's read-only SQLite/protobuf reader,
including response folding and gateway matching. Insiders session ids
have an `insiders-` prefix in magpie, leaving stable ids unchanged and
keeping equal UUIDs in the two stores from sharing a session or call id.
The files themselves are not renamed. Sessions stay read-only with no
terminal resume command; encrypted conversation text is not read.
Verification: `TestAntigravityInsidersSessions` covers Insiders alone,
both stores with the same UUID, per-model tokens, call ids, summary refresh
and turning the existing reading choice off, using #2190's real step bytes.

A read of every call decodes each file's cached shard, and over a long
history that is tens of thousands of files (#2278: 68k shards, 415 MB).
Asks for the same calls while one read is under way wait for it and get
copies of its answer (`sharedCalls`), and reads run one at a time.
`AgentCalls` reads one agent's files only. The Sessions overview's Codex
model and effort (`usage.CodexModelEffort`) is built from Codex's calls
and the gateway's records alone, since only those name a Codex actor.
It is built once at a time per period, and an ask while it builds gets
the last answer for the same home. The answer is kept when usage.jsonl is
missing or grows during the build, and up to 8 periods are kept. The page
asks for an overview it is already waiting for only once (`sessOverview`
in `plugins/usage/page.js`). Before this, each ask during the gateway's
writes rebuilt the whole ledger, and the app grew to 85 GB. Verification:
`TestConcurrentCallsShareOneRead`, `TestAgentCallsReadsOneAgent`,
`TestCodexModelEffort*` in `codex_effort_shared_test.go` (the codex-only
answer equals the whole ledger's), and `sessions-overview-once.test.cjs`.

### Calls an app reports itself

`POST /v1/magpie/usage/requests`
([`internal/gateway/usage_ingest.go`](../../internal/gateway/usage_ingest.go),
#1874) takes calls that never went through magpie, from an app that sends
them straight to its vendor, so the Usage page covers them too. The body is
one record or an array of up to 5000 (32 MiB at most), in `usage.jsonl`'s
shape: `t`, `agent`, `provider`, `model` and `rid` are required; `in`,
`out`, `cache_read`, `cache_write`, `cache_write_1h`, `reasoning`, `ms`,
`ttft_ms`, `first_text_ms`, `flow_ms`, `status`, `cost`, `effort`,
`session` and `err` are optional.
Any other field, a quoted number, a negative or out-of-range count, a time
more than an hour ahead or before 2000-01-01T00:00:00Z refuses the whole
post with a 400 that names the record (`error.index`) and the field
(`error.param`), and nothing is written. A past `t` is taken as given,
however old, so a client's history can be backfilled.

- **Who may post:** the GET routes' rule, through the same `lanGuard`:
  this machine, or another with an enabled gateway key while shared. On
  top of that a browser page (`fromBrowser`) needs a key wherever it is,
  since `corsGuard` lets a localhost page through keyless (`Origin: null`
  needs a key there too, #2242), and a held key is refused. `TestUsageIngestAccess`.
- **Writing:** `usage.IngestReported`
  ([`internal/usage/reported.go`](../../internal/usage/reported.go))
  appends under the same mutex and to the same file as `usage.Append`,
  each row with `source: "reported"` and no caller key. A `rid` the log
  already has as reported, or one repeated in the post, is skipped. The
  set of reported rids is read from the log itself, so it survives a
  restart: the first post after one scans the log once, keeping only
  lines that carry the reported mark, and each later post reads only what
  was appended since. The reply counts `accepted` and `skipped` and gives
  each record's `index`, `rid` and `status`, so a re-run of a batch is
  safe.
- **Pricing:** a record with `cost` keeps it (`Record.OwnCost`, the
  `rowOwnCost` flag on a packed row). Without one it is priced at magpie's
  own prices, like a gateway row.
- **Speed:** `first_text_ms` and `flow_ms` (#2637) mean what they do in
  `usage.jsonl` (`Record.FirstText`, `Record.Flow`), so a reported row's
  speed is `usage.DecodeOf`'s, as a gateway row's is, on the server
  (`Row.Decode`, the totals' `DecodeN`) and in `page.js`'s `ledDecode`. A
  row that reasoned tells none without `first_text_ms`; `ThinkPace` and,
  with `flow_ms`, `HeldShare` tell a reply let go in a burst, as does
  `HeldWindowMs`: a window under a second after a wait over four times
  longer. `first_text_ms`
  may not be before `ttft_ms` (the first text is content too) or after `ms`;
  `flow_ms` is the middle 80% of the content's time scaled by 10/8, as
  `firstToken.flowMs` measures it, so it is bounded by `ms` × 10/8, not
  `ms`. `TestUsageIngestTellsSpeed` (the reporter's record), `TestUsageIngestTimesChecked`.
- **Showing:** reported rows count in every total. They also have a source
  of their own (`SourceReported`): `RequestPage.Reported`, `?via=reported`,
  and a fourth cell and a "Reported" badge on the Requests tab.
- **Nothing live:** a reported row only shows on the Usage page.
  `Record.IsReported` keeps it out of a gateway key's budget and spend
  limit (`budget.counts`), a subscription window's holds (and the log's
  start that decides whether a window can be reckoned, `firstOwn`), the
  Sessions page's gateway sessions, what a session was routed to (`Vias`),
  and Codex's sealed answers and remembered model. Nothing else is done
  for it: no OTel span, no rate limit, no rest, no alert.

`TestUsageIngest*` and `TestIngestReported*` cover these, including
concurrent posts beside the gateway's own writes, and a backfill of a full
5000-record batch landing on the right 7d/30d/all days.

### Calls to magpie's own MCP tools

An agent's call to a tool of one of magpie's own MCP servers is a row of
`usage.jsonl` too (ferock on Discord): the image server's
`generate_image`, `generate_video` and `generation_result`, vision, web
search, plugin tools and the router's. The row has `source: "mcp"`,
`kind: "mcp"`, operation `execute_tool`, the tool's name as `req`, the
server's name as `host` (`magpie-image`, `magpie-vision`,
`magpie-web-search`, `magpie-tools`, or the Library server a router tool is
of), the agent, the provider and model of the last gateway request the
call made, its duration, its endpoint (the server's path, `stdio` for the
Magpie Image server an agent runs) and, when it failed, the JSON-RPC
error or the `isError` result's first text, clipped by `keepMsg`. Its
arguments are never kept, not even masked.

- **Written by:** `serveMCP` for every `tools/call` (`callMCPTool` in
  [`mcp_calls.go`](../../internal/gateway/mcp_calls.go)). The call's
  context carries an `mcpUse`, which `appendUsage` fills with each model
  row's provider and model; the image server's jobs keep it, so a
  `generation_result` names the model of the job it waited on. A drawing
  still running when its call answers leaves that call's row without a
  model. The stdio Magpie Image server calls the gateway's `/v1/images`
  and `/v1/videos` itself, so it reports each call it answered to the
  gateway's image MCP server with the JSON-RPC method `magpie/toolCalled`
  (`gateway.MCPCalledMethod`, params `MCPCalled`: tool, ms, model,
  error); a tool not Magpie Image's is -32602. Behind the [MCP router](mcp-router.md) it sends none: the router's `tools/call` is the row. An older gateway that
  doesn't know the method just leaves the call unlisted.
- **Counted nowhere:** `Record.IsProbe` is true for these rows, so they
  ride the probe lane above: listed (`probeChunk`, kept as they are),
  never in a total, chart, budget (`budget.counts`) or `Load`. The model
  requests a call made are rows of their own and count as any. The
  index's provider hosts skip them (`sawHost`), as their host is a
  server's name.
- **On the page:** the Requests row has an `MCP` badge (`.src.mcp`,
  muted, no stripe) whose tooltip names the agent, tool and server and
  says the arguments are not kept; the Where cell is the provider and the
  server, the Requested cell the tool, and the status Succeeded or
  Failed. The Purpose filter names `kind:mcp` "MCP tool"
  (`purposeKinds`, `purposes.js`).

`TestMCPToolCallIsARequestRow`, `TestMCPHubCallIsARequestRow`,
`TestMCPCalledFromStdioIsARow` and the others in `mcp_calls_test.go`,
`TestToolCallsAreReported` (imagemcp), `TestMCPCallRowsListedNotCounted`
(usage) and `usage-ledger-mcp.test.cjs` cover it.

A request a provider plugin sends its vendor by itself (Devin's cache
keep-alive ping, plugins#592) never passes the gateway. The plugin
reports it through `input.magpie.usage` and `usage.pluginCall` writes it
as a row of agent `magpie` with the plugin's kind (`cache_keepalive`,
"Cache keep-alive" in `purposeKinds`). Unlike a probe it is counted:
priced and in every total (see
[provider-plugins.md](provider-plugins.md#a-request-a-plugin-sends-by-itself)).
`TestPluginKeepaliveIsCounted`.

### Requests magpie sends on its own

The provider editor's Test, Detect APIs and per-model detection, and a
model list's refresh, reach the vendor outside any agent's turn (neiko and
gys619 on Discord: a test left no trace). Each such request is one row of
`usage.jsonl` with `source: "probe"`, agent `magpie`, the provider's id
(the host for one not yet saved), its account, model, endpoint, status,
time and the error clipped to 500 bytes, and a `kind` saying what it was
for: `provider_test`, `api_detect` or `model_list`
([`internal/probelog`](../../internal/probelog/probelog.go)).

- **Marking:** `Provider.ownRequests` marks the context in `Test`,
  `TestModels`, `Detect`, `DetectModels`, `DetectDecide`, `refetch`,
  `fetch`, `List`, `fetchOne` and `fetchDue`; a kind set inside another
  replaces it, so the list a Test fetches is a model list. A request is
  written once: by `Provider.Do`, which then marks the context taken, or
  else by `probelog.Transport`, which `netproxy.Install` puts under
  `http.DefaultClient` for the catalog's list fetches. A Claude account's
  test (the Claude Code CLI) and the translated test (through the
  gateway) are one row each from their result (`NoteResult`). A plugin's
  model list runs in the plugin host and is not written. Only the host and
  path of a URL are kept, never its query (Gemini's `?key=`) or userinfo.
- **Counted nowhere:** the log's index keeps these rows out of its blocks
  (`logSnapshot.probes`), so `Visit`, `Load`, `Summarize`, a gateway key's
  budget, a window's holds, `Vias`, the heatmap, shared days, `LastSeen`
  and the observers (`Append` skips `observeLedger`) never meet them. The
  request page lists them as a chunk of their own (`probeChunk`) and adds
  them to no total, cell, group, chart or filter count; `?via=` other than
  all leaves them out.
- **A model list answered is no row:** magpie fetches lists by itself
  (start-up's `FetchNew`, the panel's and the Providers page's, and
  `KeepListsFresh`, which asks a Remote magpie every 10 minutes, and
  `KeepRemoteListsFollowed`, as soon as its list changes there), so a
  `model_list` row with a 2xx/3xx status and no error is neither written
  (`usage`'s `probelog.Sink`) nor read back from an older log
  (`quietProbe`, in the index's parse): ferock on Discord, a Requests page
  full of 0-token rows no agent sent. A list that failed is still a row,
  under the Failed filter too, as is every Test and Detect APIs. Older
  lines stay in `usage.jsonl`, read by nothing.
  `TestAnsweredModelListIsNoRow` covers it. A request that is only a step
  towards a list is no row either (`probelog.Taken`): an Antigravity
  account's `fetchAvailableModels` with its tier named, which Google
  answers 400 for a Google AI Pro account before the request without one
  lists its models (ferock4669 on Discord, a 400 "Model list" row beside
  every list). That tier isn't named again for a day (`tierRefusedFor`);
  the request without one is written down as any list.
  `TestAntigravityRefusedTierAskedOnceAndNoRow`.
- **Whose:** a row recorded under its host, sent before its provider had
  an id, is listed under the provider saved on that host now
  (`Renames.ProbeOf`), and with none on it, a deleted one's, under the id
  the log's calls to that host were made under (`logSnapshot.hosts`,
  `probeProvider`). A host two providers are or were on stays the host,
  lowercased (yyddbds and cybercaius on Discord).
  `TestProbesOfAProviderBeingAddedListItOnce` and
  `TestProbesOfADeletedProviderStayWithIt` cover it.
- **Showing:** the row's badge is the kind's name ("Test", "Detect APIs",
  "Model list", from `purposeKinds`), and its tooltip and the "magpie's
  own" badge's say it counts in no total (`probeWhy` in
  `plugins/usage/page.js`).

`TestProbeRowsListedNotCounted`, `TestTestWritesItsRequestsDown`,
`TestDetectWritesItsRequestsDown`, `internal/probelog`'s tests and
`usage-ledger-probe.test.cjs` cover these.
`TestReportedTouchesNothingLive` and `TestUsageIngestTouchesNoLimit` go red
when any of the live guards is removed. Browser:
`usage-ledger-reported.test.cjs`.

With 200k reported rows (a 47 MB log), 40 posts of 5000 took 0.4 s in all.
The first post after a restart took 0.45 s to read the rids back, and a
later one about 2 ms. The Usage page's first read builds the log index
(2.1 s, as it does for the gateway's own rows); after that, a page of all
took 27 ms and the all-time summary 215 ms.

A usage record's `Session` stays the conversation. A Claude Code subagent's
record adds `Subagent` and `ParentAgent`. Through the gateway they come from
the `x-claude-code-agent-id` and `x-claude-code-parent-agent-id` headers
Claude Code sends for a subagent's requests (`subagentOf`, read in
`appendUsage`); the main thread sends neither. A row read from the agent's own
files gets `Subagent` from the file's name, `<session>/subagents/agent-<id>.jsonl`
or `<session>/subagents/workflows/<run>/agent-<id>.jsonl`
(`sessions.SubagentOf`), so no cache version changes, and `ParentAgent` from
the `parentAgentId` of the `agent-<id>.meta.json` beside it, which Claude Code
writes for a subagent a subagent started (`sessions.SubagentParent`, through
`ccMeta`: read once a file; a meta file not read is unknown and read again). A gateway row matched
to a file row keeps the gateway's own. Both were checked against Claude Code
2.1.296 spawning a subagent from a subagent.

A folder these readers go into may be a link: a symlink, or on Windows a
junction, which Go lists as irregular rather than as a folder or a symlink.
Each reader that picks the entries it goes into (a Claude Code, Qoder or
Cowork project folder, a session's own folder and a workflow run's, a Hermes
profile, an Antigravity CLI conversation) asks `dirent.IsFolder`, which
follows such an entry with `os.Stat`; none of them recurses through one. A
walk root that is a link is walked where it leads, its files still named
under the agent's folder (`walkLinked`, #2233). `TestIsFolder` and the
`*BehindALink` tests make real links, junctions on Windows (#2268).

Codex session identity is read from native thread metadata, rather than inferred
from a shared session header or `Kind` (`x-openai-subagent`). The sessions API
adds `role` (`main`, `subagent` or `unknown`), and when native metadata
establishes them, `parent_session` and `root_session`. A child whose root is not
known keeps an empty `root_session`; Codex entries without explicit identity
metadata, gateway-only entries and conflicting identities set `role` to
`unknown` and do not acquire a guessed parent or root. These fields are omitted
for other agents, except a Claude-format branch (below). This is Codex-specific
(#1591).

Claude Code copies a conversation into a new session file when it forks one
(`--resume <id> --fork-session`, the Agent SDK's `forkSession`, `/branch`):
every message again, under the same uuids, with the new session id; after a
compaction the copy starts at the compaction's summary
([`cc_copies.go`](../../internal/sessions/cc_copies.go), `ccFamily`). Files
are grouped by their first message and their compactions' summaries. A file
whose last message and first message another file of the group holds is a
copy: not listed, its id in `Copies`, counted in the stats only for what the
keeper doesn't hold (`ccRest`). A file that holds what came before the
other's start (the compaction the fork started after) is never that one's
copy. What is left of the group went on apart and is one conversation: the
head is the file that holds the others' first message as they don't hold
its, else the one a `/branch` names in `forkedFrom`, else the earliest, then
the lowest key. Each other one is a branch: listed with `role` `branch` and
`parent_session` the head's id, its transcript (`TranscriptOf`, Markdown
export) is the head's from its start up to the last message the two share,
then the branch's own lines (`branchTranscript`), and the stats count only
its own lines. A search reads the branch's own file. Neo (#feedback, after
v0.1.1809): a client that ran each task in a fork of a compacted session
showed a row per task, each opening on the summary and that one task, while
the session itself was hidden as a fork's copy. See
`TestClaudeForksAfterCompactAreBranches`,
`TestClaudeSlashBranchAfterCompactIsABranch`,
`TestClaudeBranchNamesItsSession` and
`TestClaudeCopyBeforeACompactionIsCounted`; the fixtures in
`testdata/claude-forks` are Claude Code 2.1.312's own files.

A session whose agent keeps its subagents' conversations beside its own
lists them in `subagents` on the sessions API (`/api/sessions`, `one`, `manage`; #2261,
[`subagents.go`](../../internal/sessions/subagents.go)), only for agents with
a transcript, oldest first. Each has `id`, `title` (the task it was given),
`kind` (the agent it ran as), `parent` (the subagent that started it, empty
for the session) and `start`/`last`; its file is never sent.

- Claude Code, Qoder and Qoder CN: each `subagents/agent-<id>.jsonl` (and
  under `workflows/<run>/`), with `agentType`, `description` and
  `parentAgentId` from the meta file beside it (`ccMeta`). With no meta file
  (Qoder), or one naming no `description`, the title is the file's first
  user line (`ccTask`, at most 16 lines read). The file is read for it only
  then, and what was read, a task or none, is kept until the file's size or
  time changes; a read that fails keeps the last one. The size and time are
  taken before the read, so a line written during it is read on the next
  ask. `agent-acompact-<id>.jsonl` is a compaction's own run and is left
  out.
- OpenCode: a child session (`parent_id`, from the JSON store or
  `opencode.db`) is a subagent of its root, not a session of its own;
  `ocRoots` records its parent. Its title splits 1.x's
  `<description> (@<agent> subagent)`; 2.x titles it the description alone
  (task tool, opencode 2.0.25), and a default `Child session - <time>` is no
  title. OpenCode 2 names the agent in `session_v2.agent` (2.0.29), which
  is the kind when set, the title's `@<agent>` otherwise (`ocV2DB.agent`,
  kept once found; #2290). ZCode shares the parser but has no transcript,
  so lists none.
- Kimi Code: each `agents/agent-<n>/wire.jsonl`. 1.5's `subagent.spawned`
  record, written in the starting agent's wire, gives `subagentName`,
  `description` and `parentAgentId` (`kimiWireOf`, read again only when the
  file's size or time changes); 1.4 writes none, and the title is the
  subagent's first `turn.prompt`. In a subagent's own wire, a `turn.prompt`
  from a `system_trigger` named `subagent` is the task it was given and
  opens as its first user message, not folded context (`kimiTask`, #2290);
  other system triggers, and any in `agents/main`, stay folded.

These are read beside the parsed-session cache, not in it, so its version
is unchanged. Only the Sessions list and a session's own lookups build
them (`assemble`); the stats, the native-session keys, `Titles` and the
id lookups use `assembleListed`, which stops before resume, transcript and
subagents, so a stats poll reads no subagent's file.
`GET /api/sessions/transcript` takes `sub=<id>` for a
subagent's conversation; the id is looked up among the session's own
`subagents` (`sessions.SubagentTranscript`), never used as a path, and one
not listed is an error. See `TestClaudeSubagents`, `TestQoderSubagents`,
`TestOpenCodeSubagents`, `TestOpenCodeV2SubagentKind`, `TestKimiSubagents`,
`TestKimiSubagentTask`, `TestSubagentTaskReadOnlyWhenTheFileChanges`,
`TestSubagentWithMetaTaskIsNotReadForIt`, `TestStatsReadNoSubagentFile` and
`TestSessionSubagentTranscript`.

A session's listed `resume` line is made on every listing and reads no more
than the listing did. The line that runs when the user opens it, from the
GUI's Terminal or the TUI, is `sessions.ResumeNow` ([`codex_resume.go`](../../internal/sessions/codex_resume.go)).
Codex 0.166 resumes a thread only on the provider it was saved with (its
state-database row, else its `session_meta`): on another, `codex resume`
stops at `modelProviderMismatch`, and at `modelProviderNotConfigured` when
config.toml has no table of that provider any more, and no flag lets a
resume go on another provider (codex-rs `config_manager.rs`
`load_for_resume`). So a Codex thread saved on a provider other than the
one Codex is on runs as `codex resume -c model_provider=<its provider> <id>`
while config.toml has that table, or it is a built-in (`openai`, `ollama`,
…), and Codex runs it embedded, on its own provider; when the provider is
gone, `MoveCodexProvider` moves it to the one Codex is on first (its files
in magpie's trash), and it resumes there (mrten_95615 on Discord). A WSL
distro's Codex session is as listed. `TestCodexResumeOnItsProvider`; both
cases were resumed by a real Codex 0.166.0 in a sandbox `CODEX_HOME`.

The Sessions page draws only main sessions on top. A session's subagents are
folded under its row ("2 subagents"); unfolded, each is a row of its task and
kind, set in under the subagent that started it, and a click shows its
conversation (`sub=`). A Codex thread whose `parent_session` chain reaches a
listed session is drawn, as a session with its own details, Resume and
Delete, under its parent in the same fold; a loop of parents, a chain deeper
than eight or a parent not listed leaves it on top. A Claude branch is drawn
under its session the same way, and the fold names them apart ("2
branches · 1 subagent"). The folder's count and
checkbox cover the sessions under others. See `sessions-subagents.test.cjs`.

Codex keeps the existing session-title precedence. Only when that leaves the
title empty may a child thread use its parent's `spawn_agent` `task_name`,
matched by the same `call_id` and an `item_completed` event whose item has type
`SubAgentActivity`, kind `started`, the same `id`, and the child's
`agent_thread_id`. A legacy rollout (Codex's default history before paginated)
keeps that started activity as an `event_msg` `sub_agent_activity` instead, its
`event_id` the call id, and it links the same way. A link counts only from the
file of the child's own parent (`parent_session`): a forked child's file
repeats its parent's history, spawns of its siblings included, and that copy
neither names a child nor takes a name away from one.
The native top-level `session_meta.agent_nickname` is exposed separately as
`nickname` on list, one, manage and overview responses; it does not replace the
title. The parsed-session cache is versioned for nickname and task-title
metadata, and the Codex-call cache for compaction, so older parses are rebuilt.

`GET /api/sessions`, `/api/sessions/one` and `/api/sessions/manage` also return
`model_effort` for each Codex thread, grouped by the canonical Usage ledger's
`model` and effective reasoning effort. It carries that thread's input, output,
cached and cache-write tokens, reasoning tokens, cost, and whether every grouped
call was priced. The Usage ledger's canonical rows supply these values: a gateway
response paired to the native `response_id` contributes once with the gateway
row's `model`, effort, tokens and price, and identical local copies are folded
once.
`/api/sessions/overview` adds the same field to its `tokens`, `cost` and `active`
top summaries using the overview's matching local calendar range; `days=0` uses
all history. The existing `models` field and session statistics keep their prior
aggregation and meaning.
These are fields on the existing API responses; no endpoint or statistics
structure is added.

In the Usage request APIs, `session_role` classifies the canonical Codex actor as
`main`, `subagent` or `unknown`. For a confirmed identity, `session` is its root
thread ID; when the actor is known but its root is not, `session` is empty. An
unpaired gateway row keeps its raw `session` value and has `session_role`
`unknown`. The existing `subagent` and `parent_agent` fields are reused for
Codex: they carry the child thread ID and, when known and itself a subagent, the
parent thread ID. `native_session` stays the recorded native header. The new
`session_role` is omitted for other agents, whose existing field semantics stay
unchanged. Ambiguous or unpaired calls do not inherit a relationship from shared
session metadata.

`GET /api/usage/requests` and `GET /v1/magpie/usage/requests` expose Codex
`compaction` as `true` or `false` when a native Codex call establishes the
marker state, and omit it when there is no matching native evidence. Only a
call read from a `token_usage_record` (it has a `response_id`) establishes it:
Codex writes that record and a compaction's `compaction_response_id` together,
so a call told from `token_count` alone, in an older rollout, leaves it out
rather than saying `false`. A unique
`response_id` match adds this metadata to the gateway row while preserving that
row's model, usage, cost and kind.
Compaction is already included in Codex's total consumption, so this marker
does not add tokens or cost. This is Codex-only; Claude behavior and the POST
`/v1/magpie/usage/requests` ingest allowlist are unchanged.
