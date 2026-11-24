# Fast tools

magpie's own Antigravity plugin. It does in one call what an agy session
otherwise spends several turns on, and tells a new conversation where it is.
It is for Antigravity only (the CLI and the app read the same
`~/.gemini/config/plugins`). It holds no third-party code and works on any
model, the gateway's or not.

## Responsibilities and boundaries

- A stdio MCP server, `magpie fast-tools mcp` (server `fast`), with four tools
  ([`internal/fasttools`](../../internal/fasttools)):
  - `batch_search`: up to 20 text, regex or glob queries and read-only git
    commands in one call ([`search.go`](../../internal/fasttools/search.go)).
    Regexes are multi-line (`^`/`$` per line) and CRLF files match like LF
    ones. Git takes an allow-list of read-only subcommands, and `gitNever`
    refuses the options that would have one write, run or page.
  - `read_slices`: up to 20 files' line ranges. A file over 250 lines comes
    as an outline first ([`slices.go`](../../internal/fasttools/slices.go),
    `outliners`: line patterns per extension, not a parse).
  - `apply_edits`: up to 100 edits across files, made in memory and written
    only when every one applies (`bestEffort` writes what applies). Each file
    goes through temp-and-rename, keeping its mode, its CRLF line ends and a
    symlink's target. `dryRun` writes nothing; `verify` runs one command
    afterwards ([`edits.go`](../../internal/fasttools/edits.go)). A miss says
    where the text nearly is (`nearest`).
  - `run_checks`: up to 8 commands side by side, in bash when there is one.
    Only failures show their output (the last 40 lines / 2400 bytes). A
    timeout ends the command's whole process group (`ownGroup`)
    ([`checks.go`](../../internal/fasttools/checks.go)).
  - Every result is capped at 8000 bytes (`outCap`). Antigravity puts a longer
    MCP result in a file and the model needs another turn to read it.
- A SessionStart hook, `magpie fast-tools session-start`
  ([`session.go`](../../internal/fasttools/session.go)): the workspace path,
  git branch and changes, the project kind, the top-level entries and a line
  on batching, capped at 1500 bytes. It answers
  `{"injectSteps":[{"systemMessage":{"systemMessage":…}}]}`. With no
  workspace it can read, it answers `{}`, so a failing hook never stops a
  conversation.
- Rules, `rules/AGENTS.md` (`rules` in
  [`plugin.go`](../../internal/fasttools/plugin.go)): when to reach for each
  tool. They go upstream with every turn, so they name neither magpie nor a
  vendor's limits.
- Not covered: other agents, the Library (it does not read agy's plugin
  folders, so `fast` never shows as a found server), and approvals: agy asks
  before an MCP call as it does for any server, except in its always-proceed
  mode.

## Source of truth

The plugin folder, `~/.gemini/config/plugins/fast-tools` (`fasttools.Dir`):

| File | What |
| --- | --- |
| `.magpie.json` | `{"fastTools": N}`, magpie's mark and the `rulesVersion` that wrote it. Written first. |
| `mcp_config.json` | server `fast`: magpie's program, `fast-tools mcp`, every tool `eager` |
| `hooks.json` | `fast-tools.SessionStart`, the hook command, timeout 5s |
| `rules/AGENTS.md`, `assets/logo.svg` | the rules, the icon |
| `plugin.json` | name, displayName, version `1.0.N`, description, logo. Written last. |

The mark lives in its own file because `agy plugin validate --strict` fails on
a plugin.json field it doesn't know. The plugin is on (`fasttools.On`) when
the mark is above 0 and plugin.json is there.

## Runtime paths

- **On / off:** the Antigravity CLI's `fasttools` field
  ([`agyFastTools`](../../internal/agent/agy.go)), on the Agents page or
  `magpie agy fasttools on|off`. It is `Own` and `Quiet`: the user's setting,
  not wiring, so Disconnect leaves it. A distro's agy (WSL) has no field,
  since there is no magpie there to start. `on` calls `fasttools.Write` with
  `update.Program()`; `off` calls `fasttools.Remove`.
- **Kept current:** at gateway start, `keepFastTools`
  ([`gateway/fast_tools.go`](../../internal/gateway/fast_tools.go), started
  from `gateway.go`) calls `fasttools.Keep`. That rewrites a plugin that is on
  when the magpie it starts is gone (the app moved or was reinstalled at
  another path) or when an older `rulesVersion` wrote it. A plugin that
  starts another magpie still there is left alone, so two magpies don't take
  it back from each other on every start. Keep never turns the plugin on.
- **Started by agy:** the MCP server and the hook run with the plugin folder
  as their working directory. So a tool call needs `cwd`, the workspace's
  absolute path: `root` refuses a relative one when the process sits in the
  plugin folder. The hook reads the workspace from its input
  (`workspacePaths`, then `cwd`; paths or `file://` URIs). The hook command
  goes through `sh -c` on unix and `cmd /c` on Windows, quoted for each
  (`hookCommandOn`).

## Constraints and failure behaviour

- magpie touches only a folder with its mark. `Write` and `Remove` return
  `ErrNotOurs` when the user's own `fast-tools` plugin is there. A
  half-written folder (the mark, no plugin.json) is still magpie's, isn't a
  plugin to agy, and is written over or removed.
- Commands run through `proc`, git with no system or global config
  (`gitEnv`). Package-level regexps are `lazyre`, because this package's init
  runs on every magpie launch.
- When `rules` or what `Write` writes changes, raise `rulesVersion` so `Keep`
  brings every user's plugin up to date.

## Verification

- `go test ./internal/fasttools ./internal/agent -run 'Fast|Agy|Plugin|Session|Batch|Slices|Edits|Checks|Serve|Root|Hook'`
  (through `mote-run`). Each guard was broken by hand and goes red:
  all-or-nothing edits, the git allow-list, Keep's checks, `ErrNotOurs`, the
  plugin-folder `root` refusal, and `ownGroup`
  (`TestRunChecksTimeoutEndsTheGroup` checks that the child is gone).
- GUI: `internal/gui/tests/agy-fasttools.test.cjs`.
- A real check: `magpie agy fasttools on` in a sandbox HOME, then
  `agy plugin validate ~/.gemini/config/plugins/fast-tools --strict --json`
  (no findings) and `agy plugin list --json` (rules, MCP `fast`, hook
  `fast-tools`).
