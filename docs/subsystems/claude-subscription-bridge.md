# Claude subscription bridge: turns across Claude Code runs

A Claude subscription's requests are answered by the local `claude` binary
(`internal/gateway/claude_subscription.go`, `serveClaudeSubscription`). This
page covers how a conversation's turns find the Claude Code process, or the
saved session, that had its last turn, and what a run leaves on disk. Tool
calls within a turn (`match`, `continueWith`) are only touched on where they
meet it.

## Which Claude Code runs

`claudeBinary` (and the sign-in's `claudeExecutable`) take
`claudecode.Find`: the machine's own `claude` (`proc.FindTool`), else the
one `magpie claude-code install` downloaded into
`<cache>/claude-code/<version>/` (`internal/claudecode`), else, on Windows,
one in WSL. The download is Anthropic's native build from
`downloads.claude.ai/claude-code-releases`, the "latest" channel, as
`claude.ai/install.sh` takes it: the release's `manifest.json` gives the
platform's SHA-256 and size, and a build that doesn't match both is not
kept. It is only made when the user asks (`magpie claude-code install`, or
yes to `magpie accounts add claude`'s offer on a machine without one), for
a server or a container (Jorben on Discord). It is not on PATH and stands
in for no agent. It doesn't update itself (every run magpie starts sets
`DISABLE_AUTOUPDATER`), so the serving magpie keeps it current
(`claudecode.KeepCurrent`, a `WhileServing` loop): a minute after it
starts and every six hours it asks for the latest release and downloads a
newer one, checked the same way. The version it replaced is removed on the
next look, not at once, as a run being started may hold its path;
`magpie claude-code install` removes it at once. It does nothing when
magpie downloaded none, when the machine has its own `claude`, or when
`DISABLE_AUTOUPDATER` is set for magpie (alextjy on Discord: a container's
download stayed at its first version across image updates). A failed look
keeps the download as it is and is logged.

A run is started with `--dangerously-skip-permissions` (`claudeCLIArgs`):
its only tools are magpie's MCP ones, answered by the client, and
Anthropic's web search. Claude Code run as root refuses that flag ("cannot
be used with root/sudo privileges") unless `IS_SANDBOX` is `1` or
`CLAUDE_CODE_BUBBLEWRAP` is set, so a run as root, magpie's own euid 0 or
a WSL distro whose default user is root (the probe's `uid`,
`wslrun.Tool.Root`), is given `IS_SANDBOX=1` (`claudeCLI.asRoot`;
FrierenF on Discord). A run as anyone else has its env as before. Only the
bridge's run gets it; the warm-up and `/usage` runs don't skip
permissions.

A run asks Claude Code for the model by `claudeRunName`. A Claude model
of the 5 generation or later that Claude Code doesn't know (Fable 5.2 on
2.1.316) is asked for by its `[1m]` name. By its own name Claude Code
assumed a 200K window for it and turned a longer conversation away before
Anthropic was asked ("Prompt is too long", `blocking_limit`), while magpie
tells agents the model's 1M window: Codex, compacting at 272K, never
compacted and failed every turn past 200K (magicalaciw18 on X).
`CLAUDE_CODE_MAX_CONTEXT_TOKENS` is not read for a Claude name. The models
Claude Code knows at 1M (`claudeKnownLong`) keep their names, since a new
name misses the prompt cache once. Every 4 generation model keeps its name
too: `claude-sonnet-4-6[1m]` is refused even a short turn on an account
without usage credits ("Usage credits required for 1M context"). The
reply, the saved session and the logs keep the model as asked; only
Claude Code's `--model` has the mark.

Every Claude Code magpie starts for the subscription (a turn's run, the
warm-up and model test `askClaude`, `/usage`, and the sign-in's
`auth login` and `auth status`) is given its proxy through one helper,
`claudeCLI.proxied` in each package, which is
`netproxy.EnvForBunWith`: the account's own proxy, else the
subscription's, else the global one (`netproxy.EnvWith`), with a SOCKS5
proxy replaced by magpie's loopback HTTP bridge to it (`netproxy.Bridge`,
as for every bun magpie starts, #1409). Claude Code is built with Bun,
which takes only http:// and https:// proxies: given `socks5://`, every
request failed as `UnsupportedProxyProtocol`, after about three minutes of
Claude Code's own retries, as a 502 (#2651). A Claude Code in a WSL
distro that doesn't share Windows' loopback (`wslrun.Tool.Host` set)
can't reach the bridge, so there, and where the bridge can't start, a
SOCKS proxy is refused before Claude Code starts, with an error that says
to use an http:// proxy. The `settings.json` env that magpie writes for the
user's own Claude Code sessions leaves a SOCKS proxy out (see
[Providers and accounts](providers-accounts.md)).

A saved account added by a setup-token (see [Providers and accounts](providers-accounts.md)) has no sign-in in its config directory. `inClaudeDir`, which builds the env of every run in a saved account's directory (a turn's run, the warm-up, `askClaude`, `/usage`), drops any `CLAUDE_CODE_OAUTH_TOKEN` it was given and sets it to `provider.ClaudeDirToken(configDir)` when that directory is a token account's: the token reaches the real Claude Code there and nowhere else, never on its command line, never in a log, and never to Claude Code's own account. magpie asks Anthropic nothing with it; the entrypoint and everything else about the run are as for any account.

The helper offers each of the client's tools under `bridgeToolName`
(`bridgeTools`), which Claude Code calls `mcp__magpie__<name>`. Claude Code
keeps only `[A-Za-z0-9_-]` of an MCP tool's name, so a name it would change
(`spawn.agent`, `fs/read`), one longer than 51 (64 with the prefix) or one
ending as a changed name ends (`_` and 12 hex digits) is cut, made of those
characters and given a hash of the whole name: unique (`a.b` and `a_b`
stay apart), the same in every run, so a resumed session, the history
(`renderParts`) and the tool-choice line (`toolChoiceAsk`) name it as the
run was offered it. `subscriptionRun.names` (`bridgeNames`) maps it back
to the declared name in `toolStart`, for streamed and whole messages, and
in `mcpCall` (#2649).
Each tool's input schema is folded to an object root (`objectSchema`,
`provider.ObjectRoot`), as a Messages request magpie builds is: Claude
Code leaves out an MCP tool whose `inputSchema` has a `oneOf`, `anyOf` or
`allOf` at its root, which Anthropic's API refuses, so Codex desktop's
`mcp__cua_repl__js` (a `oneOf` of objects) never reached the model. The
variants' fields are merged, and a field every variant requires stays
required; a schema that already has an object root goes as it came.

A client's MCP tool that it announced but hasn't loaded (Claude Code's
tool search names deferred tools in a reminder and leaves them out of
`tools` until `ToolSearch` loads them) is no tool of the run's, but the
run's model reads its name in the conversation and calls it,
`mcp__magpie__<server>__<tool>`. `toolStart` hands that call over under
the full name, which is the client's own, when the cut name maps to
nothing and isn't in `subscriptionRun.tools`: handed over cut, as
`<server>__<tool>`, the client answered "No such tool available" and the
model had to load it and call again (packinggoods, 11-02,
`TestClaudeBridgeHandsOverAnUnloadedMagpieTool`). Only a cut name of the
router's form, `<server>__<tool>` (`magpieServerTool`), keeps the prefix.
A deferred built-in (`WebFetch`, `WebSearch`, `CronCreate`: no `__` in
the name, no prefix on the client) called as `mcp__magpie__WebFetch` is
handed over as `WebFetch`; handed over whole, the client answered "No
such tool available: mcp__magpie__WebFetch" (steven_4422, 11-08). One of
another client MCP server called as `mcp__magpie__mcp__linear__…` is
handed over as `mcp__linear__…`. A deferred tool the run's model calls
by the bare client name (`WebFetch`, `mcp__linear__…`) is the run's
Claude Code's to answer and is not handed over.

A turn with `tool_choice` none is offered every tool the client sent, as
any other turn is (bbfatx on X). Anthropic caches a prompt as tools, then
system, then messages, so a turn offered fewer tools (a title or summary
asked with the tools as ever) was a prefix the cache had never seen, and
all of it, Claude Code's own tools and system prompt included, was written
again. What a turn may call is kept apart from what it is offered:
`subscriptionRun.allowed`, set at start, on resume and with tool results.
A call the turn makes anyway is answered at once with an error result
(`mcpCall`), `toolStart` treats it as Claude Code's own call, and the
client never sees it. A named `tool_choice` still offers only that tool.
`TestClaudeToolChoiceNoneKeepsTheTools` holds it.

The run's MCP server is magpie itself (`magpie claude-mcp-helper`,
`internal/claudebridge`), which hands each tool call back to the gateway
at `POST /_magpie/claude-mcp/<token>` (`mcpCall`), where the call is
registered in `b.calls` for the turn with its results to find. The token
is `randomToken` (24 bytes from `crypto/rand`), one per run. The callback
URL is written to `callback` in the run's own temp folder (0600) and the
helper is given that file's path, so the token is never on Claude Code's
command line. The helper calls the address the gateway listens on
(`callbackBaseURL`: `MAGPIE_ADDR`, `0.0.0.0`/`::` as `127.0.0.1`), which,
pinned to one interface's address, isn't loopback, and it sends no
gateway key. `lanGuard` therefore lets in exactly that path, by POST, with
the token of a run now live (`ownCallback`: `addCallback` when the run is
registered, `dropCallback` in `removeRun`, compared in constant time).
Every other request, and this path with a wrong, spent or encoded token,
is judged as any other. Without it the call was refused 401, never
reached the run, and every turn with tool results found no run waiting and
started a new Claude Code told the whole conversation (#1546).

## Claude Code's interactive mode (`claudePrintMode`)

By default a run on macOS, Linux and Windows is Claude Code's
interactive mode, not `claude -p`. Anthropic counts a request by its
entrypoint, and a `-p` run's `sdk-cli` costs about twice the plan's allowance of the interactive
mode's `cli` (anthropics/claude-code#97074). The run is the real
interactive Claude Code: nothing sets `CLAUDE_CODE_ENTRYPOINT`.

The bridge starts `magpie claude-tui-helper <claude> <the -p arguments>`
(`internal/claudetui`) in place of `claude` when `claudeTUI` says so, and
talks to it as it talks to `-p`: stream-json in and out. The helper:

- turns the arguments into the interactive mode's (`interactiveArgs`):
  `-p`, the stream-json formats, `--verbose`,
  `--include-partial-messages`, `--thinking-display`,
  `--dangerously-skip-permissions` and `--no-session-persistence` go;
  `--permission-mode default --allowedTools mcp__magpie[,WebSearch]
  --no-chrome` come. Permissions are not skipped: only magpie's MCP tools
  (and the web search `-p` was given) are allowed, and nothing else is
  offered (`--tools ""`). A new session is given `--session-id`;
- starts Claude Code in a pseudo-terminal of its own (200x50), no tmux,
  and answers what it asks of the terminal. On macOS and Linux it is a
  pty in a session of its own (`child_unix.go`). On Windows it is a
  pseudo console (ConPTY, `child_windows.go`): `CreatePseudoConsole` on
  two pipes, and `CreateProcess` with the console as an attribute and
  `STARTF_USESTDHANDLES` with no handles, since without it a child
  took the helper's own standard handles (the bridge's pipes) and drew
  into them rather than the console (tried on Windows 11 26200). An npm
  install's `claude.cmd` is run by cmd.exe with its arguments quoted as
  the `-p` run's (`proc.Command`). Ending, the helper closes the console
  (Claude Code is told as a closed window tells it) and kills it after
  3s. Its `-p` fallback runs Claude Code as a child, since Windows has no
  exec. A Claude Code in WSL is run the same way, through `wsl.exe` in
  the console: the gateway hands the helper `MAGPIE_CLAUDE_TUI_WSL`
  (`claudetui.WSL`, `claudeCLI.tuiWSL`), wsl.exe's arguments up to
  Claude Code's (`wslrun.Tool.Args`, `MAGPIE_WSL_RUN` among them), the
  distro's mount of the drives, and the projects folder as Windows reads
  it: the account's config directory (handed in as
  `CLAUDE_CONFIG_DIR/p`), or the distro user's own `~/.claude/projects`
  through `\\wsl$\<distro>` (`wslrun.Tool.Home`, from the probe). `TERM` is
  added to `WSLENV`, and an image's path is pasted as the distro reads
  it (`/mnt/c/…`, `shim.path`). The helper itself is the Windows
  magpie.exe, not the `/mnt/c/…` path the MCP helper is started at.
  In the distro user's own folder, when Windows can't stat
  `\\wsl$\<distro>\<home>` (`tuiWSLUnread`: a session with no 9P
  redirector, as one over ssh, reads nothing there) or the probe found
  no home, the transcript is read inside the distro (`WSL.Distro`, no
  `Projects`; `wslread.go`): before Claude Code starts, a shell run
  through `wsl.exe` notes how much of `$HOME/.claude/projects/*/<sid>.jsonl`
  is there (a resumed session's earlier turns), says `ready`, then
  `tail -F`s the rest, and the helper copies it to a file of its own
  that it tails as it tails any transcript. Once Claude Code ends the
  helper waits for the copy to stop growing (`settle`), then closes the
  shell's stdin, which ends it. Until 11-01 both cases were refused
  (`wslread`, `wsl-home`).
  Killing the gateway ended the distro's Claude Code with wsl.exe (tried
  on Windows 11 26200, magpie-desktop, Claude Code 2.1.289);
- pastes each user message into the prompt (bracketed paste) and presses
  Enter. A message opening with `/`, `!`, `#` or `&` is pasted after a
  line break, so it is words, not a command (`pasteable`);
- pastes a message's images as files (`userPieces`, `writeImage`): each
  base64 image is written to `image-N.<ext>` (0600) in a
  `magpie-claude-images-*` temp folder, and its path is pasted alone,
  which Claude Code reads into the message as `[Image #N]` (a path inside
  other words stays words). Each piece is pasted once the prompt shows
  the one before (`send`). Claude Code reads an image's file after the
  paste returns, so the helper waits for `[Image #N]` with N past the
  paste before; without that wait the second of two images was lost.
  Words with a line that ends naming an image file (`a.jpg` in an `ls`)
  are tried as that file first (2.1.318 asks the clipboard for it, about
  0.45s) and taken in after, and Claude Code keeps one mark of a paste
  being taken in for every paste: an image pasted meanwhile cleared it,
  and Enter sent the message without the words, which came in the next
  prompt (#5548). So words are waited for too, until their last words
  show or a `[Pasted text #N` with N past the paste before (`shown`):
  images and pasted text are numbered together, and the prompt drawn
  again shows the earlier ones (`TestShimWaitsForWordsNamingImages`,
  `TestShimPastesImages`). Both are looked for in what was drawn since
  the paste and at the end of the prompt as the screen shows it, the rows
  between its last two rules (`prompt`, `imageTaken`, `textTaken`): a
  prompt grown past a row is drawn again only where it changed, so the
  drawing after a paste can hold one digit of its mark. With nineteen
  images between words, 2.1.319 drew "3" of `[Image #33]`, the helper
  waited out `pasteWait` and ended with exit status 1, a 502 to the
  client (#5654, `TestShimSeesPastesOnAPromptDrawnInPart`, a real run's
  bytes). The transcript holds real image blocks, and
  Claude Code notes each one's path to the model (`[Image: source: …]`).
  The folder is removed once its message is sent (Claude Code has read
  the files by then), and when the helper ends, on SIGINT, SIGTERM and
  SIGHUP too: magpie's own handler (`endProbesOnSignal`) let go of the
  signals for this command (`leaveSignals`), since it re-raised SIGTERM
  and killed the helper before it could end Claude Code and remove its
  files. Windows ends a run by killing its job, which no handler hears,
  so a helper starting removes such folders, and the tap's, over an hour
  old (`dropLeft`);
- tails `<CLAUDE_CONFIG_DIR>/projects/*/<session>.jsonl` and tells each
  row as `-p` would (`translator`): assistant messages whole, a tool's
  result, a `<synthetic>` API error as the turn's error result with its
  `rate_limit_info`, and `turn_duration` as the result;
- streams each reply as it comes, through the tap (`tap.go`, `tap.js`).

The transcript has a message only once it ends, so the tap streams it.
Claude Code is a Bun program, and Bun loads first what `BUN_OPTIONS`
names with `--preload`. The helper writes `tap.js` and an empty
`tap.jsonl` to a `magpie-claude-tap-*` temp folder, and starts Claude
Code with `BUN_OPTIONS` set to the user's own value followed by
`--preload <tap.js>`, and `MAGPIE_CLAUDE_TAP` set to the file. Claude Code
in WSL gets both through `WSLENV`, with the paths as WSL reads them, and
not the user's Windows `BUN_OPTIONS`. `BUN_OPTIONS` has no quoting, so on
Windows a path with a space takes its short name. A path that still has
whitespace gets no tap.

`tap.js` takes itself out of `BUN_OPTIONS`, `process.execArgv` and the
environment, so nothing Claude Code runs loads it. Claude Code sends its
API requests through `Bun.FetchSession`, and the tap puts a subclass in
its place. That subclass calls the real fetch, with the request,
connection and TLS left as they were, and tees the body of each reply to
a request with `x-claude-code-request-class: main`, a `/v1/messages` path
and a `text/event-stream` reply. Claude Code reads one branch as before.
The tap parses the other and writes one JSON line per event to the file:
`{s, open}` when a stream starts, with `h`, the reply's
`anthropic-ratelimit-unified-*` headers, when it has any, `{s, e}` for
each event other than `ping`, and `{s, end: "done"|"cut"}` when it ends.
The tap only reads the reply's headers; the request is sent as Claude
Code made it. At load it writes
`{tap: "on"}`, or `{tap: "none"}` when there is no `FetchSession` to
replace.

The helper reads the file every time round the tail loop, every 10ms
while a stream is open, and tells each event as `-p` does: a
`stream_event` line. Order is kept two ways:

- Before an assistant message `X` from the transcript is told,
  `tap.before` waits up to 1s (`tapWait`) for a stream with no
  `message_start` yet to start as `X`. If none does, `X` is told whole,
  and when its stream comes late it is left out (`dropped`).
- Before a turn's result, `tap.settle` waits up to 1s for the open
  streams to end, and ends the rest there.

The headers become the `rate_limit_event` `-p` tells from them
(`rateLimitInfo`, as Claude Code 2.1.291 builds it): `status` and
`unifiedWindows` with each of `five_hour`, `seven_day` and
`seven_day_overage_included` (`5h`, `7d`, `7d_oi`) whose utilization and
reset are both said, the reset rounded to a second. A streamed message
tells it before its `message_start`, so the bridge keeps it
(`NoteClaudeLimits`) before the reply begins. A reply whose message was
told whole, or that never started one, tells it at the turn's end
(`tap.settle`), and only when it is the latest: a later reply's told
with its stream is never followed by an older one's. A `<synthetic>` API error's own
`rate_limit_info` is told after that, as the newest. Before this, a
successful turn in the interactive mode told no allowance (T-076).

A stream cut short gets a `message_stop` with no `message_delta`, as `-p`
ends such a stream, and the bridge counts it as an attempt and asks
again. In the bridge, a message that was streamed (`streamed`, by id) has
its whole rows left out, a late one too. A client's `tool_use` that was
streamed is marked `told`, so `onCall` doesn't tell it a second time.

A Claude Code that isn't Bun, or doesn't load the tap, writes nothing to
the file. Its messages are told whole, with no wait, as before. Claude
Code 2.1.281 on Windows doesn't send through `FetchSession`. 2.1.289 (WSL)
and 2.1.316 (macOS, Windows) do.

The transcript has a message only once it ends, and Claude Code doesn't
always write the message calling a client's tool before that call is
answered: on a real account, 4 runs of 8 wrote it after its MCP call
returned. The call can only be answered by the client, which hasn't been
told of it. So in an interactive run, `mcpCall` hands each call to the
reader (`run.onCall`). Not shown from the transcript within `callFirst`
(500ms; a message written before its call is read 118 to 162ms after
it), the call is a reply of its own: a message with that one `tool_use`
(Claude Code makes its MCP calls one at a time, a message's parallel
calls too). The first message written after such a call is the one that
made it. Its rows are left out (`lateID`), and its usage is billed with the
next reply. A call in that message that wasn't made yet becomes its own
reply when it is made (`written`). A reply told this way has no words or
thinking: the model's text before its call is lost to the client.

A reply that more than one message makes (a late message, Claude Code's
own ToolSearch or WebSearch, a cut stream's retry) tells its client the
last message's input, cache writes and cache reads, and every message's
output (`Usage.after`). Claude Code reads the last reply's three as how
full the conversation is, and each message re-sent what the one before
it had: summed, a reply after a ToolSearch read 1,154,857 cached tokens
for a 577k conversation and Claude Code compacted early (sleepy_shibe on
Discord). The earlier messages' prompts go in `Usage.Deferred`, billed in
the ledger and never told. The translated route's tries (the empty reply
asked again, a loop's re-ask, a cut reply's continuation) and web search
rounds tell theirs the same way (#5115). A later message's prompt counts
replace the earlier's, a zero too (`Usage.restart`), so a field the later
one reads nothing in is not left at the earlier's (`TestClaudeOwnCallReplyTellsLastPrompt`,
`TestClaudeTUILateMessagePromptNotTold`, `TestRetriedReplyTellsLastPrompt`,
`TestLaterTryZeroCacheReadNotTold`).

The user's own `~/.claude.json` and `~/.claude` are never written by
magpie (Claude Code writes them, as when it records its onboarding done,
below). Claude
Code asks whether to trust a folder it has not run in; the helper answers
yes only when the bridge sets `MAGPIE_CLAUDE_TUI_TRUST=1`, which it does
for magpie's own working folder or a saved account's config directory,
both magpie's. In the caller's folder under the account Claude Code is
signed in to, the question is the user's: the helper ends there, saying
so (`claudetui.Untrusted`), and the turn goes in magpie's own folder,
told the caller's as any run whose folder isn't here is (`envElsewhere`;
`startTrusted`). Until 11-01 such a run became `-p`; from then until
the fallback it was a 502, which a Claude Code client (paseo) asked again
with backoff, so the chat hung (lwjef on X).
`TestClaudeTUIUntrustedFolderAnswered` holds it.

Before each such run the bridge decides as Claude Code does whether it
would ask (`untrustedHere`, `claudeTrusts` in `claude_trusted.go`), from
the user's own answers in the profile `provider.ClaudeTrustedProjects`
reads (`CLAUDE_CONFIG_DIR/.claude.json` or `~/.claude.json`), so a folder
the user never trusted is never run in and a folder they trust is run in
from the next run on. The rules are Claude Code 2.1.316's, read from its
code (`claudeTrustedBy`): a `projects` key, the path in NFC (forward
slashes on Windows), with `hasTrustDialogAccepted` true for the
repository's main checkout (its root; for a worktree the checkout it was
added from, `claudeMainCheckout`), or for any folder from the run's up
to the repository's root and never past it, so a repository inside a
trusted one is asked about; outside a repository every folder above
counts. The run's folder as written and with links followed both count.
A profile that can't be read is unknown, and the run goes in the
caller's folder as before. A folder Claude Code asked about all the same
is passed over for 10 minutes (`passedOver`, `untrustedFor`). The run
that goes in magpie's folder after the question goes on from the
conversation's saved session (`startRun` keeps it for `startTrusted`).
Before, only the 10 minutes decided: past them, a conversation whose
session was in magpie's folder ran in the caller's again, was told the
whole conversation there, was asked, and was told it all once more in
magpie's folder with its branch dropped, every 10 minutes (sleepy_shibe
on Discord). `TestClaudeTUIUntrustedFolderKeepsItsSession`,
`TestClaudeTUIFolderTrustedLaterRunInAtOnce` and
`TestClaudeTrustedFolders` hold it. The question opens on "No, exit", and a key
sent as it is first drawn can be lost to its being drawn again, so the
helper presses ↓ until "Yes" is the last choice drawn and Enter only once
it has stayed so for 300ms (`ready`, `trustSettle`). Pressing Enter blind
chose "No, exit" in the real gateway, and no run started.

Every run is `-p` only when the Claude Code editor's "Claude Code runs
as" row is on claude -p (`claudePrintMode`; the one row, in the editor and, in magpie web as a gateway, there too), or in the gateway's own
tests, whose fake Claude Codes are `-p` ones (`claudeTUIRuns`, code
`no-shim`). With it On, nothing ever runs as `-p` (the owner, 11-01): the
plan counts a `-p` request as another client's, at about twice the
interactive mode's, and a `-p` run starts without the interactive mode's
cache. A run the interactive mode can't take is refused before any Claude
Code starts (`startRun`), and the request gets an error naming why and
the setting (`claudeTUIErr`, `claudeNotTUIWhy`). The refusal is a 400
(`tuiRefusal`): it is about the request or this computer, the same on
every account, so the fallback doesn't ask the next account and no
account rests for it. Until 11-01 it was a 502, which every account was
asked in turn (#4778: 15 tries across 3 accounts, some rested). Its
words avoid "Models" just before the error's type: "Models ›
Replies…\",\"type\":\"invalid_request_error" read as a model not served
(`provider.ModelUnserved`), which made the 400 retryable again. That is
when (`claudeTUI`, `claudeMachineWhy`) this computer has no Claude Code
path or isn't macOS, Linux or Windows. Nothing a request asks is refused
any more (`claudePrint` keeps an image known by its URL, which
`serveClaudeSubscription` has downloaded before any run, below).

The one exception to "nothing runs as `-p`" with it On is a turn Anthropic
refuses in the interactive mode as a third-party app's: a 400 "Third-party
apps now draw from your extra usage" (#4503), or on a Team or
Enterprise workspace "draw from extra usage" with no "your" (#5645; both
wordings are `thirdPartyIn`). Anthropic judges the agent's
own system prompt, as a whole, there and serves the same prompt to `-p`
(isjiajia01's controlled tests; Raven on Discord: Pi in Cindy through
`group/auto`, every turn refused). When the refusal comes with nothing of
the reply said or sent (`relay` hands it back as `redoAsPrint`,
`redoable`), the turn is asked again once as `-p` (`serveSubscriptionAs`,
`Request.tuiRefused`, `claudePrint` code `third-party`). Anthropic answered
the refused turn nothing, so the `-p` run is its only answer; a turn that
would draw from extra usage would have been served, not refused. The
prompt's fingerprint (`thirdPartyKey`: its first 2KB, the agent's own part
before Pi's working directory) is kept on the bridge for 12 hours
(`thirdPartyFor`), and its next turns start as `-p`, their runs going on
with tool results as a `-p` run does (`wantTUI`). A `-p` run's refusal, a
turn whose reply had begun, and Off are never asked again
(`thirdPartyRefused` says why). Settings' own value is not changed.
`claude_tui_refusal_test.go` holds each case.

The other exception is Claude Code's auto mode classifier when its own
cache marks keep a long transcript (#5585, #5655; `keptByPrint`,
`claudePrint` code `cache-marks`). The classifier is asked afresh each
call: its system, the user's CLAUDE.md, the transcript so far with its
marks moved on to the newest entry, and the action to judge. Pasted into a
new interactive run, it loses those marks: Claude Code marks only the last
block of what it is pasted, and the classifier's tail changes every call,
so each call read only Claude Code's own prompt (about 69K tokens) and
wrote the whole transcript again, 126K and 132K a call at an hour's 2x
in iamnil's rows. A `-p` run keeps the client's marks (below), so a call
reads what the call before wrote and writes only what is new. `-p` is
counted at about twice the allowance a token, so it is chosen only past
`marksPrintAt`, 64KB of message text before the last mark: with S Claude
Code's prompt, M the marked transcript and D the new part, the interactive
call costs about 0.1·S + 2·M and the `-p` one about 2·(0.1·(S+M) + 2·D),
which meet at M ≈ 15–20K tokens. A shorter classifier, and every other
request, stays interactive; a conversation's later turns go on in their
run, which keeps its own cache, so none of them is moved. Desktop's
classifier, which sends no `X-Magpie-Cache-Marks` header, is parsed for
its marks with the interactive mode on only for this
(`serveSubscriptionAs`). `TestClaudeTUIClassifierKeepsLongMarksAsPrint`
holds it, with and without the header.

A JSON schema is answered through an answer tool of magpie's
(`claude_tui_schema.go`): interactive Claude Code takes `--json-schema`
and does nothing with it (2.1.316, a real run: its result's
`structured_output` is null), since its `StructuredOutput` tool is
`-p`'s alone. `serveClaudeSubscription` compiles the schema
(`compileAnswerSchema`: draft-07 unless it says otherwise, as `-p` reads
one; a `$ref` outside itself is refused, never fetched) and adds the
tool, `StructuredOutput` or a numbered name apart from the client's
(`answerTool`), with the schema as its input schema, through the bridge's
MCP server. `mcpCall` checks each call against the schema (`answer`): one
that doesn't fit gets an `isError` result saying why, to be made again,
as `-p`'s is; one that fits is kept (`answerArgs`). At the turn's result
the kept input is the reply's `structured_output`; a turn that ended
without one is told once to call the tool (`answerAsk`), and fails "no
answer fitting the schema" when it still doesn't. Neither the call nor
the words around it reach the client (`r.schema`). A real run (haiku,
11-02) called `mcp__magpie__StructuredOutput` at once, with input that
fit a `pattern`. A schema the compiler can't take is a 400 before any
run (`TestClaudeTUISchemaAnswer`, `TestClaudeTUISchemaAskedOnce`,
`TestAnswerTool`).

A first message of any length is pasted: a conversation told whole to
a new Claude Code (one older than its session, or a client's own) can
run to megabytes. A real 3MB paste showed `[Pasted text` after 35s, and
a real 500KB one was answered with all of it in the transcript, so the
helper waits for a paste a second per 64KB longer than `pasteWait`
(`shownWait`) before the next piece or its Enter, and goes on when it
still isn't shown. Until 11-01 words over 400KB were
refused with a 502 (`big`), a bound no paste had been tried at.

An image known only by its URL is downloaded first
(`fetchURLImages`, in `serveClaudeSubscription`): the helper pastes
files, where `-p`'s Claude Code had Anthropic fetch the URL. It goes
through `publicClient` (public addresses only, as every image fetch), it
must read as an image (`http.DetectContentType`), and the last 32 are
kept by URL (`urlImageBytes`), since a conversation hands the same URLs
over each turn. One that can't be had fails the request, saying which
and why; no Claude Code runs it. With the bytes in, the request is an
ordinary one with a pasted image (`TestClaudeTUIImageByURL`).

Safeguards are taken, each turn's in every request (#4804). Interactive
Claude Code reads `CLAUDE_CODE_EXTRA_BODY` from its own environment as
it builds each request, and nothing outside it changes that once it runs
(2.1.316: a later `apply_flag_settings` or a rewritten `--settings` file
changes nothing it sends). `startRun` puts them in its environment, and
writes the same extra body to `guard.json` in the run's temp folder
(`guardBody`; every interactive run has the file, `{}` with no
safeguards), named to the helper by `MAGPIE_CLAUDE_GUARD`
(`claudetui.GuardEnv`). The helper hands it to Claude Code as WSL reads
it, and takes it out of the environment otherwise. On each
`/v1/messages` request whose body is a string with a `safeguards` key,
`tap.js` reads the file, puts its `safeguards` there in place of Claude
Code's, and sets `CLAUDE_CODE_EXTRA_BODY` in Claude Code's environment
to it (the file without its `effort`), so the requests after are built
with it. A JSON parse and stringify of a 2.1.316 body is byte for byte
the same, so the request is what Claude Code sends with that variable.
At the first such request, or the first of the main turn's
(`x-claude-code-request-class: main`), it writes `guard.json.live`. `setSafeguards` replaces the file (temp file and
rename) before the turn or tool results go to the run, and the run goes
on, its cache too: an auto-mode tool loop's classifier context grows by
a `prior_turn_context` entry each batch, and restarting for that began
a new run for every tool round, each writing the whole conversation to
the cache again (3 to 10 times the cost;
`TestClaudeTUIToolLoopCarriesEachBatchsSafeguards`,
`TestTapPutsTheGuardFilesSafeguards`). A changed safety beta, or
safeguards appearing or going, still restarts.

The level a remote client asks reaches `--effort` from every route: Messages
(`thinking` budget, `output_config.effort`, and the last turn's
`output_config` when the request's own names none), Chat (`reasoning_effort`,
else `reasoning.effort`, `output_config.effort` or a `thinking` budget, as a
relay turning a Claude client's request into Chat may put it) and Responses
(`reasoning.effort`) (`TestClaudeBridgeTakesTheEffortOfEveryRoute`).

A turn asking another effort goes on in its run too (#5584). The `-p`
run is told it by `apply_flag_settings` (`setEffort`, #502). The
interactive mode takes another level only from a person typing
`/effort`, and 2.1.318 saves that as the default for new sessions
(`modelSettings.<model>.effortLevel` in the settings of the account's
folder, the user's own `~/.claude/settings.json` for Claude Code's own
sign-in; only `max` is the session's alone), and reads a `--settings`
file once, as it starts. So `setTUIEffort` writes the level beside the
safeguards in `guard.json` (`tellGuard`, from the run's safeguards and
`effort`; none while it is the `--effort` the run started at,
`ownEffort`), and `tap.js` asks each main request at it in
`output_config.effort`, leaving Claude Code's other requests (titles,
summaries, its classifiers) as they were. That is the one field
Claude Code's own `/effort` changes in its requests: on the real
2.1.318 with Haiku 5.5, the turn after one asked high in place of
medium read the whole conversation from the cache (55,392 tokens read,
41 written; the turn before, 55,308 and 84). The level is the request's
own, as `--effort` takes it, not fitted to the model as Claude Code
fits its own: one the model refuses (claude-haiku-4-5 answers "API
Error: 400 This model does not support the effort parameter.") fails
that turn, which lets the run go (`ended`), and the turn asked again
starts a Claude Code at that `--effort`. A run without a `.live` file,
or a turn asking Claude Code's own level (`""`) of a run started at
another, is let go, the log saying why, and the turn starts a run anew
(`TestClaudeTUIEffortChangeGoesOnInItsRun`,
`TestClaudeTUIEffortChangeWithoutTapStartsAnew`,
`TestClaudeTUIEffortRefusedStartsAnew`,
`TestTapAsksTheGuardFilesEffort`).

A run with no `.live` file (no tap, a Claude Code not on
`FetchSession`, or no request yet) can't be given them. Its classifier
context also carries parts that change on nearly every request, the
ones Claude Code names as variable and sheds over its 256KB budget:
`prior_turn_context`, `system_reminders`, `artifacts`,
`text_provenance`, `shed`, Chrome's tabs, and git's status, remotes and
error. `safeguardsKept` leaves those out of the comparison (git keeps
cwd, root, branch and default branch), and such a run whose turn
differs only there goes on with the context it started with
(xybio; `TestClaudeTUISafeguardsVaryingPartsKeepTheRun`). The server
classifier then sees those parts as they were when the run started. A
rules, permission-mode, auto-mode, folder or branch change
still restarts. The `-p` run takes every change in place
(`apply_flag_settings`). Client cache marks are taken too, but not kept: the
helper pastes the words without their marks, so the run keeps Claude
Code's own prompt caching (no `DISABLE_PROMPT_CACHING`, no client tail)
and the reply reports every client mark as dropped
(`clientCache.pasted`), except a long classifier's, which runs as `-p`
for them (`keptByPrint`, above; see
[Client-placed cache marks](#client-placed-cache-marks-1717)).

The gateway's log says which (`claude: not the interactive mode: …`).
A refused request never ran, so it isn't the editor's last request
(`noteClaudeRun`). The model test's and warm-up's "hi" (`askClaude`) goes
through the helper too (`askClaudeTUI`), in stream-json, and its stdin is
closed at the first result. Reading the plan's allowance (`/usage`,
`claudeUsage`) never starts the interactive mode, on or off: it runs
`claude -p /usage` (`claudeUsageArgs`, #5582). The interactive Claude
Code 2.1.316 sends a model request as it starts, before anything is
typed (`probeQuotaStatus`: haiku, `max_tokens` 1, content "quota"),
unless `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` is set, which also
keeps `/usage` from loading ("Failed to load usage data"). That request
counts, so on an idle account it started the five-hour window: from
v0.1.2635 to the fix, magpie's reads at its start and as a window ended
opened the user's window with warm-up off (recorded behind a proxy that
answers everything itself). `-p` sends that request only when
interactive, so its `/usage` asks the usage endpoint alone
(`/api/oauth/usage`), and for a subscription's sign-in (the
`user:profile` scope) prints the plan's windows as the lines
`provider.parseClaudeUsage` reads. Only a sign-in without that scope (a
setup-token, which magpie never reads, or an API key) gets the session's
cost instead. `TestClaudeUsageStartsNoInteractiveRun` holds it.

The text omits the extra-usage cap. The same run prints `--output-format
json --verbose` (`claudeUsageArgs`), whose messages carry
`usage_report.rate_limits.extra_usage` (cap and spend in cents);
`claudeExtraUsage` turns an enabled one with a cap into one more line
(`provider.ClaudeExtraLine`), read as the "Usage credits" window: an
`Aside` row with the month's spend over the cap, which never marks the
account used up. It is not the prepaid balance (a separate endpoint magpie
does not call), and only Anthropic decides whether a request spends it
(X, Shinji_Zl). Tried against fakes only; the owner's account has extra
usage off.

A turn's run (`claudeRunEnv`) is not given
`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`, which the one-off runs above
keep (`cleanClaudeEnv`). With it, or `DISABLE_TELEMETRY` or
`DO_NOT_TRACK`, Claude Code 2.1.316 reads none of its feature flags, and
one of them (`tengu_curious_tower`) is Message Threads: a turn sends only
what came after the last reply. Without it every turn sent the whole
history again, every image in it (#5963, nylesssssssss: with a ~3 MB PNG,
672 KB on each of four turns; 737, 6, 3 and 3 KB with the flags read, on
a real Team account behind a counting proxy). Nothing forces threads:
`CLAUDE_CODE_TETHER_LIVE`, which turns them on whatever the account's
flag says, is never set, and an account Anthropic hasn't given threads
sends whole, as its own `claude` does. Updates and error reports stay off
(`DISABLE_AUTOUPDATER`, `DISABLE_ERROR_REPORTING`); Claude Code's own
telemetry goes as a plain `claude`'s does, unless the user turned it off:
a quiet variable set in magpie's environment or in their Claude Code's
`settings.json` env is given to the run as they set it, and the run then
reads no flags. An interactive run sends Claude Code's quota request as it
starts, right before the turn's own. `TestClaudeRunReadsItsFlags` holds it.

The helper still takes a typed `/usage`, and nothing of magpie's sends it
now: it is told `{"type":"command","command":"/usage"}`, types it, waits for the
panel's windows ("NN% used", `commandSettle` still) and closes it with
Esc. A panel with no window (its words saying why) is taken as drawn only
once its Settings tabs show and nothing is loading (`usageDrawn`), still
for three `commandSettle`s: Claude Code draws its banner and folder again
first, which stays still while a slow /usage loads, and taken as the
panel it was read as an allowance's error (ikunbots on X, v0.1.2738).
With no panel by `commandWait`, the result is an error saying what was
last drawn (`TestShimWaitsPastTheBanner`, `TestUsageDrawn`). Its result (`local_command: "usage"`) says each window as -p's lines
do, "Current session: 99% used · resets 7pm (Asia/Shanghai)" (`usageText`,
tested on the bytes Claude Code 2.1.316 drew, `testdata/usage-2.1.316.bin`),
for `provider.parseClaudeUsage`.
The helper reads the screen as a terminal shows it (`grid`,
[`screen.go`](../../internal/claudetui/screen.go), 200 by 50, the size
the pseudo-terminal and Windows' pseudo console are made, from one
`screenCols`/`screenRows`; an 80 by 24 grid of the 200 by 50 Unix
terminal drew the panel's rows over one another, #4988), beside the stream:
Windows' pseudo console sends only the cells that changed, skipping one
that holds the same letter ("Settin\x1b[1Cs"), and a window drawn only
for a moment is kept as it looked (`panel`). The panel is drawn before
its figures, "Loading usage data…" while Claude Code asks for them: the
helper waits that out (to `commandWait`), presses "r" once on "Error:
Failed to load usage data", and a panel that still has no figure is an
error result, "/usage couldn't load the plan's usage: …". Before, 3s
still on "Loading usage data…" was taken as drawn, and the panel's frame
was told as the figures, which the card read as an account with no
allowance (Seagull fries, Windows 11; `TestShimWaitsForUsageFigures`, on
the bytes the pseudo console passed on for a real 2.1.316,
`testdata/usage-loading-win-2.1.316.bin`, `usage-failed-win-2.1.316.bin`).
The provider keeps the account's last reading on that error
(`claudeUsageUnknown`, as for Claude Code's own "temporarily
unavailable"), and a panel with no figure otherwise is "Claude Code's
/usage showed no usage figures: …", never "told no allowance", which is
kept for an account Claude Code refuses (`claudeUsageDenied`, matched by
`claudeDenied`, which drops "(not your usage limit)" and the request
counts first). That error names the panel's own text from its tabs on,
or its "Error: …" line (`usageWhy`, `usagePart`), never the banner and
folder drawn above it. A panel drawn whole, with its Session tab and no
error, loading or stale mark, that has no "Current session" or "Current
week" is an account with no window started yet: Anthropic leaves a
window out until the request that starts it. Its result is
`NoUsageWindow`, and the card shows the five-hour window unused, with no
error (`TestShimReadsUsageWithNoWindow`, `TestClaudeUsageNoWindowStarted`;
koohoko5924 on Discord, a Claude Enterprise account, on panels built from
2.1.316's strings, not captured). The Usage endpoint's own errors
("temporarily limiting requests to the usage page", "rate limited
until …") are failures as "Failed to load usage data" is
(`TestShimToldWhyUsageFailed`).

These one-off interactive runs (`askClaudeTUI`) work in
magpie's shared work folder (`oneOffDir`: `claudeWorkDir`, a fresh temp
folder only when it can't be made), where the helper may answer the trust
question, so a config directory holds one trusted entry for it instead of
a new `magpie-claude-<n>` entry each read (#4830, mydosu): 67 had piled up
in one `~/.claude.json`. Entries left by older magpies stay; magpie
doesn't write that file. In a saved account's config directory, magpie
writes the folder's entry itself before each run there (`trustWork`,
`provider.ClaudeTrustFolder`), and so before a bridge run of that
account in magpie's folder: on mydosu's Windows (#4830) the helper's
answer was never recorded there, and every read of the account waited
out its time on the question. The entry is keyed as Claude Code 2.1.316
looks a folder up (`claudeTrustKeys`): the path in NFC, with forward
slashes on Windows (`C:/Users/<me>/AppData/Local/Temp/magpie-claude-work`),
also with its links resolved (`/private/var/…` on a Mac), and the WSL
mount path for a Claude Code in WSL. Only a direct child of
`claude-accounts` is written, never `~/.claude.json`; every other key
is kept, a file that can't be parsed is left alone, and a run's own
fallback folder isn't recorded. The run's own session file in that folder is
removed by the `session_id` the helper's init line names (`dropOneOff`),
so the folder's project doesn't fill with "hi"s. The helper is told when
its caller stops waiting (`claudetui.DeadlineEnv`, the context's deadline
less `oneOffMargin`, at most a quarter of what is left) and ends then on
its own, saying what Claude Code hadn't done and its last screen ("Claude
Code hadn't drawn /usage's panel in the 16.9s its caller waits for it:
…"). Killed at the deadline instead, it said nothing, and on Windows the
reading's error was "exit status 1" alone. A run that still says nothing
is "Claude Code's interactive mode said nothing before magpie stopped
waiting for it" (`oneOffErr`), and the card says the reason
(`quotaError`'s "Allowance unavailable: {why}"), its full text kept for the
tooltip. A question about trust left unanswered at the deadline is named
with its folder ("Claude Code asked whether to trust <folder>, not answered
in the 16.8s …"), where it said "showed no prompt" before (#4830). A failed
run also goes to app.log (`logOneOff`: "claude: a test on haiku of saved
account <dir> failed: …"), with the helper's whole reason, which the card
clips to 300 bytes, on one line: the home folder and any
`/Users/<name>` or `C:\Users\<name>` become `~`, secrets go through
`redact.Scrub`, and past 800 bytes it keeps the first 300 and the last 500,
so the end of the last screen stays. The `-p` asks keep a temp folder
each, which they remove: `-p` asks nothing about trust.

The Claude Code provider editor's "Claude Code runs as" row
(`claudeModeRow` in app.js, drawn by claudemode.js, which it loads with
its first read) is a switch between Interactive mode (TUI) and claude
-p: pressing one saves `claudePrintMode` (`savePrefs`) and reads the mode again.
It is the only control for it: Settings' second row, which wrote the same
setting and was the one that worked in magpie web (the editor's was
disabled there, and read as a switch that doesn't switch), is gone
(Anotiaww on X, `claude-tui-setting.test.cjs`). Hovering or focusing it opens what tells the two apart. It
reads `GET /api/claude/mode` (`gateway.ClaudeModeNow`) as the page
opens, kept 30 seconds and dropped when the setting changes, not in
`/api/providers`' polls. What decides it for every run on the machine is
`claudeMachineWhy`, whose codes the page words: `off` (the setting),
`missing` (no Claude Code found), `os` and `no-shim`. Beside
the switch: turned off, that every request runs as claude -p; on where
the interactive mode can't run, that Claude requests fail, and why;
otherwise the last request, when it was the interactive mode's. A
subscription with Only switch serves nothing through magpie and shows no
row.

A turn the interactive mode can't take ends the run, and the next starts
anew: a turn that takes earlier turns back, an effort change its tap
can't ask (see the safeguards above). A run given up on
(`letGo`) is not taken back, since the interactive mode can't answer a
control request to stop a turn.

A config folder whose onboarding isn't done (one only `-p` ran in, or
the account Claude Code is signed in to where `claude` was never run,
#4817, steven_4422 on Discord) shows its first-run screens before the
prompt. The helper goes through them as a person would (`ready`,
`onboard`): Enter on the text style (the default kept), Enter on the
security notes (`firstRun`), and Esc on whether to set up the terminal
(`terminalSetup`, shown for a TERM_PROGRAM it knows, such as
Apple_Terminal or vscode), so that nothing of the user's terminal is set
up. Each key goes once its screen has stayed drawn 300ms (`trustSettle`).
Claude Code then records the onboarding done itself (2.1.316:
`hasCompletedOnboarding`, `lastOnboardingVersion`; magpie writes neither
in the user's folder),
and later runs show none of it. Until this change the notes ended the
run, so with the interactive mode On every request and the `/usage` read
failed until the user ran Claude Code once by hand. A screen still shown
after three answers (`onboardTries`) ends the run, saying to run Claude
Code once in a terminal. `TestShimGoesThroughOnboarding` holds it.

If the interactive Claude Code doesn't start or show its prompt within 45
seconds (`claudetui.ReadyWait`), asks how to sign in (`signIn`: a
Claude Code signed in for `-p` only, as in a container given
`CLAUDE_CODE_OAUTH_TOKEN`), stays on its first-run screens, or asks
about a folder the user hasn't trusted, the helper ends, saying why on
its stderr and in its exit (`fail` in `Run`). It never becomes `claude
-p`. Until 11-01 it did, and wrote `<cache>/magpie/claude-tui-broken`,
after which every run went `-p` for up to a day; that mark is no longer
written or read, and one left on disk does nothing.

The helper answers the bridge's `initialize` only once the prompt is
shown and Claude Code has listed the tools of magpie's MCP server. Claude
Code connects its MCP servers beside its prompt, and a first message sent
at the prompt was answered without the agent's tools (MinhuWang on X, a pi
agent whose tool server was slow to load; with the server held 8s, the
real 2.1.309 answered as if it had none). So `startRun` gives
`claude-mcp-helper` a third argument, `listed` in the run's temp folder,
which it writes once it has answered `tools/list`
(`claudetui.MCPCommand`), and `ready` reads that path from the run's
`--mcp-config` (`mcpListed`) and waits for the file as well as the prompt,
within the same 45 seconds; past them it ends, saying the tools weren't
listed. The model test's and warm-up's runs have no such server and don't
wait. `TestShimFirstMessageWaitsForTools` and
`TestToolsListedFileWritten` hold it. So the bridge waits for it `tuiInitWait` (`ReadyWait` and twice a
control request's 10s), not the 10s of any other control request. With
10s, a Claude Code slower than that to start (2 vCPU in Docker) was ended
as the helper waited, and every try was answered 502 "Claude Code has
not answered magpie's initialize request in 10s" (#4504). A helper that
answers nothing even then is ended (`tuiNoStart`), and the turn fails
saying the interactive mode didn't start; the next turn tries it again.
So does a helper that ends saying why Claude Code didn't come up ("claude-tui:
…"), unless Claude Code ended itself first (`claudetui.EndedEarly`, as one
that can't load its session does): a new Claude Code told the whole
conversation waits on the same thing, and the turn was answered 502 after
twice 45s (sowhynot). `TestClaudeTUINoPromptNotTriedAgain` holds both.
A Claude Code that drew nothing in that time (0 bytes read) is said with
what it runs meanwhile, from `ps` (`undrawn`, macOS and Linux): its
Keychain read of its sign-in, `security find-generic-password`, has no
time limit in Claude Code, and one held by a Keychain window or a locked
keychain is named with what to do; any other command is named by its
program alone, never its arguments. The helper doesn't end early on 0
bytes: a Keychain window may still be answered, and Claude Code bounds its
own network waits (about 30s) before it draws.
`TestShimSaysWhatAnUndrawnClaudeWaitsOn` holds it.

## Why turns go on in one run

A run started anew is told the whole conversation in one user message
(`renderClaudePrompt`), a prefix the prompt cache has never seen, so the
conversation is written to the cache again and its images are sent again.
By default, that message holds the history in blocks of `historyChunk`
messages, and magpie's one mark (Claude Code keeps three of Anthropic's four) is on the
last whole block, else on the caller's instructions: a later run started
anew for the same conversation while it is cached reads the history up to an
earlier run's mark, found within Anthropic's 20-block lookback. A
conversation with no history (no reply that calls no tool, as a sub-agent's
loop of tool calls that Claude Code's AgentSummary asks a summary of) has
its every message but the last chunked instead. The turn to answer after a
history is not chunked, and image blocks count toward the lookback.
A turn that goes on in the run that had the last one is told only the
messages since (`renderClaudeTurn`), and Anthropic reads the rest from the
cache.

A run keeps its prompt cache as long as the user picked for Claude Code's
main conversation (`claudeRunTTL`): `FORCE_PROMPT_CACHING_5M`, then
`CLAUDE_CODE_PROMPT_CACHE_TTL`, in magpie's environment or settings.json's
`env`, then settings.json's `promptCacheTtl` (the Agents page's "Prompt
cache · main conversation"), and an hour with none of them, as Claude Code
writes it on a subscription within its limits. The run is given it as
`CLAUDE_CODE_PROMPT_CACHE_TTL` (`cleanClaudeEnvTTL`; a saved account's
Claude Code reads its own folder's settings, not the user's), and magpie's
mark in the first message is as long (`Request.runMark`), since Anthropic
refuses a 1h mark after a 5m one. A session a run goes on from keeps the
lifetime it started with (`cachePrefix.ttl`; one saved before it was kept
is an hour's). Until 11-07 every run was given 1h whatever the pick
(linkaww). `TestClaudeRunKeepsTheUsersCacheTTL`. Every `savedSession`
carries its run's `cachePrefix`: the shelf's (`shelve`), and the branches
cut from a live run's session for a fork's turn (`branch`), a turn sent
again while its run is busy (`retryBranch`) and the turn after the
interactive mode's side question (`branchAt`). Up to v0.1.3266 the three
branches carried none, and their runs went back to an hour under a 5m
pick (linkaww); `everyRunKept5m` in the fork, busy-retry and side
question tests.

`branch` also takes a side request that sends the conversation whole with the
calls its run still waits on dropped, and the words before them kept: Claude
Code's AgentSummary (#6128). The last reply then has no call to find a line
by, so the run is the one with a call line whose conversation is the one
before that reply, and the session is cut before its first call line
(`cutBefore`). Before, such a request was told the whole transcript by a new
Claude Code (90K to 160K cache tokens written for a label). When no branch is
found the log says why, by structure only: `no branch of a run's session for
this request: <reason>`.

A new run (and one started from a transcript, `replaySession`) picks its
lifetime with `runTTLFor`:

- `FORCE_PROMPT_CACHING_5M` on this computer gives 5m, as Claude Code
  does.
- A request whose marks name an hour (`askedCacheTTL`,
  `Request.askedTTL`: the longest TTL marked on its tools, system,
  messages or the request itself) gets an hour: its client picked it.
- Else the pick of the user of the computer the request's Claude Code
  runs on (`Request.pickedTTL`, `claudeTTLOf`). For Claude Code on this
  computer (its token or `claude-cli/` User-Agent from loopback,
  `caller.claudeHere`), it is this computer's, read as `claudeRunTTL`
  reads it. For a request another magpie passed on, it is what that
  magpie named in `X-Magpie-Claude-Cache-TTL` (`ClaudeCacheTTLHeader`):
  `5m`, `1h`, or `default` for no pick. Claude Desktop (`claude-desktop`)
  with no such header is `default` too: its gateway key makes Claude Code
  mark 5 minutes with no ttl, which says no pick (`TestClaudeDesktopRunKeepsAnHour`).
  `passOnCaller` sends it on every
  request to a Remote magpie whose caller is Claude Code here, or that
  came with it. `callerFrom` takes it from a `magpie/` request alone, and
  `withCaller` takes it off what comes in, so no vendor is sent it.
  `default` gives `claudeRunTTL`: this computer's own pick, or an hour,
  as Claude Code keeps it on a subscription.
- Else the marks decide, five minutes for a mark with no ttl.
- Else `claudeRunTTL`.

Claude Code marks five minutes with no ttl, whether its user picked them
or it is its default off a subscription (2.1.316: the marker names a ttl
only for an hour). So marks can't tell a 5m pick from no pick, and an
unmarked side call (Haiku's title, the classifier) says nothing at all.
Up to v0.1.3255 a remote relay wrote an hour's cache for a Mac set to 5m
(Linka). From v0.1.3256 the marks decided, so with nothing picked every
relayed write was five minutes, while a 5m pick still had its unmarked
side calls written for an hour (linkaww). The header carries the pick
itself. A settings.json that can't be read or parsed names no pick
(`claudeSettingsKnown`), and the marks decide as before. Not seen by
magpie: a pick made only in the shell Claude Code was started from, or
in a project's `.claude/settings.json`. Such a client is sent `default`,
an hour, unless its marks name an hour. A Claude Code that goes straight
to a remote magpie, with no magpie of its own, sends no header, and its
marks decide as before. Chat and Responses requests carry no marks and
keep `claudeRunTTL`; warmup and usage runs have no request and keep it
too (`cleanClaudeEnv`). `TestAskedCacheTTL`,
`TestClaudeRunKeepsTheUsersCacheTTL`, `TestClaudeCacheTTLFromAMagpieOnly`,
`TestClaudeCacheTTLOf`, `TestClaudeCacheTTLReachesTheRemoteMagpie`.

## Client-placed cache marks (#1717)

A client can send `X-Magpie-Cache-Marks: client` on a `/v1/messages`
request to a Claude subscription. Without that header, the bridge behaves
as before — except Claude Code's auto mode classifier, whose own marks are
kept like an opted-in client's when they are valid, and dropped as before when
they are not (#3835). With the interactive mode on, the classifier's marks
are kept only when they keep `marksPrintAt` of transcript, and its run is
then `-p` (#5585). Chat Completions and Responses
ignore the header. The mode is
part of the run's identity, including rewritten conversations and tool-result
handoffs: opted-in and ordinary requests never share a run.

An opted-in `-p` run sets `DISABLE_PROMPT_CACHING=1`. Its four cache slots
belong to the client. An interactive run is pasted the words without their
marks, so it keeps Claude Code's own caching and reports the system, message
and tail marks dropped beside the tools' (`clientCache.pasted`); a
classifier whose marks keep a long transcript runs as `-p` instead
(`keptByPrint`). The table below is the `-p` run's. `claude_cache.go` validates the marks before run selection;
`renderClaudePrompt` places them in a new run's first user message.

| Client mark | Bridge behavior |
| --- | --- |
| `cache_control` without `ttl` | Uses `5m`. |
| System block | Ends its own instruction block with the same TTL. |
| Top-level `cache_control` | Sets the request-level tail through `CLAUDE_CODE_EXTRA_BODY`. |
| Last message block | Sets the same request-level tail. |
| Earlier message block, new run | Ends a block at that point in the rendered history with the same TTL. |
| Earlier message block without a mark, new run | Ends a block there too, so a mark that moved on with the conversation's growth leaves the old boundary in place for Anthropic's prefix check (#3835). |
| Earlier message block, running or saved run | Dropped and reported; the existing history cannot be edited. |
| Tool definition | Dropped and reported; Claude Code owns the tools. |
| Bridge history-chunk mark | Uses only a free slot and never a longer TTL than a preceding mark. |

`setSafeguards` updates the tail before each turn or tool-result handoff
that changes its TTL, and clears it when a request no longer asks for it.
An explicit last-block mark and a top-level mark both count toward the
request limit. The bridge represents their shared boundary with one tail
when their TTLs match. Different TTLs at that boundary are rejected. Marks retained
in the first user message survive shelving. A new tail that cannot fit
the retained slots or TTL order starts a new session with the rendered
history.

More than four marks, or a `1h` mark after a `5m` mark in tools → system →
messages order, returns HTTP 400 with Anthropic's `type: error` and
`invalid_request_error` shape. The count includes top-level
`cache_control`. Both error messages and the omitted-TTL default were
checked through a relay to the live Anthropic API.

`X-Magpie-Cache-Marks-Dropped` lists counts by section, such as
`tools:1,messages:3`. It is absent when nothing was dropped. A mark on a
system block removed with the billing header is reported as `system`.
The bridge also reports marks on content it cannot render. Cache usage
retains Anthropic's `cache_creation.ephemeral_5m_input_tokens` and
`ephemeral_1h_input_tokens` values.

The dropped-mark header and its keepalive hold belong only to a Claude
subscription try. An API-key member keeps the client's marks and clears
any report or hold left by an earlier subscription try. Validation still
applies before routing among the members.

The dropped-mark header is best-effort on streaming responses. Validation
and tool-drop counts are known before headers are sent. An opted-in
Claude subscription try waits at most two seconds for run selection to
decide the other drops. Selection releases that hold before process startup or upstream
output. If selection takes longer, the gateway logs the missing report
and sends keepalives without the dropped-mark header. Later fallback can
also change the selected run after headers have been sent. Reporting never
fails the request or delays an ordinary request's keepalives.

## The caller's instructions as the system prompt (`claudeLeanPrompt`)

Every run is the real Claude Code. By default it keeps Claude Code's own
system prompt, and the caller's instructions go after it in the
interactive mode (below), and in the run's first user message inside
`<external_system_instructions>` in `-p` or with client cache marks. The Claude Code
editor's Short Claude system prompt (`claudeLeanPrompt`, off by default)
starts a run with `--system-prompt-file` instead. The file is `system.md`
in the run's temp folder, under the distro's mount in WSL (`claudeCLI.file`).
It holds `leanSystem`: the caller's instructions without a client Claude
Code's billing line, plus the tool-choice line. The first message then holds
the conversation alone. Claude Code still sends its billing block and its
identity line, so requests come from the real binary. They no longer carry
Claude Code's prompt, and that is the ban risk the setting names.

Every run in the interactive mode (not lean, no client marks:
`appendsPrompt`) gets its caller's instructions by
`--append-system-prompt-file` instead (`append.md`, the same `leanSystem`
text), after Claude Code's own prompt, and its first message holds the
conversation alone. The helper pastes a first message as one text without
marks, so instructions pasted there lost the mark `renderClaudePrompt` puts
on their block, and every run started anew wrote them to the cache again:
Claude Code's auto mode classifier read 1,197 tokens and wrote 80k on every
check of one conversation (#2432), and a conversation whose first request
was a one-off ask (no tools, no reply; its run let go after the reply,
`ended`) had its next request's run read 1,273 of 8,565 tokens and write
the rest, the instructions in its replayed transcript where the first run
had them after Claude Code's prompt (m4rs0n on Discord, 11-03). Appended,
they end the system block Claude Code marks itself, which is the same for
every run under the same instructions (only the unmarked billing line
differs; recorded from Claude Code 2.1.316 on 2026-11-03). A session keeps
no system prompt, so a run that resumes one (shelved, branched, or a
replayed transcript, `replaySession`, which then leaves them out of its
user line) is given the file again when the session's run had it
(`savedSession.appended`). The system prompt is part of every run key, so
the file is the one the session's run had. `-p` keeps the instructions in
the first message, where its mark holds. Tested by
`TestClaudeTUIOneOffAskAppendsInstructions`,
`TestClaudeAppendInTheInteractiveMode`,
`TestClaudeTUIConversationKeepsItsInstructionsWhereTheFirstAskHadThem` and
`TestClaudeTUIResumedSessionIsGivenItsInstructions`.

A request with client cache marks (#1717) is never lean: its system marks
are placed in the first message. The mode is part of the run's identity
(`cacheIdentity`, and the tool-results check in `serveSubscription`), so a
run started in one mode never takes a turn of the other. After the setting
changes, the next turn starts a run with the rendered history.

Measured on 2026-10-16 with a real Claude subscription on Sonnet 5.5,
Claude Code 2.1.301:

| Client | Off | On |
| --- | --- | --- |
| Chat client, ~600-token prompt, 2 tools, 3 turns | 10,048 input tokens | 6,580 (−35%) |
| `claude -p` as the client, Read and Bash, 4 requests | 85,453 / 85,471 | 81,948 / 81,977 (−4%) |

The saving is a fixed ~0.9–1.2K tokens a request. It is large for light
clients and small for clients with Claude Code-sized prompts. Without any
tool, Claude Code's built-in tools are already off (`--tools ""`). A run
told the whole conversation again costs far more, because it writes the
history to the 1h cache at 2×.

## Runtime path for a new turn

`serveClaudeSubscription`'s `start` tries, in order:

1. `subscriptionBridge.resume`: an idle run waiting at the conversation the
   request goes on from (`b.idle`). It is written the turn's messages on its
   stdin, as a user message with a uuid it is rewound to if the client gives
   up on the turn (`letGo`).
2. `subscriptionBridge.unshelve`: the saved session of a run let go past
   `idleMost` (`b.shelf`, at most `shelfMost`), which a new Claude Code
   starts from with `--resume`, told the turn's messages alone. It is
   sent `initialize` first, which Claude Code answers only once it has
   loaded the session; one that can't load it exits 1 ("No conversation
   found with session ID") without answering. `start` then lets the
   session go and goes on to step 4 in the same request, logged "Claude
   Code could not go on from the conversation's saved session". Before,
   the turn got 502 "Claude Code ended" with the safety context set, and
   an empty reply without it.
3. `subscriptionBridge.retryBranch` when neither found the run: a turn
   sent again while its run is still busy answering it (#5405,
   margbug01) — the client's connection dropped mid-reply, the request
   was retried, and no cancel reached magpie. A new Claude Code told the
   whole conversation wrote it all to the cache again, only its own
   system prompt and tools read from it: on a long conversation a short
   network blip cost a full cache rewrite, on top of the attempt already
   paid for and thrown away. The retry's run now resumes a copy of the
   busy run's session, cut before the turn (`cutReply`, at `backLine`,
   the line before the turn it was told), and is typed only the turn's
   messages, so the history is read from the cache. The busy run is not
   disturbed; `nil` when no busy run of the account is answering this
   very turn (the request it heard is this one, message for message), the run was started for the turn (no
   `backLine`), the session can't be copied, the client places its own
   cache marks, or the run was taken back or is being taken back — the
   turn told whole, as before.
4. `retire`, then a new run. In the interactive mode, a conversation
   with a reply that calls no tool is given to the new Claude Code as
   its own transcript (`replaySession`, in `claude_replay.go`): a session
   file of two lines, a user line holding the conversation before that
   reply as below (its images as image blocks, client cache marks taken
   out) and an assistant line holding the reply's words (thinking left
   out). The user line tells every message before the reply inside
   `<conversation_history>`, with no `<current_turn>`
   (`renderClaudePromptAt` with the split at the end): the reply answers
   the last of them, and the turn to answer is the one typed after it.
   Before 11-04 the user line was rendered as a first message, so the
   turn the reply answers was marked `<current_turn>`, "the turn to answer
   now", and a resumed run answered an old question again or said its
   tool calls had no results (愛睏柴犬阿茶 on Discord,
   `TestClaudeTUIReplayMarksNoEarlierTurnCurrent`). The run starts with
   `--resume` on it and is typed only the turns after the reply, as a saved session's run is (the session is
   discarded if the run doesn't take it). The file goes in the project
   folder of the work folder with its links followed, which is the one
   Claude Code reads (on a Mac `/var` is `/private/var`). Before 11-01
   the conversation was the first message, pasted: 229 messages and 3
   images that way were refused (#4778), and 25 images of earlier turns
   were pasted one by one until the paste timed out (#4780). A real
   Claude Code 2.1.316 resumed such a transcript of 465KB with 3 images
   and answered from all of it. If the run fails to start from it,
   `start` tries once more told whole (`req.told`). With the interactive
   mode off (`-p`), in WSL (no session folder) and with no such reply,
   the run is told the whole conversation. `retire` takes
   the request: an aside of a run that was told tools (`asideOf`: a
   title or summary asked between turns with `tool_choice` none or with
   no tools) doesn't let go of the run the conversation waits in. When it has
   replies in it, the turns already answered are wrapped in
   `<conversation_history>`, with a note that the images and files in them
   were sent with those messages, and the turn to answer (from the message
   after the last reply that calls no tool, `historyEnd`) in
   `<current_turn>` (#1365). Told as one stretch of Human:/Assistant: text,
   earlier images read as just sent. A first turn, or messages with no
   reply among them, are told as before. `start` logs "a new Claude Code
   is told the whole conversation" with the message and image counts.
   Claude Code notes every image of the message after it, so after the
   turn to answer: `[Image: source: …/images/N.png]` where it keeps the
   images (the session kept on disk), or the size it showed a large one
   at. When the history has images, the turn's opening says those notes
   are the history's, or the history's first and then the turn's
   (`currentTurnOpen`, #1751). Told nothing, the model said a text-only
   turn had an image attached and offered to open the path.
   When the images are all the turn's, as a tool's seven photos in a
   first turn (no history to open the notes' owner), `imageNotesNote` ends
   the turn: the images, tool results' too, are in the message, and the
   notes are Claude Code's record of where it keeps them. Told nothing,
   the model took the paths for the images and said it could not see them
   (a Discord report, v0.1.2862). A turn told to a saved session says the
   same (`renderClaudeTurn`).

A Claude Code that ends before it answers a control request
(`cliControl`: `initialize`, the safety context) is reported with its
exit status and the end of its stderr (`endedBefore`), as one that ends
before it reads its input is (`whyEnded`). The tool-results path and
`resume` already start a new run when the one they found has ended.

A Claude Code whose output ends after its reply began (a `KStart` on the
segment) and before the reply ended (its stop, a refusal, a result or an
error) exited mid-reply: only the `message_start`, or part of the text,
was heard. `readOutput` ends the segment with an error, "Claude Code
exited before it ended its reply" and the end of its stderr
(`replyUnended`, #4822), in `-p` and the interactive mode alike. Before
it, the closed segment was a 200 with nothing or the words so far, as a
reply that ended. Now `relay` answers a stream with nothing sent yet, and
any reply not streamed, a 502, so the spare account may answer (the cut
try keeps its usage row as handed on). A stream already sent ends in the
protocol's error event: Anthropic's `error`, chat's `error` chunk with no
`[DONE]`, Responses' `response.failed`. A reply not streamed whose words
came before any failure is a failure too, as a translated reply cut short
is (`errEndedShort`). `TestClaudeSubscriptionRunExitedMidReplyFails`
holds it on all three protocols, streamed and not, `-p` and interactive.

A streamed reply whose run is quiet past `bridgeHeardAfter` begins
without it and is sent the protocol's pings until the run speaks
(`keepBridgeHeard`, #5043): the replay of a long conversation can take
minutes before its first word, and Claude Code hung up at 599s and
started again from the replay, writing it to the cache once more (#3808).
See [gateway routing](gateway-routing.md) for the keepalive rules.

magpie ends a run (`abort`) by asking Claude Code to end with SIGTERM
(`proc.Tree.Stop`) and kills its tree only once it has exited or
`stopGrace` (5s) has passed; Windows kills the job at once. Claude Code
removes `~/.claude/.oauth_refresh.lock` as it exits on SIGTERM (2.1.299
exits within half a second), and the SIGKILL magpie sent before left the
lock for the next requests to fail on (#2195). The run counts as ended at
once; the gateway stopping (`abortAll`) waits for every tree. Covered by
`TestClaudeAbortAllowsLockCleanup` and `TestTreeStop*`.

## A side question after a reply (#1638)

After each turn Claude Code 2.1.290 asks its prompt suggestion: the
conversation so far, with the turn's system prompt, tools and thinking so
it shares the cache, and one more user message, "[SUGGESTION MODE: …",
with no system message after it. Its request goes on from the turn's
reply like a next turn, so `resume` gives it the conversation's run, and
the run waited at the suggestion's reply. The user's real next turn then
found no run and was told the whole conversation in a new Claude Code
(the reporter's 41 messages, 88k tokens written to the cache, minutes
after the last reply, with nothing in the log).

`ended` now keeps, beside the run's new key, the key it went on from
(`behindKey`) and the uuid of the turn that took it there (`behindTurn`).
A turn that finds no run under its own key takes one whose `behindKey` is
its key, rewinds it to `behindTurn` (as `letGo` does) and tells it only
the turn's messages ("taken back from the side question it answered
since"). The run is marked `rewound`, so it isn't shelved until it has
answered a whole turn since (`replied`, set as a turn ends with `stop`,
cleared at each take-back): the session's lines after the rewind chain
from the line before the side question, which is what `--resume` goes
on from. A run that can't be rewound is let go and the turn told whole
to a new run, as before.

A run let go past `idleMost` while it waits at a side question's reply
is shelved with its `behindKey` too (#4914, ggbdpq: with suggestions on,
the next turn after a let-go read 19,844 from the cache and wrote 29,345;
with them off, 49,560 read and 50 written). A turn that finds no session
under its own key takes the newest one whose `behindKey` is its key, and
the run that takes it resumes a copy cut before the side question
(`cutBehind`): before the `-p` run's message `behindTurn`
(`cutSessionBefore`), or after an interactive run's `behindLine`
(`cutSession`). The shelved file is removed. Resumed whole, Claude Code
would go on with the suggestion's exchange as the conversation's, which
the client never had. A session that can't be cut is let go and the turn
told whole, as before. `TestClaudeLetGoConversationAfterSuggestionResumesItsSession`
holds it, with a stand-in for Claude Code; not tried with the real one. Tried with the real 2.1.290 client in `-p --prompt-suggestions`
and the real 2.1.290 as the run, against a local mock of Anthropic: one
run for the whole session, and the turn after each suggestion went on
from the reply before it. Other side requests that go on from the
conversation (extract_memories, away_summary, /btw) take the same path;
they weren't tried.

An interactive run can't be rewound: the interactive mode ignores
`--resume-session-at`. Each run keeps the uuid of the last session line it
wrote (`lastLine`, from `noteLines`), the one it had before each turn
(`backLine`), and, once it went on with a side question, the one before
it (`behindLine`, beside `behindKey`). A turn that finds an interactive
run behind a side question cuts a copy of its session at `behindLine`
before the run is let go and its file removed (`branchAt`, a cut copy as
in [Tool results another request answers first](#tool-results-another-request-answers-first)),
and a new interactive run resumes the copy, told only the turn's
messages. Before, every turn after a suggestion was told whole to a new
run (bbfatx on X). The interactive mode streams a reply first and tells
its session lines after, read from the file, so as a suggestion begins
`lastLine` may be a line before the reply (its tool call) or, in a run's
first reply, empty. The run also keeps the reply's message id (`lastMsg`
from `replyBegun`, then `backMsg`, `behindMsg` and the shelf's
`behindMsg`), and `cutReply` cuts after that reply's last main-thread
line: by the id alone when no line was heard, waiting up to `cutWait` for
the reply's lines before it falls back to the line (sleepy_shibe, twice).
The shelf (`unshelve`, `cutBehind`) takes a session behind a side
question by either. There is no cut with a client's own cache marks, or
for a run with no session, or neither a line nor a reply; then the turn
is told whole, as before.
`TestClaudeInteractiveSideQuestionBranchesBeforeIt`,
`TestClaudeInteractiveSideQuestionAfterACallKeepsTheReply`,
`TestClaudeInteractiveSideQuestionAfterAFirstReplyOfWordsKeepsTheReply`,
`TestClaudeShelvedSideQuestionAfterAFirstReplyOfWordsKeepsTheReply` and
`TestClaudeCutReplyWaitsForTheReplysLines` hold it.

A turn after a reply that starts a new run logs why none waited
(`whyNoRun`, "claude: no run waits for this turn: …"): its run went on
with a turn this one doesn't follow, waits under another key (the model,
settings, system prompt or tools differ), waits at an earlier turn, is
busy with another request (a turn whose run is busy answering the very
turn sent again goes on from a copy of its session instead,
`retryBranch`, so this line is not logged for one), ended (how long ago,
and why: `endWhy`, set by
`abortFor`, kept in `b.lost` by conversation), or no run here has had the
conversation. Tool results that find no run waiting add what became of
the run that made the calls ("the run that made the calls ended …",
`whyCallsLost`, from `b.lostCalls`, which `removeRun` fills with the calls
the run waited on and the results it was last handed). Tool results a
waiting run can't take (its safeguards can't be set, or it isn't
waiting on those calls) end it with `abortFor`, and the log says why:
"tool results process expired: <why>", where it said "process expired"
alone and the run's end read "magpie ended it" (#4804,
`TestClaudeTUIToolResultsLogWhyTheRunEnded`).

## A turn the client gives up on (`letGo`, #780, #1365)

When the client goes away mid-reply, a run resumed for the turn is told to
rewind (`rewind_conversation`) to the turn's user message, and waits at the
conversation before the turn (`backKey`). The turn's id lasts until its
reply ends without calling a tool: `ended` keeps `turnUUID` and `backKey`
over a reply that calls tools, so a client that goes away while the run
answers its tool results also has the whole turn taken back, its tool
rounds with it. Its next request (the same results sent again, or the next
turn) then finds no run waiting on the calls ("tool results no run
waiting"), is resumed in that run at `backKey` and told the turn since its
user message (logged "a turn taken back goes on in its run"). Before, the
turn's id was dropped with the reply that called the tool, the run was
ended, and that request was told the whole conversation in a new run.
Claude Code 2.1.295 rewinds past a tool round mid-reply (tried against a
local mock of Anthropic). A run started anew for the turn has nothing to
rewind to and is still ended; so is one with a call still in the client's
hands (`pending`). Rewinding it to its first message would leave nothing,
and its results were already handed over its MCP calls, so the results
sent again go to a new run told the whole conversation (#1638's first
case: a 499, then "tool results no run waiting" and 17 messages told).
Keeping its cache would mean taking it back to Claude Code's own
tool_result message and telling the results again as text, which wasn't
done: the reporter's requests aren't known, and a fork that took the
lead's calls (`claimResume`) looks the same from here. The log now says
which run ended and why.

### One key per waiting run

A run waits under one key (`keepIdle`, the only writer of `b.idle`);
kept again, a run leaves the key it waited at, unless another run now
waits there. Which order of calls kept one run twice in the reporter's
gateway isn't known: `ended` and `letGo` of one request exclude each
other (`serveSubscription`'s ended callback runs only when `gone()`
stopped the `AfterFunc` that calls `letGo`), so it isn't those two of one
request. `keepIdle` holds the rule whichever caller it was (#1596). Past `idleMost`, the
eviction loop takes out the key it found as oldest, not the run's own.
Before (X, Sma1lboy: magpie at 158 GB after two days of omp agents), one
run under two keys was the oldest on every pass, the pass took out only
its newer key, and the loop never ended: it held `b.mu`, stopping every
Claude subscription request, and grew its list of runs to let go without
bound. `claude_idle_test.go` (`TestClaudeIdleEvictionEnds` runs the loop
in a child process, which on the old code fails on what the loop
allocates, not on a deadline).

`keepIdle` refuses a run that has ended, and its callers then leave the
run's timer alone (#1667). A resumed turn's 30-minute timer can end a run
(`abort` → `finish` → `removeRun`) while its reply is going out, and the
reply's last event, already in the segment, still reaches the client
whole, so `ended` is told it went well. Before, the ended run was kept,
held one of `idleMost` places until pushed out, and its re-armed timer
found it closed and never took it out. `finish` sets `closed` before
`removeRun` takes `b.mu`, and `keepIdle` reads it under `b.mu`, so a run
is either refused or kept and then taken out by `removeRun`.
`TestClaudeRunEndedMidReplyIsNotKeptIdle` fires the timer before `ended`.

## Finding the run: keys

- `nextTurn` splits a request at its last reply that calls no tool: the
  conversation up to it, and the messages since (with any calls and results
  the client made itself after it).
- `ended` keeps a run whose reply was whole under
  `turnKey(owner, req, messages + reply)`: the account, model, whether an
  effort was asked, thinking off, tool choice, system prompt, the tools, web
  search, schema, and the messages as `hashMessages` reads them. The
  system prompt is taken without the client Claude Code's
  `x-anthropic-billing-header` line (`withoutBillingHeader`), which the run
  is never told and whose fields (`cch`, `cc_prompt_id`, `cc_prev_req`…)
  change every turn; with it in, no next turn found its run (#1638). The
  exact match is that key, from `nextTurn`'s split of the next request.
- It also keeps a `looseTurn`: the same settings without the tools, the
  message count, the conversation before the last turn (hashed; its system
  prompt also without the billing line), and the
  last turn's messages one by one (`messageWords`), the reply last. Shelving
  copies it, with the tool names the run's agent had, to the
  `savedSession`.

### Claude Code's own sign-in before it has a name

The owner in every key is the provider, the account's name and, for Claude
Code's own sign-in, `own` (`ownHome`). `claude auth status` gives no email
until Claude Code puts the account's profile (`oauthAccount`) in its
config, which its first run does. Until then the account is named from its
plan ("Claude Max", `Account.Unnamed`), and its owner ends in `unnamed`
(`unnamedOwner`).

The first run on such an account then names it, mid-turn. Its tool
results came under the email, the owner no longer matched, and the run
was let go ("tool results account changed"). A new run was told the whole
conversation again, about 8k more cache writes. Now a run whose owner is
unnamed takes the named owner of the same provider's own sign-in
(`takeNamedOwner`), and goes on: a run in Claude Code's own home runs as
whatever that home is signed in to. A turn that ends without a tool call
takes the name before its keys are made (`nameOwner`, from
`provider.ClaudeOwnName`), so the next turn finds the idle run. Every
other change of owner still lets the run go. The owner is an atomic
pointer (`owned`), read by the run's reader without a lock.

### A conversation the client rewrote (#912)

Some clients send a turn's conversation back changed. Alma takes the
`[Context: …]` block it gave the latest user message out once the turn is
over, and its `<alma_notification>` block out of the end of the reply, and
picks the tools for each turn. When the exact key misses, `rewritten`
compares the request's `looseTurn` with every idle run's and saved
session's. The request matches one when all of these hold:

- the same owner, settings and conversation before the last turn
  (`looseTurn.key`);
- the last turn has the same messages, roles, tool calls and results, and
  only these changed (`looseTurn.rewrote`):
  - a user message had one part taken out (`cutFrom`), not all of it;
  - the reply is a non-empty start of what the run said.

An aside of a run that was told tools (`asideOf`, above) is never taken
as rewritten: `looseTurn`'s key has no tools, so a title asked with no
tools looked like the conversation rewritten, and the conversation's run
went on with it and waited at the title afterwards (bbfatx on X).
`TestClaudeAsideLeavesTheConversationsRun` holds both.

It is taken only when exactly one run or saved session matches. Two that
could be it are two conversations alike as far as their words go, and a
run handed the other one would answer from what that one was told.

An idle run is taken only when its agent was told every tool offered now
(`offers`). Claude Code is not told of new tools at a turn's start, only
with tool results. A saved session is taken only when the tools offered now
include every one its agent had (`toolsCover`). It is resumed with the new
list, and its earlier calls name no tool that is gone.

The run goes on with the conversation as it told it. Its Claude Code still
holds the context block and the notification. The client's next turn
brings its own context.

### Images the client took out (#1382)

Hana replaces each image before the last reply with a line of text
(`[图片已省略：…]`) in the tool result or user message that held it. Over
Chat Completions, pi-ai sent a tool's images in a user message after the
results ("Attached image(s) from tool result:" and the images); that
message is gone, and each result's text has the line added to its end. So
the step after one that read an image missed the run both ways: its tool
results failed `follows`, and its next turn failed `turnKey` and
`looseTurn.rewrote` (the message count changed).

`lostMedia` (`claude_lost_media.go`) tells this rewrite by its shape, not
by Hana's words. It compares the conversation the run had (kept as each
part's hash, `heardRuns`) with the request's, a role's messages in a row
at a time, and takes it when images or files were taken out and nothing
else changed:

- each image or file is still there, gone, or has text in its place;
- a tool result has text added to its end only when it carried images, or
  the images after the results went;
- the text before those images goes with them only when a result before
  it had text added;
- at least one image or file was taken out.

`follows` checks it when the hash misses (`toldRuns`, logged "tool results
go on in their run, the images before them taken out by the client"), and
`looseTurn.rewrote` when `cut` doesn't match; the looseTurn key no longer
has the message count, which `cut` compares itself. The run's Claude Code
still holds the images; it is told only what came since.

A client that drops the images' message without adding text to a result
goes on only when that message held images alone, no text. A tool result of an image alone (pi-ai's "(see attached image)")
whose text is replaced by the line is not this shape, and starts anew.

## How many Claude Code processes run (`claudeRoom`, #2956)

Every Claude Code magpie starts for a Claude subscription takes a place in
`claudeRoom` (`claude_room.go`) before it starts, and frees it once its
process has exited: each conversation's run (`startRun`), busy, idle or
parked on tool results, and each one-off test, warm-up and /usage read
(`runClaudeTree`). Nothing bounded them before. Claude Code 2.1.303 holds
about 205 MiB RSS idle on Linux arm64, 86–112 MiB of it its own, and a
512 MiB systemd service had 2 busy runs and 14 starting at once when the
kernel's OOM killer ended them. In Docker under a 512 MiB memory cgroup,
six idle runs were enough for two OOM kills.

How many may run (`claudeRunsMost`):

- the user's `claudeRunsMost` in settings.json, set with
  `magpie claude-code runs N` (`auto` clears it). It is this machine's own
  setting (`KeepOwn`): a sync never brings a desktop's number to a server.
- otherwise, on Linux, `(limit − 256 MiB) / 128 MiB`, at least 1, where
  the limit is the least of MemTotal, the cgroup v2 `memory.max` and
  `memory.high` of magpie's cgroup and every one above it, and the cgroup v1
  `memory.limit_in_bytes` (`memLimit`). 512 MiB gives 2, 1 GiB 6, a 964 MiB
  machine with no cgroup limit 5. It is read once per process.
- otherwise no bound: macOS, Windows, and a Linux whose limits can't be
  read. A limit not known is not a small one.

An ask that finds no room (`take`):

1. lets go of the run idle longest, of any bridge. Its session is shelved,
   as a run pushed past `idleMost` is, so its conversation's next turn
   resumes it (`--resume`) and is told only the new messages.
2. otherwise lets go of the run parked longest on tool results, once it has
   waited `roomParkLongest` (1 minute, shorter than `claudeRoomWait`). A
   lead conversation waits on its parallel subagents' Task calls for as
   long as they work, and each subagent needs a Claude Code of its own:
   kept until `parkLongest` (5 minutes), the lead held a place while a
   subagent waited 2 minutes and got the 503 (Wing8Aqua on X, a 768 MiB
   `memory.high` that holds 4). The lead's results then go to a new run
   told the whole conversation. Busy runs, and runs waiting on tool calls
   made within the minute, are never let go.
3. otherwise waits for one to end: woken when a process exits or a run
   goes idle (`nudge`), and it looks again every `roomLook` (5s) for a
   parked run that has come to wait `roomParkLongest`.

A run being ended counts as `leaving`, so several waiting asks don't each
let one go for the same room. After `claudeRoomWait` (2 minutes), or once
the request's context ends, the ask gives up. A conversation's turn is
answered 503 with "N Claude Code processes are running, as many as may run
at once (why); none ended within …" and how to set the number. `start`
doesn't retry without the saved session after such an error. A /usage read
returns it as its error.

Routing treats that 503 (`claudeNoRoom`) as a protected failure. No
account is rested, no other Claude account is asked next (each would wait
for the same room), and the request isn't tried again after a moment. A
member of the group at another provider answers it. Alone, the client gets
the 503.

### Which ones run (`ClaudeRunsPath`)

Each run is two processes: Claude Code, and the MCP helper it starts for
the caller's tools (`magpie claude-mcp-helper`, `internal/claudebridge`).
Claude Code's parallel subagents are conversations of their own, so a
session that starts eight has eight pairs running while they work and
`idleMost` (6) pairs kept for an hour after. Those still waiting on tool
calls stay for `runTimeout` (30 minutes) once their caller stops. A task
manager names Claude Code by its version (its binary is
`versions/2.1.311`), so on macOS Activity Monitor shows these as "2.1.311" and
"magpie", with nothing to say what they are (@Candydidi3 on X).

`GET /v1/magpie/claude-runs` (`claude_runs.go`, loopback only, as the
heap profile) lists each run with its pid, model, account, `state`
(`answering`, `idle` for its next turn, `tools` waiting on the caller's
tools) and `since`. It also gives `ends`, when its timer lets it go
(`endIn` records it at each reset), and Claude Code's RSS. `live` is
`claudeRoom`'s count, one-off asks included. `magpie claude-code runs`
prints the list under the bound and says what the processes are.
On 10-27, a sandbox `magpie serve` with a made-up Claude sign-in and real
Claude Code 2.1.311 against a mock API got 8 concurrent subagent-like
requests. It kept 6 Claude Code and 6 helpers, and the list named the same
6 pids that `ps` showed. Quitting magpie ended all 12 within a second.

## How large one Claude Code process may grow (`claudeRunBudget`, #3256)

`claudeRoom` bounds how many Claude Code processes run, not how large each
is. A run is kept for its conversation's next turn, turn after turn, and a
service that is never restarted kept one for days: on a 31 GiB host a
`node-MainThread` in magpie.service reached 8 GiB RSS and the kernel's OOM
killer took the machine's other work with it.

Each time a run's reply ends, `oversized` (`claude_rss.go`) reads the
resident memory of its process tree (`proc.Tree.RSS`: `/proc/<pid>/status`
VmRSS on Linux, `ps -o rss=` on macOS, the working sets of every process in
its job on Windows, where an npm install's `claude.cmd` is a cmd.exe with
the node under it) and logs it with the pid. The budget is an eighth of the
memory magpie may use (`memLimit` on Linux, `proc.MemTotal` elsewhere), at
least 1 GiB, 4 GiB where neither is known. It is read once per process.

- A whole turn (`ended`, stop) over the budget: the run is not kept idle.
  Its session is shelved at the conversation's key (`letGoOversized`), and
  the next turn resumes it in a new Claude Code (`--resume`), told only the
  new messages, so Anthropic's cache is read as before.
- A reply that called the client's tools (`park`) over 1.5× the budget:
  the run is let go, and the results start a new run told the whole
  conversation. It has no session to shelve there, so it waits for more.

Each Claude Code a run starts logs its pid and model when it starts, and its
run time, how it ended and its peak RSS (`proc.PeakRSS`, rusage
`ru_maxrss`; not on Windows) when it ends: an OOM report's pid can be found
in magpie's log.

## The run's working folder (`clientWorkDir`)

Claude Code tells its model the folder it runs in as the "Primary working
directory". A run has no tools of its own: the client's run in the
client's folder. Run in magpie's shared work folder (`claudeWorkDir`,
`<tmp>/magpie-claude-work-<uid>`), the model was told that folder. It then
wrote the client's files there, through the client's own tools, while the
client's own prompt named another folder. Under a real Claude Code client,
`pelican.html` landed in magpie's folder.

A run therefore starts in the folder the caller names. `clientWorkDir`
reads the system prompt and every message that is not the model's, and
takes the folder the first of them names (in that text, its last line).
It knows four ways of naming it:

- Claude Code: "Primary working directory: X";
- Codex: `<cwd>X</cwd>`;
- OpenCode: "Working directory: X";
- pi: "Current working directory: X".

The first text counts, not the last, so a conversation keeps one folder.
Claude Code says a move (`cd`) as a later "# Environment update" message,
"Primary working directory: /y (was /x)". Taking that one gave a run
started again for the conversation (its saved session resumed, a branch,
a replay) another prompt than its first run: `envTurnsNote` names the
folder, and a run in the caller's own folder has that folder in Claude
Code's environment and saves its session in that folder's project, so
`--resume` found nothing. Either way the prefix after Claude Code's own
part changed and the whole conversation was written to the cache again
(EricWvi on X: …/frontend, then …/frontend/src, the cache read flipping
between under 10% and over 90%). The move still reaches the model in the
conversation, as the caller said it. Not measured: whether a model told
the first folder in `envTurnsNote` after a move misreads the update.

The folder is taken only when it is an absolute path to a folder on this
computer. Otherwise, as for a caller elsewhere, a Windows path on a Mac,
or no folder named, the run starts in the shared folder as before. A
Claude Code in WSL always takes the shared folder.

A run in the shared folder for a caller that names a folder
(`Request.envElsewhere`) has Claude Code's prompt give this computer's
platform, OS and the shared folder, and the caller's give another's. A
model told both spent tool calls finding out which is real (vv_nil on X:
a Mac client of a Linux gateway). Since every tool is the caller's,
`renderClaudePrompt` ends the caller's instructions with
`envElsewhereNote`: the environment they describe is where every tool
runs, and goes before one given earlier. It names no app or vendor, since
it goes upstream. Not measured: how many tool calls it saves on a real
run.

A caller on another computer (`Request.remoteCaller`, not `local`) that
names no folder (`Request.envUnknown`) has only Claude Code's environment
to go on, and that is the gateway's. packinggoods on Discord, a Mac
client of a Windows gateway, had every command wrapped in PowerShell.
Such a caller's instructions end with `envUnknownNote`: the environment
given elsewhere is not where the tools run, so no platform, shell or path
is taken from it. A caller with no instructions of its own gets the
block for the note alone. A caller on this computer that names no folder
is told nothing more: Claude Code's platform and shell are its own.

Claude Code has no option to leave its environment out. In 2.1.312 it
is a `<system-reminder>` in the run's first message, before magpie's
text, with `--system-prompt-file` too; the SDK's `excludeDynamicSections`
only moves it there. So a lean run (`claudeLeanPrompt`) is told the same
in its system prompt (`leanSystem`), the named-folder note worded as
`envElsewhereLeanNote` since the environment comes after it there
(`envNote` picks the note).

Claude Code 2.1.296 and later, as a client, give their own environment
the same way: a `{role: "system"}` message after the user's first, not
in the system prompt. For such a caller (`Request.envInTurns`, from
`clientNamedWorkPlace`) "the environment these instructions describe"
pointed at instructions that describe none, while the caller's came
after them as a message (packinggoods again, after v0.1.2093). Its
instructions, or a lean run's system prompt, end with `envTurnsNote`
instead: of the environments the conversation gives, the last one is
where every tool runs. The run's own is always the first, at the start
of its first message, so the last is the caller's. Not measured: whether
a real model on a Windows gateway then stops writing PowerShell.

Nothing else of the folder reaches the run. `--setting-sources ""` loads
no CLAUDE.md, settings or skills from it. That also kept the user's own
`disableDeepLinkRegistration` from every run, its warm-up and its usage
helper, and each registered the `claude-cli://` URL handler again (#4928):
they are given `--settings {"disableDeepLinkRegistration":"disable"}`,
flag settings, which `--setting-sources` doesn't control
(`disableDeepLinkSettings`). Auto memory is off, and
`CLAUDE_CODE_DISABLE_GIT_INSTRUCTIONS=1` keeps the repository's status out
of the prompt, which would change it, and Anthropic's cache with it,
whenever a file changes. The system prompt magpie sends is unchanged; only
the folder Claude Code runs in is.

A run's sessions are saved in that folder's project
(`<config>/projects/<the caller's folder>`), beside the user's own Claude
Code sessions there when the run is on the user's own sign-in. Claude
Code's `--resume` picker lists them while they last. That project is never
swept: only magpie's own folder is (`sweepSessions`). A run's session is
removed there by its id, when it ends or is discarded (`removeSession`).
A session that a stopped gateway left there, and its temp files, stay
there as the user's own sessions do; a sweep can't tell them apart.

## Files a run leaves

- Its session: `<config>/projects/<work project>/<session>.jsonl`, for
  `--resume` (`claudeSessions`). It is removed when the run ends unless
  shelved or kept for a branch ([A run that ended during a
  turn](#a-run-that-ended-during-a-turn-6528)), and when a saved session
  is discarded (`removeSession`).
  `sweepSessions` removes those a stopped gateway left in magpie's own
  work folder's project, once older than `idleLongest`; a caller's folder
  is never swept (`clientWorkDir`).
- Claude Code's temp files for the session, the images it was given:
  `<CLAUDE_CODE_TMPDIR or /tmp>/claude-<uid>/<work project>/<session>`
  (`claudeTempDir`, not on Windows). They are removed with the session
  (`removeSession`). The sweep removes those whose session file is gone and
  in which nothing changed for `tempLongest` (a day). The folder is
  the run's work folder's for every account, and another account's session files
  are in its own config folder.

A run started anew still sends every image in the conversation. Replacing
earlier images with a placeholder would change what the model sees, and
magpie doesn't do it.

## Tool results another request answers first

A request with tool results finds the run waiting on those calls (`match`)
and claims it (`claimResume`) until the reply it is resumed for ends. More
than one request can carry results for the same calls: Claude Code's fork
sub-agents each start from their lead's conversation as it stands, its
reply's calls answered with the placeholder "Fork started — processing in
background" and the fork's directive after, and they come at once with the
lead's own next turn; a client may also send a turn again while the first
is still answered. The first to claim the run goes on in it. Each of the
others, and one whose run has ended, is a conversation of its own from
there and gets a run started anew (`answeredElsewhere` in the log). It is
never refused: Claude Code doesn't retry a 409, and the agent that sent it
stops (ylorn on Discord).

Such a run doesn't start from the whole conversation (`branch`). Told in
one message, the conversation is a prefix the cache has never seen, so a
900K conversation was written to the cache again for each fork. Aisiverto
on X saw two requests 100ms apart: one read 900K and the other wrote 877K.
Each cost about a fifth of a Pro plan's five hours. The run that made the
calls instead keeps where in its session each call of the client's tools
was written: the uuid on Claude Code's `assistant` line (`noteLines`). As
it parks on them, it also keeps the conversation those calls answered
(`historyKey`, set in `park`), by the ids of the calls the client was
shown (`shown`): the interactive mode tells a reply's session lines after
its stream, read from the file, so the run parks before it has them, and
`noteLines` keeps that conversation when the line comes. Before 11-05 only
lines already heard were given it, and a late one took the next park's
conversation, so an interactive run's branch missed in the usual order:
a fork's directive, or (as kay20475 on X saw it, by its shape) Claude
Code's AgentSummary for a sub-agent, carrying the results the sub-agent's
own next turn had taken, was told the whole conversation in a new Claude
Code each time, pasted as one text, and read only the tools and system
prompt from the cache (6% of 169k, twice 35s apart). A
request no run takes, whose last reply's
calls a live run of the same owner made, from the same conversation, goes
on in a branch of that run's session. The branch's run copies the
session's lines up to the reply's uuid into a session file of its own
next to it, its `sessionId` set to the copy's id (`cutSession`, which waits
up to `cutWait` for that line to be whole), and starts with
`--resume <copy>` in the mode its source run had (`savedSession.tui`):
interactive for an interactive run, `-p` for a `-p` one while the
interactive mode is off. With it on, a branch of a `-p` run's session (one
made before it was turned on) is told the whole conversation in a new
interactive Claude Code instead (`startRun`): its prompt isn't the
interactive mode's, so the cache has nothing of it to read. Until 11-01
it was refused with a 502 (`branch` in `claudePrint`). Claude Code's own
`--fork-session --resume-session-at` cut is not used, as the interactive
mode ignores `--resume-session-at` and resumes the whole session, and a
`-p` branch of an interactive run's session read only tools and system from
the cache (the prompts differ; measured on mote with 30 MCP tools). The
copy is Claude Code's session as it asked it of Anthropic, so it reads the
lead's cache: with the real Claude Code 2.1.316 on mote, a cut copy
resumed interactively read 7648 tokens and wrote 14, and with `-p` read
24009 and wrote 13. The translator passes each interactive assistant row's
uuid through (`TestTranslatorTellsTheRowsUUID`), so an interactive run's
lines can be cut. It is told only the results and what came after, as a
turn after an unshelve is. The lead's session file stays the lead's, and
the branch owns its copy, as an unshelved run owns its session.
Calls it answered nothing for get a synthetic error result. There is no
branch with a client's own cache marks, of a `-p` run under Interactive
only, from a run that was rewound, closed or has no session (Windows and
WSL keep none on disk), or when the conversation before the reply isn't
the one the run answered. Then, and when the cut or the resume fails, the
run is told the whole conversation as before.
`TestClaudeCutSessionCopiesAsFarAsTheLine` and
`TestClaudeForkBranchesOffTheLeadsSession` holds it.

### A run that ended during a turn (#6528)

A run can end in a turn the client still goes on with. It waited on the
client's AskUserQuestion past `runTimeout` ("it waited as long as it
may"), or its client went away mid-reply in a turn that can't be taken
back (`letGo`: the interactive mode, or calls in the client's hands).
Before, its session went with it (`removeRun`), and what came after found
no run and was told whole to a new Claude Code. recih saw an answer given
two minutes after the timeout write 118K tokens to the cache, and a
tunnel drop's two retried sub-agents and their delayed AgentSummary write
305K.

Now `removeRun` keeps such a run's session for a branch to be cut from
(`keepEnded`, [`claude_ended.go`](../../internal/gateway/claude_ended.go)),
in the same `b.mu` section that takes it out of `runs`, so no request
between finds it in neither. The run object is kept as it ended: its
session id and folders, the call lines (`lines`, each with the
conversation its reply answered), `told`, `convKey`, `backLine` and
`backMsg`, and its mode, owner and prompt flags. Kept: a session on disk
with a call line or a turn start to cut at (`keepable`). Not kept: a run
on the shelf (its file is the shelf's), one whose turn was taken back
(`rewound`), one with the client's own cache marks, one whose account was
handed over to Claude Code, and every run as the gateway stops
(`abortAll` sets `stopping` and removes the kept ones; they are not
written to `claude-shelf.json`, and `sweepSessions` takes what a crash
left). Each is kept `endedLongest` (`idleLongest`, the cache's hour), and
`endedMost` (16) at most, the oldest let go first; letting one go removes
its files.

`branch` and `retryBranch` look at the kept runs after the live ones, the
one that ended last first, with the same rules except that a kept run is
closed. Tool results that come after (the late answer, or a retry of a
continuation) are cut after the call's line and told the results; a
delayed AgentSummary on the history before them is cut before the calls
(`cutBefore`); a turn sent again after its run ended answering it (a
retry after a dropped connection) is cut before the turn
(`backLine`/`backMsg`) and told the turn. A kept session is never taken
or written: each branch cuts a copy of its own, so the AgentSummary after
a recovery still branches. The log says the run ended, when, and why
("the run this conversation goes on from ended 2m0s ago (it waited as long
as it may); its session, kept since, is branched").

Claude Code 2.1.316 writes a reply's text and `tool_use` lines when the
reply has streamed, before the tool runs, in both modes; killed (SIGTERM)
while an MCP call is held, it adds only `cost-state` lines, no result and
no interrupt marker (tried on 11-13 with a mock Anthropic and an MCP tool
that never answers). So the kept file has the call line, and a cut after
it leaves the reply's call open for the results the branch is told. No
tool is run again: the branch is told the client's results.
`TestClaudeAnswerAfterTheRunEndedBranchesOffItsKeptSession`,
`TestClaudeEndedRunsSessionsAreBounded`,
`TestClaudeTurnSentAgainAfterItsRunEndedBranchesOffItsSession`.

## The user's words beside tool results (#872, #1878)

A user message can hold tool results and, after them, text blocks of the
user's: Claude Code's AgentSummary asks for its sub-agent's progress label
that way ("Describe your most recent action in 3-5 words. Do not use
tools.", JinJieBeWater), a fork its directive, and an older Claude Code a
message sent while a tool ran (#872). The words are the user's, and both
ways a run is told them keep them apart from the tool's output:

- **A run told the messages as text** (`renderParts`, for a run started
  anew and for a resumed turn): a text or file part after a tool result in
  the same message closes the result, `[end of tool result id=…]`, and is
  marked `[user message]`. Before, it ran on after the tool's last line
  (`SOURCE_LAST_LINEDescribe your…`), and the model read it as tool output
  and went on with the task. A result followed by nothing, or by another
  result, is told as before.
- **Results that go on in their run** (`match`, `continueWith`): `match`
  no longer adds the words to the last result's text, which made them part
  of the MCP result. `saidWithResults` takes them, and `continueWith`
  writes them to Claude Code's input as a user message (`queue`) before it
  hands the results over, and waits for Claude Code's `command_lifecycle`
  "queued" for that message's uuid (at most `queuedWait`, 2s; a version
  that doesn't say goes on after it). Claude Code then tells its model the
  message with the result, as one the user sent while it worked ("The user
  sent a new message while you were working: …"), within the turn. Tried
  with the real Claude Code 2.1.296 against a local mock of Anthropic: the
  next request holds the tool's output, then that reminder, and the turn
  ends with one `result`. Handed over after the results, a message could
  come after the model's reply and start a turn of its own.

What the client's agent puts beside the results is not the user's (#2060).
Claude Code 2.1.296 sends its hooks' notices ("PreToolUse:Bash hook
additional context: …"), its `<total_tokens>` count and a message the user
sent mid-turn as one `{role: "system"}` message after the results; older
versions send them as `<system-reminder>` text blocks in the results'
message. `saidWithResults` takes from a system message only the words
after "The user sent a new message while you were working:" (or "The user
sent the following message:"), without Claude Code's ask around them, and
leaves out a text that `agentNotice` knows: empty within its reminder
tags, a hook's notice of a known kind, a token count, or a lead `userWords`
drops (`injected`). Before, nearly every call of a client with a hook was
followed by "The user sent a new message while you were working:" and the
hook's output, or nothing (the run's Claude Code doesn't show a bare
`<total_tokens>`). `renderParts` closes a result before such a notice
without marking it `[user message]`. A reminder with other words in it is
still the user's, as #872's was.

A background sub-agent's `<task-notification>` (with its "[SYSTEM
NOTIFICATION - NOT USER INPUT]" head) is not the user's either, but the
run's model must hear it. Claude Code 2.1.310 sends one that finishes while
its main agent is mid-turn as a `{role: "system"}` message after the
results (several that finish together share one text block), or as text
after them in their message. `withTaskNotices` appends each to the last
result, inside `<system-reminder>`, as Claude Code itself does for a
notification that lands in a tool's result, and `saidWithResults` strips
them before judging what is left. Before, `agentNotice` dropped them
whole, the run never heard the sub-agent finished, and the main agent
waited for it (stones_2026 on Discord). A notification after a reply is a
turn of its own and is told as it is.

A skill's body is not the user's either. Claude Code sends a loaded
skill's SKILL.md as a text block after the Skill call's result, starting
"Base directory for this skill: ", and `desktopNote` counts that as an
agent notice (#5403), so the run's model got "Launching skill: …" and
nothing it said. `withSkillBodies` appends each such body to the result
of its Skill call (the first that has none yet), or to the result before
it, and the [skill router](skill-router.md)'s entry and every skill
reach the run whole. `TestSkillBodyRealClaudeCode` holds a real Claude
Code request.

Where the result ends is said by the message's parts alone. A tool whose
output says "Do not use tools." has it told as its output, inside the
result. The words of a message that holds images (pi-ai's "Attached
image(s) from tool result:" over Chat, #1382) still go with the result
before them, with the images.

A request with words beside the results still takes the run waiting on
the calls when it comes first (`claimResume`). An AgentSummary that does
is answered in the sub-agent's run, and the sub-agent's own next request
then gets a run started anew, as any request after another answered the
calls does. Starting such a request anew instead would tell the whole
conversation to a new run, a prefix the cache has never seen (#1716,
#1717), for every message sent mid-turn.

## Tool calls written as text

A run told the conversation as text (`renderClaudePrompt`, and
`renderClaudeTurn` with the client's own calls in the turn) is shown each
past call as a `[tool call <name> id=… args=…]` line and each result as
`[tool result id=…]`. The model can copy that form and write its calls as
text: Claude Code has no call to make, the turn ends with end_turn, and the
client shows the lines as the reply's words while nothing runs (Zhenzhen on
Discord: Claude Desktop on Fable 5.1 got
`[tool call mcp__magpie__Bash id=toolu_0Validate bash args={…}]`).

- Such a prompt ends with a note (`pastCallsNote`) that the lines record
  earlier calls and that writing one calls nothing. A prompt with no call in
  it is told as before.
- `readOutput` holds back a reply's words from the first line that opens
  with `[tool call `, `<invoke name=` or `<function_calls>`
  (`textCallOpens`, `textCalls`): the last two are Claude's own call
  written out (packinggoodsllc_07668: Claude Code through a group of
  Claude models was shown `<invoke name="ToolSearch">…` and a
  `*Result: …*` the model made up). Words before it, and lines that only
  start like it, go on as they come. A reply that ends there with end_turn
  and calls none of the client's tools isn't ended: once its turn's
  `result` is in, the run is told `textCallsAsk` as a user message, and the
  reply goes on in the next message (one `message_start`, as after Claude
  Code's own call), so the client gets the real `tool_use` and its results
  go on in the run. It is asked once per reply: written as text again, the
  words reach the client as written. A run with no input to write to, or a
  reply that called the client's tools too (the text is left out), is
  not asked.

## A refusal Claude Code goes on from (Chixi_Fang on X)

Claude Code 2.1.316 doesn't end a turn at a refusal. At the first
`stop_reason: refusal` it writes a `system/informational` line and a
synthetic user message and asks the same model again; at the second it
writes `system/model_refusal_fallback` and asks its fallback model
(claude-opus-4-8 for an Opus 5.5 run), whose answer ends the turn with
`result` `success`. Only when the fallback refuses too does it write
`system/model_refusal_no_fallback` and a `<synthetic>` "API Error: …"
message, and end with `stop_reason: refusal` (`-p`) or
`error_during_execution` with `api_error_status` 0 (the TUI). The TUI's
lines are whole messages; the retracted try is left out of them.

So `readOutput` doesn't end the reply at a refusal from Claude Code's own
model (`refused`, not `theirs`, not a schema reply): the next message goes
on with the same reply, as a continuation does, and its words follow the
ones streamed before. A refusal's `stop_details` (its category) is held
(`heldIfRefused`) and told only if it stands: at a `result` with no API
status, or at the end of the output (`refusal`). The client then gets a
refusal with its stop_details, as before, so the gateway's `hw.refused`
still decides about the next account (b01766d13). A `result` with a
status after a refusal is that error (a retry that failed), not a
refusal. A fallback that answers is a 200 with no refusal anywhere, so
nothing moves to another account.

The model that went on is told on the continuing `KUsage` as `Model` and
`Served`: a stream has named its model at `message_start` and can't
change it, but the try records the fallback model (`Served`, `Swapped`),
and a reply told whole takes it as its model (`collector`). Words a
retracted try streamed before its refusal stay in a stream; Claude Code
shows the user the same. A reply that called the client's tools before
the refusal ends there, as before.

Claude Code's other fallback, on overload (529), needs
`--fallback-model`, which the bridge doesn't pass, as Claude Code itself
doesn't by default; that path is unchanged.

## The account's allowance on the reply (#1257)

Claude Code tells a run's account allowance in its stream-json
`rate_limit_event`, not as HTTP headers; in the interactive mode the
helper tells the same event from the tap (see above). The bridge keeps it
(`provider.NoteClaudeLimits`) and says it again on the reply as the
`anthropic-ratelimit-unified-{5h,7d,7d_oi}-{utilization,reset}` headers
Anthropic sends (`limitHeaders`, from `provider.KeptClaudeLimits`): a share
from 0 to 1 and a Unix-seconds reset, for each kept window that says when
it renews. They are set when the request comes in, from what is kept, and
again as the reply begins (`relay`'s `begin`), once the turn's own
`rate_limit_event` is in. A run on Claude Code's own home that has moved to
another account says nothing (`ClaudeCodeMovedOff`), as its event isn't
kept either.
An event names only some windows (often `five_hour` alone): the rest
stand as last kept, after a restart as `quotas.json` kept them, when
they haven't renewed since (`keptClaudeWindows`; ifccod7391564 on X: the
week's ring went on a cold start, `TestNoteClaudeLimitsKeepsTheWeekOnDisk`).

A Claude Code signed in to claude.ai (its sign-in field set to
`claudeai`, `claudeSignIns` in `internal/agent/claude.go`) reads these into its status line's `rate_limits`, which
claude-hud shows. One that signs in to magpie with magpie's key
(`ANTHROPIC_AUTH_TOKEN`) ignores them: Claude Code 2.1.293 reads the
headers only for a claude.ai subscriber. A reply another provider answered
carries none.

## A restart to update

A run is a Claude Code process of this magpie's, so a restart to update
that hands the gateway to the new version ([GUI shell](gui-shell.md#a-restart-to-update-hands-the-gateway-over))
waits for the bridge's turns, and for none of the gateway's other requests
(`restartBusy` in [`handover.go`](../../internal/gui/handover.go): `Busy.Bridge`,
`Tools`, `BridgeLast`). A run's tool calls go to the gateway's port, which
the new one serves once it has the socket; a `claude-mcp` token the new one
doesn't know is forwarded to the old one's callback socket
(`ForwardCallbacks`, `forwardCallback`), which serves it until it quits
(`ServeCallbacks`). A run idle between turns ends with the old process, but
its session does not (#6292, recih on X): as the gateway ends
(`abortAll`), `keepShelf` shelves every idle run and writes the shelf to
`claude-shelf.json` beside the settings (temp and rename;
[`claude_persist.go`](../../internal/gateway/claude_persist.go)). Each entry
has the conversation's fingerprint (`turnKey`, which holds the account, the
model and the settings), Claude Code's session id, its project folders (the
config folder, so HOME, and the work folder), the account, the tools it was
told of, and the cache marks. The next gateway's `unshelve`, when its shelf
has nothing for a turn, reads the file once it changed (`loadShelf`, which
takes the file away, so a session that went on in a run is never resumed
from a record of where it stood) and the turn starts Claude Code with
`--resume`, told the turn alone. A file that doesn't parse whole, another
version, an entry whose session file is gone, whose folder isn't a Claude
Code project folder, or that waited longer than `idleLongest` is unknown,
and the turn is told whole to a new run, as after a crash (no file is
written then). A conversation a client rewrote (`looseTurn`) is not kept,
and a run in a turn or parked on tool results at the stop is not either.
`TestClaudeIdleRunSurvivesARestart`, `TestClaudeShelfFileUnreadableFallsBackToReplay`.

### A turn refused for the account's limit

A turn Anthropic refuses for the account's limit before any of its reply
(429, "You've hit your limit · resets 9pm") ends its run, as any turn with
no answer does (`relay`'s abort). Before, the session went with it, and the
turn asked again after the reset, hours later, was told whole to a new
Claude Code (#6292, recih: 354,900 tokens written to the cache). Now
`relayFailing` tells the abort the status and words the client gets
(`bridgeFailed`), and on a 429 `keepForReset`
([`claude_persist.go`](../../internal/gateway/claude_persist.go)) shelves a
copy of the run's session cut before the refused turn, so neither its user
line nor Claude Code's synthetic error line is in it, under the turn's key
(`nextTurn`). The cut is at `backLine`/`backMsg` for an idle run resumed for
the turn, or at `fromLen`, the length of the saved session's file a run was
started from (unshelve, a branch, a replay), while it has answered no turn:
a turn asked again while the account is still limited is shelved again,
cut at the same place. A run started for the turn (nothing before it in its
session), a conversation taken back and not gone on with, or one with the
client's own cache marks keeps nothing.

The entry waits until `until` (`expires`), not `idleLongest`: the reset the
refusal names (`resetsAt`), else the latest reset of the account's windows
that are used up (`KeptClaudeLimits`, from Claude Code's
`rate_limit_event`), else 5 hours (`quotaShelfUnsaid`), plus
`quotaShelfMargin` (1h), never less than `idleLongest` and never more than
`quotaShelfLongest` (`longestQuota`). `until` is written to
`claude-shelf.json`, so the entry survives a restart while the account
waits; an entry whose `until` is further than `quotaShelfLongest` past its
`at` is unknown. The shelf still holds `shelfMost` at most, the longest
waiting let go first (`trimShelf`).
`TestClaudeTurnRefusedForTheLimitKeepsItsSessionTillTheReset` (both modes:
two refusals, a restart, then the turn resumes the cut session told the
turn alone), `TestQuotaShelfFor`.

## Verification

```sh
go test -tags nogui ./internal/gateway/ -run '^TestClaudeClientCache' -count=1
go test -tags nogui ./internal/gateway/ -run '^TestClaudeLeanPrompt' -count=1
go test -tags nogui ./internal/gateway/ -run '^TestClaudeTUIChosen$' -count=1
go test ./internal/claudetui/ -count=1
go test -tags nogui ./internal/claudecode/ -count=1
go test -tags nogui ./internal/claudecode/ -run TestKeepCurrent -count=1
go test -tags nogui ./internal/gateway/ -run '^TestClaudeCodeGivenTheBridgeForASOCKSProxy$' -count=1
go test -tags nogui ./internal/provider/ -run '^TestClaudeSignInBridgesASOCKSProxy$' -count=1
go test -tags nogui ./internal/netproxy/ -run '^TestEnvForBunWith' -count=1
go test -tags nogui ./internal/gateway/ -run '^TestClaudeToolNamesComeBackAsDeclared$' -count=1
go test -tags nogui ./internal/gateway/ -run 'TestClaudeRunsHeldToTheRoom|TestClaudeRoom|TestClaudeOneOffsTakeRoom|TestClaudeMemRuns|TestClaudeRunsMost|TestClaudeNoRoom' -count=1
go test -tags nogui ./internal/cli/ -run '^TestClaudeCodeRuns' -count=1
go test -tags nogui ./internal/gateway/ -run '^TestClaudeRunsNamesEachProcess$' -count=1
go test -tags nogui ./internal/settings/ -run '^TestClaudeRunsMostIsMachineLocal$' -count=1
go test -tags nogui ./internal/gateway/ -run 'ClaudeSubscriptionRunsTheDownloaded|ClaudeSubscriptionWithoutClaudeCode|ClaudeRewritten|LostMedia|ImagesTakenOut|RunLetGoWhenTheClientRewrote|ClaudeSessionTempFiles|ClaudeLetGo|ClaudeSessionFiles|ClaudeOldSessions|ClaudeSubscriptionReplySaysTheAllowance|ClaudeAllowanceHeaders|ClaudeForksAnsweringTheLeadsCalls|ClaudeTurnGivenUpOn|ClaudeToolResultsGivenUpOn|ClaudePromptMarksEarlierTurns|ClaudeHistoryCachedAcrossNewRuns|ClaudeUnloadableSession|ClaudeEndedRunSaysWhy|ClaudeIdle|ClaudeSideQuestion|ClaudeInteractiveSideQuestion|ClaudeCutSession|ClaudeForkBranches|ClaudeToolChoiceNone|ClaudeAside|ClaudeNoRunSaysWhy|ClaudeToolResultsNoRunSaysWhy|ClaudePromptToldUserText|ClaudePromptKeepsInstructions|ClaudeToolResultsGoOnWithTheUsersWords|ClaudeToolResultsKeepInstructions|ToolResultKeepsUserTextSentMidTurn|ClaudeTextToolCalls|ClaudeTextCallsHoldsOnlyTheLine|ClaudePromptSaysPastCallsAreARecord' -count=1
go test -tags nogui ./internal/gateway/ -run 'TestClaudeTaskNotification|TestWithTaskNotices|TestClaudeTurnToldTheTaskNotification' -count=1
go test -tags nogui ./internal/gateway/ -run 'TestClaudeAnswerAfterTheRunEnded|TestClaudeEndedRuns|TestClaudeTurnSentAgain|TestClaudeBranchFor' -count=1
```

`claude_rewritten_test.go` has a case for each relaxation and one for each
rule that keeps another conversation out; `claude_lost_media_test.go` does
the same for images taken out, from Hana's request bodies. `claude_resume_test.go`'s harness
stands in for Claude Code with a script that keeps sessions as it does.

## Which entry a request used

Each request the bridge answers notes how its run started: `startRun` calls
`noteEntry(ctx, tui, code)` beside `noteClaudeRun`, which is the process's
last mode and so says nothing of one request. The note rides the request's
context (`withEntry`, `claude_entry.go`) and `serve` copies it onto the try
(`Try.Entry` "tui" or "print", `Try.EntryWhy` the `claudePrint` code that
chose -p: off, os, missing, no-shim, image, third-party, cache-marks). The
Routing page's request row shows a TUI or -p chip with the reason in its
title (`entryNote` in the routing plugin's client.js), so the plan cost of
a request can be told from how it ran (Anotiaww on X,
`TestEntryNoted`, `routing-entry.test.cjs`).

## Claude Code on a remote magpie

A local Claude Code that uses a remote magpie holding the account sends its
whole history each turn to the remote gateway, as any API client does. The
remote bridge keeps the run's session and resumes it (`--resume`), typing
only the turn that is new (`from.since`), so Anthropic is not sent the
history twice. A new run (first request, a restart, a branch, a session that
isn't found) replays the conversation once as one message, with a cache
miss. The remote Claude Code adds its own system prompt (about 1K tokens,
cached) beside the caller's, which the Short Claude system prompt setting
(`claudeLeanPrompt`) removes (Tossipe on X).
