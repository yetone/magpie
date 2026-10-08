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
| Provider | `Provider` holds the endpoint bases for each `Protocol` (Chat, Responses, Anthropic, Gemini), the keys, `Fallback`, `Routing`, `Sink`, `Affinity`, concurrency and queue limits, `Headers`, `Proxy`, models, and balance settings. `BaseAPI` is the API a custom provider's editor shows its Base URL as: the one the user picked and saved, while it has a URL, so a provider with a chat URL beside its Responses one isn't shown as OpenAI compatible after Responses was picked. Requests go by which URLs are set. `Account` is set only for a subscription and is never saved (`json:"-"`). | [`provider.go`](../../internal/provider/provider.go) |
| providers.json | `Path` is `providers.json` in magpie's config folder. A read that fails returns `ErrUnreadable` and is never treated as empty. `store` writes atomically through a rename and drops the cached catalog (`catalog.Touched`). | [`provider.go`](../../internal/provider/provider.go) |
| The list | `All` merges stored providers with `Accounts()`. An entry with no URL stores the user's picks for an account. The order is `file.Order`. Reads within one request share one build (`Hold`, #746). | [`provider.go`](../../internal/provider/provider.go) |
| Keys | Several keys per provider. Requests go to the first key that is on; the gateway moves on when it is out or rate limited. `Key` stays the first, so other code reads it as before. A key can be made for one protocol (`KeyProtocol`). | [`keys.go`](../../internal/provider/keys.go) |
| Subscription accounts | `Accounts` reads the credentials each agent keeps on disk or in the macOS Keychain; nothing is stored twice. A built-in that has moved to its plugin is dropped (`Moved`), and plugin accounts are placed in its slot (`placeMoved`). `Sign` adds the account's own auth, then the user's `Headers`. | [`account.go`](../../internal/provider/account.go) |
| Token refresh | Each account refreshes through its vendor. For example, `codexToken` refreshes within 5 minutes of expiry and writes the rotated tokens back into `~/.codex/auth.json` for Codex CLI. A 400 or 401 from the vendor's refresh endpoint means it was refused (`refreshFailed`). | [`account.go`](../../internal/provider/account.go) |
| Saved accounts | `logins.json` remembers every Codex and Claude Code account seen signed in. A switch moves the credentials into the agent's own store and saves the ones it replaces. A saved account that is on (`SetLoginOn`) stands behind the agent's own. Its tokens stay in `logins.json`. | [`logins.go`](../../internal/provider/logins.go), [`logins_on.go`](../../internal/provider/logins_on.go) |
| Sign-in in magpie | `StartSignIn` runs the vendor's own browser OAuth with its client and takes the tokens at a callback on this machine. A browser that can't reach that callback (magpie on a server or in Docker) ends on a page that won't load; its address, pasted (`SubmitSignInCallback`), goes through the same callback handler (`pastedCallback`). A plugin's browser sign-in (`auto`) whose page names a port on this machine to come back to takes its pasted address too: magpie opens it there on 127.0.0.1, for the plugin's own listener (`pluginCallback`, `loopbackPorts`); Command Code's page posts its key from script, so it offers the API key way instead. A Claude account is signed in by Claude Code itself. Signing in brings back an account removed from magpie (`ShowAccount`). A sign-in started in magpie (`StartSignIn`, `StartPluginSignIn`) reads as done (`SignInStatus`, `WaitSignIn`) only after `finish` has run `ShowAccount` for its account, so the window, which opens the account as soon as it sees done, finds it listed. If providers.json can't be written then, the account stays hidden and the sign-in still ends done. Signing in to a plugin's provider from the command line (`pluginLogin`, with a key or the browser) brings it back too. It also takes off the mark a sign-in its vendor turned away left on the account (`Lapsed`) and shows again the agent's own account removed in magpie (`clearPluginLapse`). The window's plugin sign-in does the same with the browser (`pluginDone`, then `finish`) or with a key (`PluginAPIKey`); a key and the command line share these steps in `PluginSignedIn`. | [`signin.go`](../../internal/provider/signin.go), [`claude_signin.go`](../../internal/provider/claude_signin.go), [`pluginsignin.go`](../../internal/provider/pluginsignin.go), [`pluginsignin_paste.go`](../../internal/provider/pluginsignin_paste.go), [`plugins_cli.go`](../../plugins_cli.go) |
| Used-up switch | When the account Codex or Claude Code is signed in to has used its allowance, the agent is signed in to the next account that is on and has room (#208). | [`codex_switch.go`](../../internal/provider/codex_switch.go) |
| Routing groups | User-made groups from the Routing view, and auto-found groups of the same model across providers (derived, never stored). Members are `provider/model[:effort]`. A group can nest another (`group/<id>`); `SaveGroup` refuses loops. `Match` takes a glob or `re:` pattern (#766). | [`group.go`](../../internal/provider/group.go) |
| Allowance | `Quotas` asks every vendor at once: subscription windows (`SubscriptionUsage`), key-bought plans (`PlanQuotas`) and key balances (`KeyBalances`). The Usage page, `magpie quota --json` and `GET /v1/magpie/quotas` read it. A single card can be read again on its own (#840). A remote magpie's cards come from what it has kept (`CachedCards`, `GET /v1/magpie/quotas/cards`) and are never read there for this computer except by a card's refresh (`POST /v1/magpie/quotas/refresh`, throttled per card). | [`quotas.go`](../../internal/provider/quotas.go), [`subscription_usage.go`](../../internal/provider/subscription_usage.go), [`planquota.go`](../../internal/provider/planquota.go), [`balance.go`](../../internal/provider/balance.go), [`quota_refresh.go`](../../internal/provider/quota_refresh.go), [`remote_quotas.go`](../../internal/provider/remote_quotas.go) |
| A key's own windows | A sub2api key given a spending limit per 5 hours, day or 7 days answers its `/v1/usage` with `rate_limits` and no `remaining` (`readSub2APIKeyLimits`). With the Balance URL set to it, the key's card shows them as the windows *5 hours*, *1 day*, *7 days*, in USD, beside a balance field the user wrote; *Check balance* and `magpie provider show` give what its fullest window has left (`Balance`). `KeyAllowance` gives routing the same windows: never waited for, read in the background at most once a minute, a failed read keeping the last, and seeded from the card's last reading (`quotas.json`) after a restart.  A key bought as a plan (Kimi Code, GLM Coding Plan and Z.ai, MiniMax Coding Plan, OpenCode Go, Command Code: `planQuotaSourceOf`) gives routing its card's windows the same way (`planKeyWindows`, Zhipu's team quota behind a key with none of its own); the Usage page's read of its card is taken at once (`noteKeyAllowance`), and a key there that turns out pay-as-you-go, with no windows, is asked again only every 30 minutes (`keyNoWindowsAge`). StepFun's Step Plan is read through a sign-in, not the key, and isn't one of them. | [`sub2api_usage.go`](../../internal/provider/sub2api_usage.go), [`key_allowance.go`](../../internal/provider/key_allowance.go) |
| Last served | `served.json` records when each account, plan or key last answered, shown as `lastServedAt` (#570). | [`served.go`](../../internal/provider/served.go) |
| Protocol detection | Works out which APIs a base URL speaks when a provider is added. | [`detect.go`](../../internal/provider/detect.go) |

## Runtime path

1. **Add.** The Providers page adds a key provider through `Add` or `Save`, or an account through `StartSignIn`. An agent signed in on its own is found by `Accounts` with no step from the user.
2. **Resolve.** The gateway calls `provider.Resolve` or `GroupFor` with the model name, then plans candidates over the provider's keys or accounts.
3. **Sign.** For each try, `Sign` gives the request its auth. A token about to expire is refreshed first, and the rotated tokens go back to their one holder: the agent's store, or `logins.json` for an account standing behind it.
4. **Allowance.** Usage endpoints are rate limited, so `SubscriptionUsage` caches its results: a cached copy comes back at once and a stale one is refreshed in the background. When an account's windows start again, `OnRenewed` tells the gateway so a resting account can come back: a Codex reset spent (`renewedNow`), or a reading routing takes (`Allowances`, as the agent the account's usage is read under, `claude` or `plugin:<id>`) that finds the account full till sooner than the last did (`renewedFrom`, the rule a key's reading goes by): the whole account, or a pool of some models with the whole account's windows over it. A window used below `SpentShareOf` its routing, or whose reset passed, is full no more; one full with its reset not known is full for good. A Claude account is back as soon as a new `/usage` or the reset passing shows the window it was out of started again, not when the time Claude Code's refusal named comes; its five hours started again in a week used up tells nothing. A first reading, a failed one, one read alike, or one leaving the full window out tells nothing, and a reset spent is told once, by `renewedNow`. A key's reading that finds a window it was full in (at `SpentShareOf` its routing) full no more, or full till sooner, tells it too, as agent `""` and the key's `KeyAllowanceID`, so a key out of its limit is back once the limit is raised or its usage reset; a first reading, a failed one, or one that finds it fuller tells nothing. An account refused for its quota has its agent's allowances read again at the next ask (`StaleAllowance`). A reading already out then was asked before the refusal: what it read is kept, as not read, so the ask after it lands reads again, as `StaleKeyAllowance` does for a key. Built-in Grok's own per-account cache (`grokLoginUsage`) doesn't keep such a reading at all: it goes to its caller only, so that next ask reaches xAI.

## Allowance history and forecasts

[`quota_history.go`](../../internal/provider/quota_history.go) records each
window's observed percent left, observation time, cycle start and reset.
`QuotaHistories` serves those lines through `GET /api/usage/quotas/history`
and `GET /v1/magpie/quotas/history`. Each line may also carry a derived
`forecast`; changing the requested `days` trims the displayed points, not
the full history used to forecast. Forecasts are never persisted or synced.

[`quota_forecast.go`](../../internal/provider/quota_forecast.go) compares
current consumption with an even burn. Windows of at least two days may
also use at least three completed cycles with sufficient readings and
coverage. The more conservative result wins: either layer can warn that the
allowance runs out. Unknown cycles, insufficient readings and expired resets
have no forecast; `state: "none"` means the cycle is too young or too little
is consumed, `"spent"` means an observed depleted allowance, and `"ok"`
means a projection is available. Below 8% of a cycle, a clear early run-out
still gets a projection once 30 minutes have elapsed, at least 10% has been
used and the even-burn ETA is at most half the time remaining to reset.
Smaller or younger early burns wait for more of the cycle. The backend
returns numbers and enums; labels belong to the UI's translations.

A forecast's `asOf` is its last observation, which fixes its rate, ahead
percentage, verdict, projected `leftAtReset` and `headroom`. Unobserved time
never counts as zero consumption. `runsOutAt`, when present, is that
projection's fixed crossing; `etaSeconds` counts down from the query's clock
and floors at zero after the crossing. An expired projection does not claim
an observed `"spent"` state. A reset that has passed ends the forecast,
and readings from a new cycle cannot reuse the previous cycle's verdict.

The Usage page and tray share the same verdict in
[`app.js`](../../internal/gui/assets/app.js). An absent forecast or `"none"`
state shows no verdict; actual readings and their times remain available.
Each full account window has its own burn-down, even-burn line and projected
crossing or reset remainder.
The actual dot is at the reading's time, with a reading tooltip; the vertical
line marks now. The shared legend explains the marks, and the range menu
changes charts without changing the verdict. Brief accounts retain meters
only. Clicking a plot or pressing Enter enlarges it without scrolling; the
tray keeps a compact verdict and sparkline. Model/family switches redraw
and fit the new windows after insertion, even when the card wall's size
stays the same. Resizing redraws from the stable
card container on the next animation frame, avoiding WebKit observer loops.

## Constraints and failure behavior

- Each refresh token has exactly one holder. Vendors rotate tokens on refresh, so two copies of one token would sign each other out. `savedTokenMu` stops two requests refreshing one saved account at once.
- A Claude subscription account is used only for Claude Code, through the genuine Claude Code binary (see [Claude subscription bridge](claude-subscription-bridge.md)).
- Claude Code's `/usage` prints a reset with no year when it falls this year ("Oct 9, 2:59pm (UTC)"), or, in older versions, as a time alone ("3pm"). `claudeResetTime` reads it as the end of its window, so never further ahead than the window's length (5 hours for the session, 7 days for a week) and `claudeResetSlack`: a date is in the first of last year, this year and next that is less than a day past and no further ahead, a time alone the next one no further ahead. Where none is, as for a reading days old, it is the latest one already past, so the window reads as renewed rather than full for a year. Where the clocks go back and show a time twice, the reading not yet past is taken. A week's time alone read just after its reset still reads as tomorrow's.
- A Copilot account is known by its GitHub login and host (`CopilotAccountName`, #1220): `mona` on github.com, `mona@acme.ghe.com` on an enterprise's GHE.com. That name is its `logins.json` user, its row in every list, its usage card and its per-account routing settings, and it is what `SwitchLogin`, `SetLoginOn` and `ForgetLogin` take, so one login on two hosts is two accounts, neither written over by the other nor dropped as the editors' own. One saved before on an enterprise's host under its login alone is read under its name with the host (`copilotSavedName`), keeping its id; the editors' own remembered so is renamed in place, still hidden if it was (`copilotRenamed`). Either rename takes the account's per-account settings in `providers.json` (proxy, models, cap, concurrency) from the bare login to the new name before the new name is written to `logins.json` (`renameSettled`): a setting the new name has already stays, and while `providers.json` can't be read or written the account stays saved under its old name, its settings with it. `magpie accounts switch|forget copilot` takes the full name, the login with `--host <name>.ghe.com`, or the login alone when it names one account.
- The Copilot account the editors and CLI keep is read by `copilotLogin` ([account.go](../../internal/provider/account.go)): an editor's github.com sign-in in `~/.config/github-copilot/apps.json` or `hosts.json` first, then the Copilot CLI's, then an editor's on GHE.com. When GitHub refuses the editor token's trade for a session token (401 or 403, `copilotRefusals`, retried after an hour), the CLI's sign-in of the same login on the same host takes its place, sent as the CLI sends it, from that request on and on the Usage card (`copilotStandIn`); a CLI signed in to another account is never used for it, and the error names the editors' file (#1238). A sign-in from magpie of the account whose editor token is refused is kept and stands for it (`copilotProbeEditor`, `addCopilotLogin`); while the editor token works, it is let go as the agent's own, as before.
- An unreadable `providers.json` is an error, never an empty list. Saving over an empty list would lose every provider.
- When `logins.json` cannot be decoded as saved accounts, reads keep the accounts read before. Before saving a change, `keepUnreadLogins` copies the original file aside, including valid JSON with invalid account field types, so its credentials can be recovered. If the existing file cannot be read or the backup fails, saving returns the error and leaves the original file unchanged. A missing file can be created normally.
- A switched-off provider (`Off`, #163) stays saved. The gateway says it is off instead of saying the model is unknown.
- A provider removed or switched off takes its models out of the catalog. [Agent wiring](agent-wiring.md)'s `Reseat` moves agents off them.
- A preset marked `NoList` uses its preset model list instead of requesting `/models`, and ignores previously fetched lists, including their protocol and key restrictions, unless the user sets `ModelsURL` or chooses a region marked `Lists`. Volcengine Ark's Coding Plan and Agent Plan use this path: Coding's `/api/coding/v3/models` returns a general Ark catalog instead of the plan's model ids, while Agent's `/api/plan/v3/models` returns 404. Ark's pay-as-you-go region still fetches its own list. [`presets.go`](../../internal/provider/presets.go) defines these choices; `List`, `fetch` and `live` in [`models.go`](../../internal/provider/models.go) apply them when adding, refreshing and reading cached models. `TestVolcengineModelLists` checks that the plans make no list requests, ignore old cached lists and return their preset models; pay-as-you-go and an explicit list URL still make requests.
- A gateway key held to some accounts (#905) is held to those of the providers its list names: a provider it names no account of, the key uses as it always did. The list keeps an account by its stable id — `logins.json`'s `id`, set once written and kept through renames; the agents that keep no logins (and plugins) are known by a hash of the account's name, so an account renamed there drops out of a key's list, holding it closer, never wider. Storing an id for every agent is a larger change, not this one's.
- A sub2api key's windows are fixed: each starts at the key's first request after it was empty, aligned to the relay's day, and a window not started yet says `used 0` with no `reset_at`, so it is taken to renew a span from now. They count this key's spend only, not the relay's accounts behind it. `ForgetBalances` (a key or Balance URL changed) has every key's windows read again at its next ask, a read begun before dropped; until then the last reading stands, fresher than the card's, which routing's reads never write. A key changed is another key (`keyID`), with windows of its own.
- Whether a Codex account spends its credits (`CodexNoCredits` in settings, opt-out, by lower-cased email) is the gateway's routing only. It never changes which account Codex is signed in to: `NextLogin` moves off an account at its routing's spent share, 100% at most, credits or not.
- A window whose reset time has passed has started again, whatever share it was read at (`resetPassed`). That holds for a reading kept from before (`AsOf`) and for a fresh one: Claude Code's `/usage` gives a reset to the minute and can lag it, so a run just after a reset can still say 100% with that reset. `claudeWindows` hands such a window back empty on every path, and `usedPast`, `usedUp`, `BackAt` and `capReached` leave it out when they judge an account spent, for Codex, Claude Code and `magpie quota wait` alike. A card can still show a kept reading as it was read (`reported`, for a Claude card while `/usage` is unavailable); that changes what it shows, never which account is used. A reset `/usage` gives as a time alone ("resets 3pm") can be read as its next one, a day on (`claudeResetTime`), and is then not seen as passed here.

## Verification

```sh
go test -tags nogui ./internal/provider -run 'TestAccount|TestCodex|TestGroup|TestQuota|TestLogin|TestSignIn|TestSeveralKeys|TestKeyProtocol|TestSetKeyWeight|TestReadSub2APIKeyLimits|TestSub2APIKeyLimitsOnItsCard|TestKeyAllowance|TestPlanKeyAllowance|TestPayAsYouGoKeyAskedSeldom|TestClaudeAccountToldRenewed|TestPluginAccountToldRenewed'
go test -tags nogui ./internal/provider -run 'TestVolcengine|TestQianfan|TestBedrock'
go test -tags nogui ./internal/provider -count=1
```

The forecast regressions include `TestQuotaHistoriesForecastIgnoresDisplayDays`,
`TestQuotaForecastOldReading` and `TestQuotaForecastStaysOutOfSync`. Browser
checks use Node and Playwright:

```sh
node --test internal/gui/tests/quota-curve.test.cjs internal/gui/tests/quota-forecast.test.cjs internal/gui/tests/balance-curve.test.cjs
```

The package's tests use a home of their own (`testenv`). Never point them at
real `~/.codex`, `~/.claude` or `~/.config/magpie` files. Check a change
against real accounts in a sandbox HOME, as [Code standards](../code-standards.md#acceptance-criteria) describes.
