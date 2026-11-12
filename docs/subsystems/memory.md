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

Times (`created`, `updated`) are RFC3339 in the zone the memory was saved
in, with its offset. `memory_search` and the dream's notes show them in the
reader's local zone (`memoryStamp`, `TestMemorySearchDatesInLocalTime`); a
memory saved earlier, stored as UTC, reads the same way.

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

### Other memory services

EverMe and Nowledge Mem are not built in: both publish an MCP server, so the
Library's market offers them as servers (`evermeServer`, `nowledgeServer` in
[market.go](../../internal/library/market.go)), added in one step beside
Magpie Memory and given to the agents like any server
(`TestThirdPartyMemoryServers`). Their memories live in their own service;
magpie's digest, Memory tab, distillation and search don't read or write them.
EverMe runs `npx -y @everme/memory-mcp` with the agent id and `evt_` token
its console or `evercli auth login` gives (`EVERME_API_BASE` defaults to the
managed backend, editable for a self-hosted one). Nowledge Mem is its
Streamable HTTP endpoint on `127.0.0.1:14242/mcp`; a Mem reached through
Access Anywhere takes its address and an `Authorization: Bearer` key, edited
after adding.

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

### Search by meaning

[internal/memembed](../../internal/memembed/memembed.go) ranks a search by
what it means as well as by its words, so "什么时候上线" finds "Releases go
out on Tuesdays". It is off until switched on under Memory → Search by
meaning, since it downloads a model (~135 MB).

- The model is paraphrase-multilingual-MiniLM-L12-v2, Xenova's int8 ONNX
  at a pinned revision, fetched from Hugging Face with each file's size
  and sha256 checked (`pii.Fetch`) into
  `<cache>/paraphrase-multilingual-MiniLM-L12-v2/<rev>`. It
  runs in Go on the PII guard's BERT code (`guard.LoadEmbedder`); nothing
  leaves the computer. Its vectors match ONNX Runtime's to cosine 0.995
  and are no further from fp32 than ONNX Runtime's own
  (`TestEmbedderMatchesONNXRuntime`). A search and a memory are read the
  same way, with no prefix. multilingual-e5-small was tried first: its
  cosines sat in 0.78 to 0.89 for everything, and a Chinese search ranked
  Chinese memories over the English one it meant.
- Switched off, the model folder is deleted (`Set`). The switch is
  `memory-semantic.json` in the config folder; one that can't be read is
  off (`TestNotThereIsWords`).
- What is read: each active or candidate memory's text and why, as an
  one text, keyed by its sha256 in `vectors.bin` beside the model, once,
  and again when its text changes; vectors of texts gone are dropped
  (`TestIndexReadsOnce`). `Keep` reads them in the backend each minute and
  on a nudge (`Poke`); a search reads up to 16 missing ones itself, and
  with none read yet it is by words while Keep reads. The model is let go
  after 10 minutes unused.
- Ranking (`memory.Semantic`, `fuse` in
  [search.go](../../internal/memory/search.go)): the memories whose
  cosine with the query passes `Near` (0.30) and is within `Within` (0.15)
  of the best are fused with the word hits by reciprocal rank (`rrfK` 20),
  pinned ×1.2 and non-global ×1.1. Calibrated on 97 real memories and 16
  queries, half in Chinese for English memories: the one sought scored a
  median 0.47, the rest a median 0.16; 12 of 16 found with ~4 others each
  (`TestSearchFindsByMeaning`).
- Every search goes through it: the MCP server's `memory_search` and the
  Memory tab, which asks `POST /api/memory/rank` `{q, status}` (ids, best
  first, not noted as used; null while off) and lists what it ranked
  first, then what holds the words. `GET`/`POST /api/memory/semantic`
  `{on}` give the row its state: the download's progress, the memories
  read, and why either failed.

### Backups and sync

Memory goes to another computer in a backup file (the scheduled backups
too) and in sync (WebDAV, S3, GitHub), merged memory by memory, never one
computer's memory.json over the other's.
[sync.go](../../internal/memory/sync.go) owns it.

- What goes (`memory.Bundle`, `Collect`): the memories in every state, the
  projects, the dismissed distilled texts, the suggestions turned down and
  the deletions. The digest published into the agents' instructions and the
  vectors (memembed) are this computer's own and are made again after a
  change. A memory.json that can't be read stops the backup or sync instead
  of going as no memory (`TestCollectUnreadableIsAnError`,
  `TestCollectStopsOnUnreadableMemory`).
- Merging (`Combine`, the same either way round): of two copies of one
  memory, the one changed later stays, unless that change wasn't the user's
  (`Edited` is when the user last changed it) and the other copy is pinned
  or the user's while it isn't: an agent's forget or a tidy on one computer
  never undoes a pin made on another (`TestSyncMachineChangeNeverOverUsersOwn`).
  `Used` and `Uses` are the most of both, and leave `Hash` alone, so a
  search is no change to push (`TestSyncKeepsMostUses`).
- Deleting: `Remove`, and Untidy's delete of a merged memory, note the id
  in `removed` for 180 days (`keepRemoved`), so a computer that still has
  it doesn't bring it back; a copy changed after the deletion stays
  (`TestSyncDeletionStaysDeleted`, `TestSyncEditAfterDeletionKeepsIt`).
- Switches: sync's **Memory** tick (`davsync.Config.Memory`, on unless
  turned off, since a sync file is always sealed with a passphrase), and
  Export's **With memory** and Import's **Bring in memory too**, both on.
  A computer with the tick off sends none and takes none, and leaves the
  server's copy as it was (`TestSyncMemory`, `TestBackupMergesMemory`).
- After a sync or restore changes memory, `library.PublishMemory` writes the
  digests again.

### Promotion

Memory suggests making more of what it holds
([promote.go](../../internal/memory/promote.go)). The suggestions sit
over the In use list, unsearched and not narrowed to a scope
(`memSuggestions` in the library plugin's client.js), and come with
`GET /api/memory` (`suggestions`).

- To hold everywhere (`ToGlobal`): active project memories that say the
  same in two or more projects. The same means the same text (`norm`), or
  at least 60% of their words in common with six words or more on each
  side (`sameWords`); two short texts sharing words are as often two
  facts. A memory whose sources' folders (`Source.Dir`, by the projects'
  roots) are in two projects counts for both. Taken (`Promote`,
  `POST /api/memory/promote`), the one kept first (the user's own or
  pinned, the most used, the latest) becomes global and the others are
  superseded by it, their tags, sources and uses carried over; a global
  memory with the same text already is the one kept. It is the user's
  change, so a sync keeps it (`TestPromoteKeepsTheUsersOwn`,
  `TestPromoteIntoTheGlobalOne`, `TestSuggestGlobalAcrossProjects`,
  `TestSuggestGlobalBySourceFolder`).
- To be a skill (`ToSkill`): an active procedure that searches returned
  five times or more (`skillUses`). Taken (`library.MemorySkill`,
  `POST /api/memory/skill`), it becomes a skill of the library's own: a
  SKILL.md with the first line as its description, named from its first
  words (from its id when it has fewer than two Latin words), given to the
  agents ticked for instructions, or to every agent with skills when none
  is. The memory is archived then, noted with the skill's name
  (`MadeSkill`), so it isn't in the digest beside the skill
  (`TestMemorySkill`, `TestSuggestSkillAndDecline`).
- Turned down (`Decline`, `POST /api/memory/decline`): the key goes in
  `declined` (the last 500), which goes with sync, so it isn't suggested
  on another computer either. A global suggestion's key is its memories'
  ids, so another project joining it is a new suggestion.

## Verification

```sh
mote-run go test ./internal/memory ./internal/backup ./internal/davsync ./internal/memdistill ./internal/memembed ./internal/library
mote-run sh -c 'MAGPIE_EMBED_DIR=$HOME/models/pml go test -run Embedder ./internal/pii/guard'
mote-run go test -run 'Distill|Dream' ./internal/gui
mote-run go test -tags nogui -run 'MemoryMCP|OwnMCP' ./internal/gateway
mote-run go test ./internal/browsermcp ./internal/cli
mote-run node --test internal/gui/tests/library-memory.test.cjs internal/gui/tests/library-memory-select.test.cjs internal/gui/tests/library-memory-suggest.test.cjs
```
