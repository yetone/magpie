# Memory

What the agents know from earlier conversations, kept once for all of
them: a preference the user states in Claude Code is known to Codex the
next day. It is read and written three ways: an MCP server every agent can
mount, a digest written into the instructions of the agents ticked under
Library → Instructions, and the Library's Memory tab.

## Source of truth

`memory.json` in magpie's config folder ([internal/memory/memory.go](../../internal/memory/memory.go)).

- One writer at a time across magpies (`filelock`), written through a temp
  file, the last good generation kept beside it (`lastgood`).
- A file that can't be read is unknown, never empty: nothing is written
  over it, searches fail, and `Digest` returns an error so the instructions
  keep what the agent has (`TestUnreadableFileIsNeverWrittenOver`,
  `TestCutShortFileReadFromItsBackup`).

A memory is one short text (`MaxText` runes) with:

| Field | Values | Meaning |
| --- | --- | --- |
| `scope` | `global`, `project:<key>`, `agent:<id>` | where it holds. A project is the git repository, keyed by its remote (`ProjectOf`, `RemoteKey`), so every clone and worktree shares it. An agent id has no `@wsl:…` suffix. |
| `kind` | profile, preference, rule, reference, fact, procedure, episode | how long it stays true and where it goes in the digest. A field of its own, not a tag. |
| `tags` | free words (`CleanTags`) | the user's own grouping. |
| `origin` | user, agent, imported, inferred | who wrote it. An agent's save never changes text the user wrote (`TestUsersMemoryNotChangedByAnAgentsSave`). |
| `status` | active, candidate, archived, superseded | only active ones are searched by default and digested. Candidates wait under "To review". |
| `pinned` | bool | first in lists and digests. |

`Save` keeps one memory per scope and text (`TestSaveKeepsOneOfTheSameText`)
and refuses empty or over-long text, an unknown kind and a bad scope
(`TestSaveRefusesWhatIsntAMemory`).
`Forget` archives; `Remove` deletes.

## Runtime paths

### MCP server

[internal/gateway/memory_mcp.go](../../internal/gateway/memory_mcp.go):
`memory_search`, `memory_save`, `memory_forget`, Streamable HTTP at
`MemoryMCPPath` (`/mcp/magpie/memory`), served like the search server
(`serveMCP`) but to the agents on this computer alone, as the hub gives it
(`HereOnly`): memory is what is known about this computer's user, and its
digest goes into every agent's instructions here, so another computer's
gateway key (which may be a friend's, and may use every MCP server unless
it is held to some) and a web page, even one Settings lists, are turned
away (`TestMemoryMCPGuarded`).

- The Library offers it in the market as `magpie-memory`
  ([market.go](../../internal/library/market.go)), run as
  `magpie mcp memory` ([browsermcp.RunMemory](../../internal/browsermcp/browsermcp.go)),
  which relays stdio to the gateway with the folder the agent started it in
  (`X-Magpie-Cwd`) and the client name from `initialize`
  (`X-Magpie-Mcp-Client`).
- Behind the [MCP router](mcp-router.md) the hub passes the caller in the
  call's `_meta` instead (`HubRootsMeta`, `HubClientMeta`;
  `TestMemoryMCPHubMetaNamesTheProject`).
- Without a folder the server can't name the project, nor the agent without
  a client name: such a save says so instead of guessing, and a save with
  no scope is kept for everyone (`TestMemoryMCPSaysWhatItCantKnow`).
- A save through MCP is active at once, origin `agent`.

### Digest in the instructions

[internal/memory/digest.go](../../internal/memory/digest.go) renders one
digest per scope within a budget (global 4000, project 1500, agent 1000,
the whole 8000 characters), pinned and longer-lived kinds first, no memory
cut in the middle. The text goes to every agent's vendor: it names neither
magpie nor any vendor.

The digest changes only when published, so an agent's instructions — and
the prompt cache built on them — change mid-session only then
(`TestDigestChangesOnlyWhenPublished`):

- a change the user makes in the GUI publishes at once
  (`library.PublishMemory(false)` from [internal/gui/memory.go](../../internal/gui/memory.go));
- `KeepLinked`'s loop publishes once a day for what agents saved meanwhile
  (`PublishMemory(true)` → `memory.PublishDue`, which writes nothing while
  memory has never held anything).

`Library.memoryDigest` adds the agent's digest (global, its own, and each
project's under the folders it was seen in) to the instructions of the
agents ticked under Instructions, unless `Instructions.NoMemory`
(`TestMemoryDigestInInstructions`). The Memory tab's switch sets it.

### GUI

[internal/gui/memory.go](../../internal/gui/memory.go):

| Route | Does |
| --- | --- |
| `GET /api/memory` | `{memories, projects, digest, agents}` |
| `POST /api/memory` | `{id?, …Input}`: `Edit` with an id, else `Save` as the user's |
| `POST /api/memory/remove` | `{ids}` |
| `POST /api/memory/status` | `{ids, status}`: `SetStatus`, in one write, as `Edit` by the user per memory (`TestSetStatusOfSome`, `TestMemoryStatusRouteActsOnSome`) |
| `POST /api/memory/digest` | `{on}` |

A reply after a change carries `problem` when publishing the digest
failed. The tab is in [plugins/library/client.js](../../internal/gui/assets/plugins/library/client.js)
(`renderMemory`), tested by `internal/gui/tests/library-memory.test.cjs`.
Its Select puts a box on each memory listed: a click picks one, a
Shift-click the range from the last one clicked, ↑ ↓ move between the
boxes (Shift picks as it goes), ⌘A / Ctrl+A picks every one shown, Escape
ends it. The bar under the list keeps (To review), archives (In use) or
restores (Archived) the ones picked through `/api/memory/status`, and
deletes them on a second click through `/api/memory/remove`; a search or
filter unpicks what it hides (RealZeroP27 on X,
`library-memory-select.test.cjs`).

### Distillation

`internal/memdistill` reads finished conversations into candidate
memories. It is off until switched on under Memory → Learn from
conversations; switched on, it looks back seven days (`Back`) and no
further (`State.Since`), unless a range is picked.

- How far back (`State.Days`, `SetRange`): 7, 30, 90 or 365 days, or All
  (`memdistill.All`, -1); 0 is the 7-day default, so a state from before
  the picker keeps its own `Since` until a range is picked
  (`TestRangePicked`). Every range reads from the latest 400 conversations
  (`Listed`).
- A backlog is read at once when the user asks (`ReadAll`, through
  `distillNow`): switched on, a model or a longer range picked, Read now.
  It reads every conversation due, one request at a time, and the row
  shows how far it has gone (`Progress`, `doing` of `of` in the view)
  with a Stop (`Stop`, `POST /api/memory/distill/stop`; switching off
  stops it too). A stop or a failure leaves the rest for the next run,
  and the row says how many (`State.Left`) (`TestBacklogReadAtOnce`,
  `TestStopEndsTheRun`). Before, every run read 10, and 400 took 40
  hours (RealZeroP27 on X).
- State is `distill.json` beside `memory.json` (`memdistill.Path`):
  the switch, the model, the reasoning level, the range, the
  conversations done, and the last run's time, counts, error and how
  many it left. A
  conversation's mark is dropped once it is before `Since`, or older
  than 30 days and past the oldest conversation listed; before that a
  conversation older than 30 days was read again every hour
  (`TestOldConversationReadOnce`). It is written through temp-and-rename
  under a file lock; an unreadable file runs nothing
  (`TestUnreadableStateRunsNothing`).
- `Keep` runs 5 minutes after the GUI backend starts, then hourly
  (`keepDistilling` in `internal/gui/memory_distill.go`). `Run` takes at
  most 30 conversations (`PerRun`, more than a busy hour brings) quiet for 30 minutes (`Quiet`) with at
  least two turns, and skips the gateway's own records, read-only
  agents, subagent sessions and its own agent (`memory`). One run at a
  time, in this process and across processes (`.run` lock).
- What is sent: the conversation's user and assistant text only (no
  tools, no reasoning), cut to 24k characters keeping the head and the
  tail, plus the memories already kept for that project, under neutral
  instructions that name neither magpie nor the vendor
  (`TestWhatIsSent`, `TestLongConversationCut`).
- The request goes through the gateway this process serves
  (`distillComplete`), as `x-api-key: magpie-memory`, so it is routed,
  masked and counted in Usage under the agent `memory`
  (`TestDistillCompleteThroughGateway`). With no model picked it uses
  `gateway.AutoVision()`. A reasoning level picked (`State.Effort`,
  `SetEffort`) is fitted to the model's levels and sent as thinking with
  room for it (`gateway.WithSideEffort`, as Vision and Searcher do);
  Default sends none. Run and the nightly tidy both use it
  (`TestEffortPicked`).
- Each proposal is saved as an inferred candidate (To review) with its
  conversation as the source and the quote it rests on; at most 5 per
  conversation. A proposal whose quote isn't in what the user wrote
  (letters and digits compared, `bare`) is dropped: on real
  conversations those were the assistant's own instructions said back. A text the user deleted or archived is never proposed
  again (`memory.Dismissed`, `TestDeletedNotProposedAgain`).
- A failed request stops the run, records the error for the row, and
  leaves the conversation to be read again next time
  (`TestFailureAndCap`).
- Routes: `GET /api/memory/distill`, `POST /api/memory/distill`
  `{on, model, effort?, days?}` (switching on, a new model, or a longer
  range reads at once; a bad level or range is a 400,
  `TestDistillRangeAndEffortRoutes`) and
  `POST /api/memory/distill/run`. The row polls every 2s while a run
  goes and reloads the memories when it ends.

### Dreaming

The nightly tidy (`internal/memdistill/dream.go`, `internal/memory/tidy.go`):
a model is shown each scope's memories and names the ones that say the
same thing, merged into one, and the ones no longer true, set aside. It is
off until switched on under Memory → Tidy up every night.

- Due once a day from 03:00 local (`DreamHour`), never before the first
  03:00 after it was switched on (`Dreams`, `TestDreamsOnceANight`). `Keep`
  runs it after the hourly distill run; Tidy now runs it at once. It
  shares distillation's model, run lock and `distill.json`.
- What is sent: per scope with two or more active or candidate memories
  and at least one not fixed, up to 120 (`perAsk`) a request, each as
  `nN | kind | date | used N× | [fixed |] text — why`. Ids are replaced by
  the short names, and a name not shown is ignored. The prompt names
  neither magpie nor the vendor.
- What the user wrote or pinned (`memory.Fixed`) is never rewritten or
  set aside. Merged with others, it is the one kept and the others go into
  it; two fixed ones are never merged (`TestTidyNeverChangesTheUsers`).
  A merge across scopes or of a memory no longer in use isn't done.
- A merge makes a new inferred memory carrying every member's tags and
  sources, active when any member was. The members are kept, superseded
  by it. A stale memory is archived with the model's reason.
- An inferred candidate left unreviewed for 30 days (`Unreviewed`) is
  archived as well, with no request needed.
- A failed request changes nothing and is kept to show
  (`TestDreamFailureChangesNothing`).
- What the last tidy did is kept in `distill.json` (`changes`). Undo puts
  it back once (`Untidy`), leaving what the user changed since: a memory
  they brought back or archived, or a merged one they edited
  (`TestUntidyKeepsWhatTheUserChanged`).
- Routes: `POST /api/memory/dream` `{on}`, `POST /api/memory/dream/run`
  and `POST /api/memory/dream/undo`; `GET /api/memory/distill` carries
  `dreaming`.

## Not built yet

Semantic search is planned; today search is by words (`Search`, CJK
split per character, `TestSearch`).

## Verification

```sh
mote-run go test ./internal/memory ./internal/memdistill ./internal/library
mote-run go test -run 'Distill|Dream' ./internal/gui
mote-run go test -tags nogui -run 'MemoryMCP|OwnMCP' ./internal/gateway
mote-run go test ./internal/browsermcp ./internal/cli
mote-run node --test internal/gui/tests/library-memory.test.cjs internal/gui/tests/library-memory-select.test.cjs
```
