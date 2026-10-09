# Claude subscription bridge: turns across Claude Code runs

A Claude subscription's requests are answered by the local `claude` binary
(`internal/gateway/claude_subscription.go`, `serveClaudeSubscription`). This
page covers how a conversation's turns find the Claude Code process, or the
saved session, that had its last turn, and what a run leaves on disk. Tool
calls within a turn (`match`, `continueWith`) are only touched on where they
meet it.

## Why turns go on in one run

A run started anew is told the whole conversation in one user message
(`renderClaudePrompt`), a prefix the prompt cache has never seen, so the
conversation is written to the cache again and its images are sent again.
A turn that goes on in the run that had the last one is told only the
messages since (`renderClaudeTurn`), and Anthropic reads the rest from the
cache.

## Runtime path for a new turn

`serveClaudeSubscription`'s `start` tries, in order:

1. `subscriptionBridge.resume`: an idle run waiting at the conversation the
   request goes on from (`b.idle`). It is written the turn's messages on its
   stdin, as a user message with a uuid it is rewound to if the client gives
   up on the turn (`letGo`).
2. `subscriptionBridge.unshelve`: the saved session of a run let go past
   `idleMost` (`b.shelf`, at most `shelfMost`), which a new Claude Code
   starts from with `--resume`, told the turn's messages alone.
3. `retire`, then a new run told the whole conversation. When it has
   replies in it, the turns already answered are wrapped in
   `<conversation_history>`, with a note that the images and files in them
   were sent with those messages, and the turn to answer (from the message
   after the last reply that calls no tool, `historyEnd`) in
   `<current_turn>` (#1365). Told as one stretch of Human:/Assistant: text,
   earlier images read as just sent. A first turn, or messages with no
   reply among them, are told as before. `start` logs "a new Claude Code
   is told the whole conversation" with the message and image counts.

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
hands (`pending`).

## Finding the run: keys

- `nextTurn` splits a request at its last reply that calls no tool: the
  conversation up to it, and the messages since (with any calls and results
  the client made itself after it).
- `ended` keeps a run whose reply was whole under
  `turnKey(owner, req, messages + reply)`: the account, model, whether an
  effort was asked, thinking off, tool choice, system prompt, the tools, web
  search, schema, and the messages as `hashMessages` reads them. The exact
  match is that key, from `nextTurn`'s split of the next request.
- It also keeps a `looseTurn`: the same settings without the tools, the
  message count, the conversation before the last turn (hashed), and the
  last turn's messages one by one (`messageWords`), the reply last. Shelving
  copies it, with the tool names the run's agent had, to the
  `savedSession`.

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

## Files a run leaves

- Its session: `<config>/projects/<work project>/<session>.jsonl`, for
  `--resume` (`claudeSessions`). It is removed when the run ends unless
  shelved, and when a saved session is discarded (`removeSession`).
  `sweepSessions` removes those a stopped gateway left, once older than
  `idleLongest`.
- Claude Code's temp files for the session, the images it was given:
  `<CLAUDE_CODE_TMPDIR or /tmp>/claude-<uid>/<work project>/<session>`
  (`claudeTempDir`, not on Windows). They are removed with the session
  (`removeSession`). The sweep removes those whose session file is gone and
  in which nothing changed for `tempLongest` (a day). The folder is
  the work folder's for every account, and another account's session files
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

## The account's allowance on the reply (#1257)

Claude Code tells a run's account allowance in its stream-json
`rate_limit_event`, not as HTTP headers. The bridge keeps it
(`provider.NoteClaudeLimits`) and says it again on the reply as the
`anthropic-ratelimit-unified-{5h,7d,7d_oi}-{utilization,reset}` headers
Anthropic sends (`limitHeaders`, from `provider.KeptClaudeLimits`): a share
from 0 to 1 and a Unix-seconds reset, for each kept window that says when
it renews. They are set when the request comes in, from what is kept, and
again as the reply begins (`relay`'s `begin`), once the turn's own
`rate_limit_event` is in. A run on Claude Code's own home that has moved to
another account says nothing (`ClaudeCodeMovedOff`), as its event isn't
kept either.

A Claude Code signed in to claude.ai (its sign-in field set to
`claudeai`, `claudeSignIns` in `internal/agent/claude.go`) reads these into its status line's `rate_limits`, which
claude-hud shows. One that signs in to magpie with magpie's key
(`ANTHROPIC_AUTH_TOKEN`) ignores them: Claude Code 2.1.293 reads the
headers only for a claude.ai subscriber. A reply another provider answered
carries none.

## Verification

```sh
go test -tags nogui ./internal/gateway/ -run 'ClaudeRewritten|LostMedia|ImagesTakenOut|RunLetGoWhenTheClientRewrote|ClaudeSessionTempFiles|ClaudeLetGo|ClaudeSessionFiles|ClaudeOldSessions|ClaudeSubscriptionReplySaysTheAllowance|ClaudeAllowanceHeaders|ClaudeForksAnsweringTheLeadsCalls|ClaudeTurnGivenUpOn|ClaudeToolResultsGivenUpOn|ClaudePromptMarksEarlierTurns' -count=1
```

`claude_rewritten_test.go` has a case for each relaxation and one for each
rule that keeps another conversation out; `claude_lost_media_test.go` does
the same for images taken out, from Hana's request bodies. `claude_resume_test.go`'s harness
stands in for Claude Code with a script that keeps sessions as it does.
