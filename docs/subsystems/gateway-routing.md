# Gateway routing and fallback

The gateway is magpie's local LLM endpoint (`127.0.0.1:3425` by default). It
serves Chat Completions, Responses, Anthropic Messages and Gemini. It finds
the provider, key or account and model for each request. When the upstream
doesn't speak the agent's API, the gateway translates. When an upstream
fails before any of the reply is sent, the gateway moves to the next
candidate.

This page covers how a request is routed. Plugins that change requests and
replies are in [Gateway middleware](gateway-middleware.md). Subscriptions
served by a plugin are in [Provider and plugin ownership](provider-plugins.md).

## Responsibilities and sources of truth

| Part | Responsibility | Source |
| --- | --- | --- |
| Address and caller | `DefaultAddr`. The local token `magpie` and the per-agent `magpie-<agent>` (`TokenFor`). `agentOf` tells which agent asked, first by token, then by User-Agent. | [`gateway.go`](../../internal/gateway/gateway.go) |
| HTTP surface | `Handler` routes `/v1/chat/completions`, `/v1/responses`, `/v1/messages` (each also without `/v1`), `/v1/models`, Gemini's `/v1beta/models/…`, the Codex backend path, `/mcp/{name}` and the `/v1/magpie/*` status endpoints. Every route is wrapped in `counted`, `callerGuard`, `withCaller` and `keyLimited`. | [`gateway.go`](../../internal/gateway/gateway.go) |
| Model resolution | `serve` resolves the model the agent asked for: Claude tier stand-ins, a routing group (`provider.GroupFor`), auto stand-ins, then `provider.Resolve`. An unknown model gets a 404 naming why: a switched-off provider, a disabled group or an empty group. | [`gateway.go`](../../internal/gateway/gateway.go), [`internal/provider/group.go`](../../internal/provider/group.go) |
| Candidates | `plan` lists one provider's candidates: one per key or account on, then each `Fallback` model, with resting ones last (`restLast`). `planGroup` and `planLevel` weigh a group's members together. A nested group plans by its own routing (#576). A member's own fallbacks are not the group's. | [`fallback.go`](../../internal/gateway/fallback.go) |
| Order of keys and accounts | `Routing`: smart (""), `order`, `rotate`, `usage` (least used, with an `usageHalfLife` of 1h), `pace` or `weight` (`weightedFirst`). A failure rests a candidate (`failure`, `failureOf`). | [`routing.go`](../../internal/gateway/routing.go), [`weighted.go`](../../internal/gateway/weighted.go), [`internal/provider/routing.go`](../../internal/provider/routing.go) |
| Sink | With `Sink` on, a key or account that answers 429 while it still has quota goes to the back, behind every one not rate limited since. Held in memory until restart. | [`sink.go`](../../internal/gateway/sink.go) |
| Affinity | A conversation stays with the key or account that answered it, so the vendor's prompt cache is read again. Modes: auto (""), `session`, `turn`, `off`. Stored in `affinity.json` next to `providers.json`. | [`affinity.go`](../../internal/gateway/affinity.go) |
| One try | `attempt` sends one candidate the request. It goes through the provider's proxy (`p.Via`). A subscription with its own client is served specially, such as Claude through the genuine Claude Code. The request is relayed as is (`passthrough`) when the model is usable on the agent's API. Otherwise it is translated (`translate`). | [`gateway.go`](../../internal/gateway/gateway.go) |
| Trace | The Routing view's record of each decision, written where the decision is made: candidates, rests, which try answered. | [`trace.go`](../../internal/gateway/trace.go) |

## Runtime path

1. `handle(proto)` reads the body and the model (`requestBody`, `requestModel`), then calls `serveAgent`. `serveAgent` runs the [middleware](gateway-middleware.md) chain, then `serve`.
2. `serve` redacts secrets in the request (`redacted`), resolves the model to a provider or a group, and refuses what can't be served:
   - `DecidesModel` gives a 400.
   - A gateway key held to some models (`keyHolds`, #882) gets a 403 for any other model.
   - Sealed tasks (#619) are checked here.
   - Rules and image inputs are handled here too.
3. Candidates come from `planGroup` for a group or `plan` for a provider. They are filtered by `sealedReaders` and `allowedCandidates`. An account past its share is held out of the candidates (`capHeld`): a usage cap's share, or 100% of a window for a Codex account set not to spend its credits (`provider.HoldShare`, `CodexNoCredits`). When every candidate is held and one held for its credits spends its resets by itself, `autoReset` spends a reset on it and it is tried (`planned.held`); otherwise a capped or barred set returns `cappedError` or `barredError`. Then `affine` moves the conversation's last answerer first, unless it is resting or nearly used up. An `X-Magpie-Account` header (`AccountHeader`) pins the request to one account: only that account is tried, and when it can't answer the caller is told why.
4. The candidate loop calls `attempt` on each candidate in turn, through a `holdWriter`. The writer holds the reply until it is clearly passing. A candidate that fails before anything is sent rests (`failure`): out of credit for 30 minutes, out of quota until reset, rate limited until retry-after, otherwise a minute and longer on repeats. The next candidate is then tried. A failed provider waits `fallbackCooldown` (1 minute) at the back.
5. The last candidate's error reaches the agent. A try that answered is recorded (`served`, `answered`) for routing, affinity and usage.

## Constraints and failure behavior

- A fallback happens only while none of the reply has been sent. An agent never gets half a reply from one upstream and the rest from another.
- A streamed request is never left silent while its vendor says nothing. `holdWriter.watch` runs beside each try: past `keepHeldAfter` (15s) it sends a held try's agent the stream's 200 and SSE comments (`keepAlive`), none of the try, so another candidate may still answer in the same stream; once the reply passes, it puts a comment between two of its lines after `keepaliveEvery` of quiet. It stops after `keepaliveLongest` (5 minutes) with nothing from the vendor. A proxy in front such as Cloudflare (125s) otherwise ends the request (#947). Gemini streams get no comments (#934).
- A resting candidate is tried last, never dropped (`restLast`). A lone candidate is tried even while it rests, because there is no other.
- Routing decides who goes first only when nobody has answered the conversation yet. Affinity keeps the answerer while its cache is worth it. Auto mode keeps it across turns while at least `cacheWorth` (1024) tokens were read from the cache and the cache isn't cold (`cacheCold`, 5 minutes).
- In turn (`rotate`), a conversation nobody has answered yet goes to the key, account or model the fewest other conversations of the provider's model or the group answered in the last `heldFor` (30 minutes) are on (`leastHeld`, #946). Ties, resting ones and a group with a group in it keep routing's order. With session affinity, two sessions of one group so stay on different models.
- A ChatGPT account that holds credits answers past its windows on them; the vendor spends them. That is the default. Set not to spend them (Usage card's *Use credits*, `magpie quota credits <account> off`), the account is held at 100% of a window till it renews, so the other accounts, groups and fallbacks take the request. A cap set on the account (1–99%) takes precedence. Unlike a cap, the hold is at the vendor's own 100%, so an account allowed to spend its resets spends one there, both when every candidate is held and after the last one is refused. A reset made forgets the account's cached allowance (`renewedNow`), so neither hold outlives the windows it started again.
- Different failures are classified separately: out of credit, out of quota, rate limit and other errors rest for different lengths. A fix must not merge them into one bucket (see #873).
- Codex offers its image tool (the `image_gen` namespace, #870) to every magpie provider. When a provider other than a ChatGPT sign-in or openai.com answers a bare 400, `attempt` asks it once more without the tool (`image_tool.go`). This happens both passed through and translated. If that works, the provider is marked unfit for the tool (`tool:image_gen`) and isn't offered it again. If it doesn't, the tool is restored and the 400 stands (#949).
- Group members are `provider/model[:effort]`. A member with a fixed effort is asked for that effort, bounded by the group's levels (#671).

## Verification

```sh
go test -tags nogui ./internal/gateway -run 'TestQuiet|TestSilentHeld|TestRotateSpreadsSessions|TestRouting|TestSmartRouting|TestFallback|TestNoFallbackForOtherErrors|TestLastFallbackErrorReachesTheAgent|TestRateLimitedSinksToTheBack|TestGroupRateLimitedSinks|TestAffinity|TestSeveralKeysOnTakeOverFromEachOther|TestSubscriptionAccountsTakeOver|TestGroup|TestTrace|TestImageTool|TestCodexNoCredits'
go test -tags nogui ./internal/provider -run 'TestGroup|TestCodexCreditsSwitch|TestRenewedAccountForgetsItsAllowance'
```

The gateway's `TestMain` gives the package a home of its own, so these tests
never read real providers or accounts.
