# WorkBuddy answers 400 `Illegal API invocation from an unapproved channel`

> What a chat WorkBuddy's gateway turns away, why magpie passes it to the
> agent instead of failing over, and the fix in this branch.

## What happens

With Claude Code pointed at magpie, a routing group whose first member is
a WorkBuddy model (`workbuddy-ai/deepseek-v4.1-flash`, say) stops on the
first try: WorkBuddy answers

```
400 {"error":{"message":"WorkBuddy AI: Illegal API invocation from an unapproved channel"}}
```

and Claude Code sees exactly that — the group's other members are never
tried. The same happens on the China build (`WorkBuddy: …`).

## Why

WorkBuddy's gateway has an identity check: it turns a chat away when the
**first system message opens with Claude Code's own identity**. Its own
client's system prompt never does, so the answer reads as "another
channel". Two openings are refused:

- `x-anthropic-billing-header` — the billing line Claude Code's CLI puts
  first in its system prompt
- `You are Claude Code, Anthropic's official CLI for Claude.`

Everything else about the request is ordinary: the same words anywhere
else are answered as usual.

### The exact boundary (asked against both builds)

- Only the **very start** of the first system message counts, and
  case-insensitively: **any one character in front** — a letter, a
  leading `\n`, a space, a tab — passes.
- The same words on a **later line** of the first system message pass.
- The same words in a **second system message** pass.
- The identity line needs to run to `…for Claude` to be refused at all:
  cut short at `…official CLI for` it passes, `…for Claude` without the
  full stop is still refused.
- No whitespace normalisation: `You  are  Claude  Code` (double spaces)
  passes.
- Only Claude Code's identity is checked. Codex's ("You are Codex, based
  on GPT-5. You are running as a coding agent in the Codex CLI…"),
  Hermes's, Cline's, Cursor's, Qoder's and Kimi's own system prompts are
  served as they are.

## Why magpie does not fail over

`retryable` (internal/gateway/fallback.go) reads a 400 as another
provider's business only when its body names a quota or a model it does
not serve:

```go
case status == 400, status == 422:
    return quotaWords.Match(body) || unservedWords.Match(body)
```

`Illegal API invocation from an unapproved channel` names neither, so the
error is taken to be the request's own — "an error another account
wouldn't fix" — and handed to the agent with the group's other members
left unasked.

## The fix

`wbBody` (internal/provider/workbuddy.go) already puts a neutral system
line in front of a chat that has none. This branch has it do the same
when the chat's first system message **opens with one of the refused
openings**:

- a neutral `You are a helpful assistant.` goes first,
- the agent's own prompt stays, second, so Claude Code is still told who
  it is,
- the body is otherwise untouched, and other agents' requests are byte
  for byte as they were.

WorkBuddy's gate then sees a chat its own client could have sent, and the
agent keeps its identity.

### Tested

- `TestWorkBuddyRefusedPrompt` (new): the openings refused (both lines,
  with and without the full stop, uppercase, billing line first), Claude
  Code's blocks shape keeping its prompt second, and the bodies left as
  they were — a character in front, a later line, a second system
  message, another agent's identity, the words cut short, a plain
  prompt, not a chat, not JSON.
- Against the live gateway of both builds: Claude Code's system prompt as
  sent — 400 before the change, answered after it, with the neutral line
  first and the prompt second.
- `go build -tags nogui`, `go test -tags nogui ./internal/provider ./internal/gateway`.
