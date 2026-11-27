---
name: magpie-api
description: Read magpie's (usemagpie.ai) own HTTP API from an agent — what is left of every subscription, coding plan and key balance (GET /v1/magpie/quotas), the models available now (GET /v1/models), and which model a routing group picked for a session and what it fell back from (GET /v1/magpie/route). Use when orchestrating work across models or agents through magpie: choosing a model whose provider still has allowance, listing what can be asked for, or following where a turn went.
---

# magpie API

[magpie](https://usemagpie.ai) serves every model the user has (subscriptions,
API keys, local models, routing groups) on one local gateway, and tells what
is left of each. Read these endpoints instead of calling vendors yourself.

## Find the gateway and key

- If magpie wired you, your own requests already go to it: the base URL of
  the provider magpie added to your config (for Claude Code,
  `ANTHROPIC_BASE_URL` in your environment). Use it without a trailing `/v1`.
- Else `magpie status --json` prints its `url`; else try
  `http://127.0.0.1:3425`. `GET /api/hello` answers `{"name":"magpie",…}`.
- Send the key your own requests to magpie carry (for Claude Code,
  `$ANTHROPIC_AUTH_TOKEN`) as `Authorization: Bearer <key>` (or
  `x-api-key: <key>`). Never print or store it.

On this computer any key, or none, is taken. From another computer magpie
answers only while it is shared on the local network and the request carries
one of its gateway keys. A key held to a budget, models or accounts is told
only what it may use; refused, it gets 403 with `error.code` `gateway_key_held`
(`GET /v1/magpie/limit` tells such a key its own limit).

```sh
B=${ANTHROPIC_BASE_URL:-http://127.0.0.1:3425}; B=${B%/v1}
curl -s "$B/v1/magpie/quotas?kept=1" -H "Authorization: Bearer $ANTHROPIC_AUTH_TOKEN"
```

## GET /v1/magpie/quotas

What is left now. It asks the vendors (up to about 12 seconds); `?kept=1`
gives the last readings without asking anyone, for frequent checks.

- `object`: `list`.
- `data`: one entry per account, coding plan or key.
  - `provider`: the models that spend it are those whose `/v1/models` id
    starts with `<provider>/`.
  - `name`, `user`, `plan`: the provider's name, the account (email, login
    or key name) when it has several, and the plan's name.
  - `kind`: `subscription` (a signed-in account), `plan` (a coding plan
    bought with a key) or `balance` (a key's money).
  - `windows`: each usage window.
    - `name`: "5 hours", "Weekly"….
    - `used`, `remaining`: percent, 0 to 100. `remaining` 0 is used up
      until `resetsAt`.
    - `resetsAt`: when it starts again (RFC 3339).
    - `unlimited`: it has no limit.
    - `display`, `amount`, `limit`, `unit`: the vendor's own count, when it
      counts one ("1.2k / 3k").
    - `text`: the window is its `display` alone (credits held, a gift to
      claim); its `used` is no reading.
    - `balance`: what is left, in `unit`, of a wallet with no total; its
      `used` is no reading either.
  - `balance`: what a key or account holds ("$12.40", "1.2K credits").
  - `error`: why it couldn't be read. That is unknown, not empty: say so.
  - `asOf`, `readError`: the reading shown is one kept from `asOf`, since a
    new one failed (`readError` says why).
  - `readAt`: when what is shown was read, when known.
  - `lastServedAt`, `last`: when it last answered through magpie, and
    `last` on the one that answered most recently.

## GET /v1/models

The models the user has on now, chat models only unless asked
(`?kind=image,video,embedding,rerank` or `?kind=all`).

- `data`: one entry per model.
  - `id`: what to send as `model`: `provider/model`, or a routing group's
    `group/<name>` or `auto/<model>`, which picks a member and falls back.
  - `owned_by`: the provider serving it (a routing group's first member's).
  - `display_name`: its name.
  - `reasoning`, `reasoning_levels`: whether it reasons, and the efforts it
    takes (`low`, `high`…).
  - `context_window`, `max_output_tokens`: tokens, when known.

A key held to some models is listed those alone. Requests go to
`/v1/chat/completions`, `/v1/responses` or `/v1/messages` on the same base.

## GET /v1/magpie/route

Where a session's latest turn went: name the session with `?session=<id>`
or an `X-Magpie-Session: <id>` header. Send the same `X-Magpie-Session`
header on your own model requests to give them a session; an agent's own
session header (Claude Code's, Codex's…) counts too. It answers before the
reply's first token. `after=<seq>&wait=<seconds>` waits, up to 60 seconds,
for the route to change past that `seq`.

- `session`: the session asked.
- `seq`: the route's place; pass it as `after` to wait for the next.
- `route`: `null` before the session has one, else:
  - `asked`, `asked_effort`: the model and effort as asked.
  - `group`: the routing group's id, when one was asked.
  - `model`, `effort`: the member being tried, or that answered, as
    `provider/model`, and the effort it was sent at.
  - `served`: the model the reply says answered.
  - `tries`: each member tried, in order; every one before the last failed
    and was fallen back from.
    - `model`, `effort`: that member.
    - `status`, `fail`: its HTTP status, and why it failed.
    - `done`: it has finished.
  - `done`, `status`, `error`: the turn has finished, its status, and why
    it failed.

A key held to a budget, models or accounts is told only the routes of its
own requests.

## Choosing where to send work

Read the quotas, leave out what is used up (`remaining` 0 in a window whose
`resetsAt` hasn't come) or has an `error`, and pick a model in `/v1/models`
whose id starts with the `provider` of one with room left, or a routing
group, which falls back by itself. To wait for allowance to come back, run
`magpie quota wait <provider>` (the magpie-quota skill).

## Don't

- Don't call `/v1/magpie/quotas` without `?kept=1` in a loop: it asks the
  vendors.
- Don't send requests just to test a model or an account; they are the
  user's allowance.
