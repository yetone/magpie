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
`MemoryMCPPath` (`/mcp/magpie/memory`), served and guarded like the search
server (`serveMCP`): another computer reaches it only while magpie is
shared, with a gateway key, and has no folder here for a project
(`TestMemoryMCPGuarded`).

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
| `POST /api/memory/digest` | `{on}` |

A reply after a change carries `problem` when publishing the digest
failed. The tab is in [plugins/library/client.js](../../internal/gui/assets/plugins/library/client.js)
(`renderMemory`), tested by `internal/gui/tests/library-memory.test.cjs`.

### Distillation

`internal/memdistill` reads finished conversations into candidate
memories. It is off until switched on under Memory → Learn from
conversations; switched on, it looks back seven days (`Back`) and no
further (`State.Since`).

- State is `distill.json` beside `memory.json` (`memdistill.Path`):
  the switch, the model, the conversations done (kept 30 days), and the
  last run's time, counts and error. It is written through temp-and-rename
  under a file lock; an unreadable file runs nothing
  (`TestUnreadableStateRunsNothing`).
- `Keep` runs 5 minutes after the GUI backend starts, then hourly
  (`keepDistilling` in `internal/gui/memory_distill.go`). `Run` takes at
  most 10 conversations (`PerRun`) quiet for 30 minutes (`Quiet`) with at
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
  `gateway.AutoVision()`.
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
  `{on, model}` (switching on, or a new model, reads at once) and
  `POST /api/memory/distill/run`. The row polls every 2s while a run
  goes and reloads the memories when it ends.

## Not built yet

A nightly pass that archives stale memories and merges duplicates, and
semantic search, are planned; today search is by words (`Search`, CJK
split per character, `TestSearch`).

## Verification

```sh
mote-run go test ./internal/memory ./internal/memdistill ./internal/library
mote-run go test -run Distill ./internal/gui
mote-run go test -tags nogui -run 'MemoryMCP|OwnMCP' ./internal/gateway
mote-run go test ./internal/browsermcp ./internal/cli
mote-run node --test internal/gui/tests/library-memory.test.cjs
```
