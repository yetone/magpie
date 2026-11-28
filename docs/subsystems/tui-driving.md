# TUI driving

`internal/tuidrive` runs an agent's interactive terminal UI in a terminal
of magpie's own and lets a driver read what it draws and type to it, as a
person at a keyboard does. The agent is the real program in its real
interactive mode: nothing is translated or re-implemented, so what the
vendor sees is its own client's requests.

The first driver is Claude Code's (`internal/claudetui`, the
[Claude subscription bridge](claude-subscription-bridge.md)'s interactive
mode). The package holds what is the same for every agent; what an agent's
screens say, how a message gets into its prompt and where its answers are
read from belong to that agent's driver.

## What the package owns

- **The terminal** (`Term`, [`term.go`](../../internal/tuidrive/term.go)).
  `Start(path, args, env)` runs the program in a terminal of `Cols` by
  `Rows` (200 by 50). The environment is the driver's, whole: the package
  adds nothing to it.
  - On macOS and Linux it is a pty from `/dev/ptmx` (`pty_darwin.go`,
    `pty_linux.go`), the agent in a session of its own with the pty as its
    controlling terminal ([`term_unix.go`](../../internal/tuidrive/term_unix.go)).
    `Stop` sends SIGTERM, then SIGKILL to the process group after 3s.
  - On Windows it is a pseudo console
    ([`term_windows.go`](../../internal/tuidrive/term_windows.go)). On
    Windows 10 it prefers a `conpty` folder beside magpie.exe, then the
    ConPTY a release carries (`internal/conptypair`), over the system's,
    which drops paste marks (#5205; `OldConsole` says when the system's is
    in use). `Stop` closes the console, then kills the agent after 3s.
  - `EndSignals` are the signals a driver ends on, its agent with it.
  - `Attach(rw)` reads a terminal a program already runs in. Tests use it.
- **Reading** (`read`). It keeps what the agent draws, answers the
  cursor-position (`ESC[6n`) and device-attributes (`ESC[c`) queries, then
  the driver's own `Replies` (each `Ask` seen is answered with its
  `Answer`; Claude Code's driver sets none), and goes on past EAGAIN from a non-blocking master (#4834). `Ended` records
  the exit only once what the agent wrote before it has been read. On a
  terminal that stays open (Windows) that means until it goes quiet. Either
  way it waits 2s at most (#5283).
- **The screen** has three views:
  - `Plain` is what was drawn since the last `ClearScreen`, escapes taken
    out (`ANSI`).
  - `Now` is the screen as it stands, on a `Grid`
    ([`screen.go`](../../internal/tuidrive/screen.go)). A pseudo console
    sends only the cells that changed, so the stream alone misreads a
    redraw. The grid is made on the first bytes drawn.
  - `Seen` is `Plain`, or `Now` once nothing has been drawn since the clear
    for `redrawWait` (1.5s): the key was turned away, and nothing will be
    drawn for it.

  `LastScreen` is the tail an error quotes. With nothing drawn it says so,
  and adds the driver's `Undrawn` reason.
- **Hooks** run under the screen's lock (`Locked`), so what a driver keeps
  of the screen changes with it. `OnDraw(g)` runs after each piece drawn,
  and `OnClear` runs on `ClearScreen`. Claude Code's driver keeps the
  `/usage` panel this way: it can be drawn only for a moment.
- **Dialogs** (`Dialog`, [`dialog.go`](../../internal/tuidrive/dialog.go)).
  A dialog is matched at the foot of the screen. Its option rows (by their
  words) must have no more than `Foot` characters under them, and its words
  must be within `Body` of them. The same words further up are history (a
  resumed conversation), not the dialog. `Heading` matches a screen by its
  heading. `LastMatchNear`, `LastAt` and `TailAfter` are the pieces.
- **What a silent agent waits on** (`Waiting`,
  [`waiting_unix.go`](../../internal/tuidrive/waiting_unix.go)). It lists
  the agent's descendants with their age, and the agent's RSS, from `ps`.
  There is none on Windows.
- **`Deadline`** reads a caller's deadline given as Unix milliseconds.

## What a driver owns

- the program's arguments and environment (`childEnv` for Claude Code);
- the state machine from start to ready: which dialogs to answer and how
  (trust, onboarding, sign-in);
- how a message is typed or pasted, and how to tell it arrived whole (paste
  marks, `imagePad`, `cutHint` on top of `OldConsole`);
- where its answers come from. Claude Code's driver reads its transcript and
  the tap, never the screen;
- the words of its errors, and `Undrawn` (Claude Code names a Keychain wait);
- any terminal query its agent waits on beyond the two the package answers
  (`Replies`). agy 1.5.17 asks for the secondary device attributes
  (`ESC[>c`) and draws nothing until it has them.

## Another agent: agy

`TestDrivesAgy` ([`agy_test.go`](../../internal/tuidrive/agy_test.go))
drives a real agy (Antigravity's CLI) with the package and nothing of
Claude Code's. It runs when `MAGPIE_TEST_AGY_BIN` names an agy. agy starts
in a sandbox HOME, on an API key, against a fake Gemini on loopback. Every
other host goes to a proxy that isn't there, and `SSH_CONNECTION` keeps
agy's sign-in out of the system keyring. The test:

1. waits for agy's prompt (`? for shortcuts` in the footer, no
   `esc to cancel`);
2. pastes a message with paste marks, then Enter;
3. reads the answer from agy's transcript
   (`~/.gemini/antigravity-cli/brain/<id>/.system_generated/logs/transcript_full.jsonl`,
   the `PLANNER_RESPONSE` row);
4. checks the answer on `Now`.

agy draws in place with relative cursor moves, not on the alternate
screen, so `Plain` and `Seen` have its answer in pieces; the grid has it as
shown.

Magpie serves Antigravity through the agy-cli plugin, which runs the real
agy over ACP (`agy acp`). On an API key, the TUI, `-p` and ACP send the same
main request, headers included, and the TUI and ACP both send the title
request first. `-p` is the one that can be told apart. The Google sign-in
path was not compared, as that needs a real account. So no agy driver
serves requests yet. The test shows the package carries one.

## Contracts

- The package changes no driver's behaviour. It was taken out of
  `internal/claudetui` with the same sizes, timings, escape answers and
  ordering. `Feed` is the one way bytes reach the screen; `read` uses it,
  and tests feed recorded screens through it.
- Hooks are called under the lock: they must not call `Locked`, `Now`,
  `Plain` or anything else that takes it. Inside a hook, use `NowLocked`
  or the grid it is given.
- `Changed`, `Exited`, `Exit`, `Plain` and `LastScreen` are safe on a nil
  `*Term`, as a driver's zero value is in its tests.

## Verification

```sh
go test ./internal/tuidrive/ ./internal/claudetui/ -count=1
GOOS=windows go vet ./internal/tuidrive/ ./internal/claudetui/
```

`TestEndedAfterLastWordsRead` and `TestReadPastEAGAIN` cover reading.
`TestDrivesAgy` (with `MAGPIE_TEST_AGY_BIN`) drives a real agy.
`TestGridDrawsInPlace` covers the grid. `TestDescendants` and `TestEtime`
cover `Waiting`. The Claude Code driver's own suite (`internal/claudetui`
and the gateway's `claude_tui_*` tests) runs the whole path against a fake
Claude Code in the pty.
