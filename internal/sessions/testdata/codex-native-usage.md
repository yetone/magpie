# Native Codex usage fixtures

These excerpts come from real Codex JSONL rollouts. Event order, usage vectors and relative timestamps are retained. IDs are replaced consistently; timestamps are shifted by one fixed offset per file. Prompts, instructions, paths, account metadata and tool results are removed. Tool-result events retain their type and a placeholder so their ordering is still exercised.

- `codex-native-paginated.jsonl`: three RECORD/token_count pairs from a page with `history_base`. The parent is deliberately absent. Tool results can arrive between RECORD and token_count. Expected: 3 calls, input 128,290, output 293, cache read 254,720. The previous implementation in #678 returns 6 calls through ReadCallSource.
- `codex-native-compaction.jsonl`: one legacy call, one compaction appearing as both a RECORD and embedded compaction metadata, one paired response, and one RECORD-only response. `task_started` changes the current turn. Expected: 4 calls, input 22,857, output 8,001, cache read 520,320. Main at d5591b8f returns 2 calls and omits the compaction and RECORD-only usage.

The source hashes and original line references are retained locally, outside this repository. Both public fixtures contain usage metadata only. The tests exercise Calls, CallSources/ReadCallSource, summary totals, cache reloads and an append split between a RECORD and its token_count.
