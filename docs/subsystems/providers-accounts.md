# Providers and accounts

The gateway can send a request to three kinds of upstream:

- A **provider** in `providers.json`: an endpoint with one or more API keys.
- A **subscription account**: an agent's own sign-in (Codex, Claude Code, Copilot and others) that magpie reads from the agent's store, or an account signed in from magpie.
- A **plugin's account**: a subscription served by an OpenCode plugin.

Package `internal/provider` holds all three, the routing groups built over
them, and what is left of each one's allowance.

Plugin ownership of the deprecated built-in subscriptions is in
[Provider and plugin ownership](provider-plugins.md). How the gateway orders
keys and accounts per request is in [Gateway routing and fallback](gateway-routing.md).

## Responsibilities and sources of truth

| Part | Responsibility | Source |
| --- | --- | --- |
| Provider | `Provider` holds the endpoint bases for each `Protocol` (Chat, Responses, Anthropic, Gemini), the keys, `Fallback`, `Routing`, `Sink`, `Affinity`, concurrency and queue limits, `Headers`, `Proxy`, models, and balance settings. `Account` is set only for a subscription and is never saved (`json:"-"`). | [`provider.go`](../../internal/provider/provider.go) |
| providers.json | `Path` is `providers.json` in magpie's config folder. A read that fails returns `ErrUnreadable` and is never treated as empty. `store` writes atomically through a rename and drops the cached catalog (`catalog.Touched`). | [`provider.go`](../../internal/provider/provider.go) |
| The list | `All` merges stored providers with `Accounts()`. An entry with no URL stores the user's picks for an account. The order is `file.Order`. Reads within one request share one build (`Hold`, #746). | [`provider.go`](../../internal/provider/provider.go) |
| Keys | Several keys per provider. Requests go to the first key that is on; the gateway moves on when it is out or rate limited. `Key` stays the first, so other code reads it as before. A key can be made for one protocol (`KeyProtocol`). | [`keys.go`](../../internal/provider/keys.go) |
| Subscription accounts | `Accounts` reads the credentials each agent keeps on disk or in the macOS Keychain; nothing is stored twice. A built-in that has moved to its plugin is dropped (`Moved`), and plugin accounts are placed in its slot (`placeMoved`). `Sign` adds the account's own auth, then the user's `Headers`. | [`account.go`](../../internal/provider/account.go) |
| Token refresh | Each account refreshes through its vendor. For example, `codexToken` refreshes within 5 minutes of expiry and writes the rotated tokens back into `~/.codex/auth.json` for Codex CLI. A 400 or 401 from the vendor's refresh endpoint means it was refused (`refreshFailed`). | [`account.go`](../../internal/provider/account.go) |
| Saved accounts | `logins.json` remembers every Codex and Claude Code account seen signed in. A switch moves the credentials into the agent's own store and saves the ones it replaces. A saved account that is on (`SetLoginOn`) stands behind the agent's own. Its tokens stay in `logins.json`. | [`logins.go`](../../internal/provider/logins.go), [`logins_on.go`](../../internal/provider/logins_on.go) |
| Sign-in in magpie | `StartSignIn` runs the vendor's own browser OAuth with its client and takes the tokens at a callback on this machine. A Claude account is signed in by Claude Code itself. | [`signin.go`](../../internal/provider/signin.go), [`claude_signin.go`](../../internal/provider/claude_signin.go) |
| Used-up switch | When the account Codex or Claude Code is signed in to has used its allowance, the agent is signed in to the next account that is on and has room (#208). | [`codex_switch.go`](../../internal/provider/codex_switch.go) |
| Routing groups | User-made groups from the Routing view, and auto-found groups of the same model across providers (derived, never stored). Members are `provider/model[:effort]`. A group can nest another (`group/<id>`); `SaveGroup` refuses loops. `Match` takes a glob or `re:` pattern (#766). | [`group.go`](../../internal/provider/group.go) |
| Allowance | `Quotas` asks every vendor at once: subscription windows (`SubscriptionUsage`), key-bought plans (`PlanQuotas`) and key balances (`KeyBalances`). The Usage page, `magpie quota --json` and `GET /v1/magpie/quotas` read it. A single card can be read again on its own (#840). A remote magpie's cards come from what it has kept (`CachedCards`, `GET /v1/magpie/quotas/cards`) and are never read there for this computer except by a card's refresh (`POST /v1/magpie/quotas/refresh`, throttled per card). | [`quotas.go`](../../internal/provider/quotas.go), [`subscription_usage.go`](../../internal/provider/subscription_usage.go), [`planquota.go`](../../internal/provider/planquota.go), [`balance.go`](../../internal/provider/balance.go), [`quota_refresh.go`](../../internal/provider/quota_refresh.go), [`remote_quotas.go`](../../internal/provider/remote_quotas.go) |
| A key's own windows | A sub2api key given a spending limit per 5 hours, day or 7 days answers its `/v1/usage` with `rate_limits` and no `remaining` (`readSub2APIKeyLimits`). With the Balance URL set to it, the key's card shows them as the windows *5 hours*, *1 day*, *7 days*, in USD, beside a balance field the user wrote; *Check balance* and `magpie provider show` give what its fullest window has left (`Balance`). A GLM Coding Plan's key (Zhipu's or Z.ai's coding endpoint) has its windows from the plan's monitor endpoint, where its Usage card asks them (`glmCodingPlanKey`). `KeyAllowance` gives routing the same windows, either kind: never waited for, read in the background at most once a minute, a failed read keeping the last, and seeded from the card's last reading (`quotas.json`) after a restart — the balance's for a sub2api key, the plan's for a GLM one. | [`sub2api_usage.go`](../../internal/provider/sub2api_usage.go), [`planquota.go`](../../internal/provider/planquota.go), [`key_allowance.go`](../../internal/provider/key_allowance.go) |
| Last served | `served.json` records when each account, plan or key last answered, shown as `lastServedAt` (#570). | [`served.go`](../../internal/provider/served.go) |
| Protocol detection | Works out which APIs a base URL speaks when a provider is added. | [`detect.go`](../../internal/provider/detect.go) |

## Runtime path

1. **Add.** The Providers page adds a key provider through `Add` or `Save`, or an account through `StartSignIn`. An agent signed in on its own is found by `Accounts` with no step from the user.
2. **Resolve.** The gateway calls `provider.Resolve` or `GroupFor` with the model name, then plans candidates over the provider's keys or accounts.
3. **Sign.** For each try, `Sign` gives the request its auth. A token about to expire is refreshed first, and the rotated tokens go back to their one holder: the agent's store, or `logins.json` for an account standing behind it.
4. **Allowance.** Usage endpoints are rate limited, so `SubscriptionUsage` caches its results: a cached copy comes back at once and a stale one is refreshed in the background. When an account's windows start again, `OnRenewed` tells the gateway so a resting account can come back. A key's reading that finds a window it was full in (at `SpentShareOf` its routing) full no more, or full till sooner, tells it too, as agent `""` and the key's `KeyAllowanceID`, so a key out of its limit is back once the limit is raised or its usage reset; a first reading, a failed one, or one that finds it fuller tells nothing.

## Constraints and failure behavior

- Each refresh token has exactly one holder. Vendors rotate tokens on refresh, so two copies of one token would sign each other out. `savedTokenMu` stops two requests refreshing one saved account at once.
- A Claude subscription account is used only for Claude Code, through the genuine Claude Code binary (see [Claude subscription bridge](claude-subscription-bridge.md)).
- An unreadable `providers.json` is an error, never an empty list. Saving over an empty list would lose every provider.
- When `logins.json` cannot be decoded as saved accounts, reads keep the accounts read before. Before saving a change, `keepUnreadLogins` copies the original file aside, including valid JSON with invalid account field types, so its credentials can be recovered.
- A switched-off provider (`Off`, #163) stays saved. The gateway says it is off instead of saying the model is unknown.
- A provider removed or switched off takes its models out of the catalog. [Agent wiring](agent-wiring.md)'s `Reseat` moves agents off them.
- A gateway key held to some accounts (#905) is held to those of the providers its list names: a provider it names no account of, the key uses as it always did. The list keeps an account by its stable id — `logins.json`'s `id`, set once written and kept through renames; the agents that keep no logins (and plugins) are known by a hash of the account's name, so an account renamed there drops out of a key's list, holding it closer, never wider. Storing an id for every agent is a larger change, not this one's.
- A sub2api key's windows are fixed: each starts at the key's first request after it was empty, aligned to the relay's day, and a window not started yet says `used 0` with no `reset_at`, so it is taken to renew a span from now. They count this key's spend only, not the relay's accounts behind it. `ForgetBalances` (a key or Balance URL changed) has every key's windows read again at its next ask, a read begun before dropped; until then the last reading stands, fresher than the card's, which routing's reads never write. A key changed is another key (`keyID`), with windows of its own.
- A GLM Coding Plan's windows are the plan's, as the monitor endpoint tells the key: the 5-hour and weekly TOKENS_LIMIT windows count its models, the month's TIME_LIMIT (MCP tool calls) is set aside, and a window's `nextResetTime` is what routing rests to. A team's key — whose windows come from the team ask (`type=2`, `ZhipuTeam`) — and a pay-as-you-go GLM key (no plan of its own) are asked nothing by routing, which knows nothing of them; their card is unchanged.
- Whether a Codex account spends its credits (`CodexNoCredits` in settings, opt-out, by lower-cased email) is the gateway's routing only. It never changes which account Codex is signed in to: `NextLogin` moves off an account at its routing's spent share, 100% at most, credits or not.

## Verification

```sh
go test -tags nogui ./internal/provider -run 'TestAccount|TestCodex|TestGroup|TestQuota|TestLogin|TestSignIn|TestSeveralKeys|TestKeyProtocol|TestSetKeyWeight|TestReadSub2APIKeyLimits|TestSub2APIKeyLimitsOnItsCard|TestKeyAllowance|TestGLMCodingPlanKeyAllowance'
go test -tags nogui ./internal/provider
```

The package's tests use a home of their own (`testenv`). Never point them at
real `~/.codex`, `~/.claude` or `~/.config/magpie` files. Check a change
against real accounts in a sandbox HOME, as [Code standards](../code-standards.md#acceptance-criteria) describes.
