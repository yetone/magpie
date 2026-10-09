# Provider and plugin ownership

Some built-in subscriptions are deprecated and have community plugins that provide the same service. A subscription can still use its built-in implementation or move to its plugin. That ownership determines where sign-in, refresh, model discovery, requests, usage, and errors are handled.

## Responsibilities and sources of truth

| Part | Responsibility | Source |
| --- | --- | --- |
| Provider migration | Match subscriptions to plugins, record ownership, transfer accounts, and maintain required plugin versions | [`migrate.go`](../../internal/provider/migrate.go) and `migrate_*.go` in the same directory |
| Provider integration | Expose plugin-backed provider cards, usage, sign-in, and the plugin's own daily check-in | [`plugins.go`](../../internal/provider/plugins.go), [`plugin_usage.go`](../../internal/provider/plugin_usage.go), [`pluginsignin.go`](../../internal/provider/pluginsignin.go), [`plugin_checkin.go`](../../internal/provider/plugin_checkin.go) |
| Plugin host | Load plugins and execute their code | [`internal/plugin`](../../internal/plugin), [`host.js`](../../internal/plugin/host.js) |
| Gateway | Dispatch requests through the implementation that owns the subscription | [`internal/gateway`](../../internal/gateway) |
| GUI | Present available subscriptions, migration state, and plugin sign-in | [`app.js`](../../internal/gui/assets/app.js) |
| Community plugin | Implement the moved subscription's upstream behavior | [magpie-community/plugins](https://github.com/magpie-community/plugins), `packages/<name>` |

The `movers` map assembled in `internal/provider/migrate*.go` is the authoritative subscription-to-plugin mapping. Each mover's `min` is the minimum plugin version required by magpie. The list of moved subscriptions in [AGENTS.md](../../AGENTS.md#built-in-subscriptions-that-a-plugin-serves) is a contributor lookup; confirm the mapping in source when changing it.

## Ownership and runtime paths

`provider.Moved(id)` identifies a subscription whose migration state in `migrations.json` is `plugin`. In `/api/providers`, its row exposes `move.state == "plugin"`; the GUI also receives its id in `onPlugins`.

| Operation | Built-in owns the subscription | Plugin owns the subscription |
| --- | --- | --- |
| Sign-in, refresh, and models | Built-in provider implementation | Community plugin through magpie's plugin integration |
| Upstream requests and translation | Built-in gateway adapter | Plugin fetch through the plugin host |
| Usage and upstream errors | Built-in implementation | Plugin, exposed through magpie's provider integration |
| Migration state and required version | Magpie | Magpie |

For a moved subscription, its built-in upstream implementation does not run. Plugin accounts use `Account.Agent == "plugin"`; requests reach `plugin.Fetch` through `Provider.Do` and bypass the built-in subscription adapter. Magpie still handles routing, protocol conversion, quota handling, and compatibility adjustments, as well as the plugin host, provider integration, migration code, and GUI plugin paths.

Quota aggregation in [`quotas.go`](../../internal/provider/quotas.go) suppresses a key's GLM Coding Plan card only when its comparable reset windows match a ZCode subscription card: built-in or moved `zcode`, or independently installed `zcode-plugin`. [`PlanQuotas`](../../internal/provider/planquota.go) identifies GLM plans by their quota endpoint, including custom provider ids. Matching reset schedules from unrelated vendors do not hide cards. Within this pair, resets remain a heuristic for the shared account; a plan's `User` is a key label or mask, not a login identity. Errors, conflicting resets, or no comparable resets keep the plan visible. This applies to the Usage page, tray, quota reports, alerts, and quota waits; it does not change plugin ownership or upstream requests.

## A plugin's daily check-in

A plugin can press its vendor's daily check-in (签到) itself: its `auth` hook gains `checkin(getAuth, provider)`, beside `usage`. The host lists the provider with `checkin: true` (`plugin.Provider.Checkin`) and answers `checkin({provider, account})` (`plugin.AccountCheckin`) by refreshing the account as `usage` does and calling the hook with the account's auth.

The hook returns `{outcome, credit?, streak?, message?}`. `outcome` is one of `claimed` (checked in now), `done` (already in today), `ineligible`, `inactive` (no check-in event now), `captcha` or `failed`. A throw, or an outcome not in that list, is `failed` with the reason. `captcha` means the vendor wants a captcha: magpie never solves one, and says "check in in its own app" instead. A plugin must not solve captchas either.

[`plugin_checkin.go`](../../internal/provider/plugin_checkin.go) runs these through the same `checkiner` as the built-in check-ins ([`checkin.go`](../../internal/provider/checkin.go)): each signed-in, in-use account once a Beijing day, the first look 2 minutes after start and then every 30 minutes, a `failed` asked again after 30 minutes, every other outcome settled for the day. Results are kept in `plugin-checkin.json` by `provider|account`; a failure is recorded as failed and never resets an account, a key or a previous day's result. Each account's Usage card has the check-in row (`checkinBy: "plugin:<provider>"`), and Settings → Usage has a Plugins tab with a Daily check-in switch per provider (`settings.PluginCheckins`, `POST /api/settings/plugin-checkin {provider, on}`; *Check in now* is `POST /api/usage/plugin-checkin {provider}`). `magpie accounts checkin` and `c` on the TUI's Usage page include them.

The switch is off by default. For a provider whose vendor magpie checked in through the plugin's fetch before (WorkBuddy, Trae CN, MiniMax Code, Qoder), it follows that vendor's existing switch until set on its own, and magpie's own check-in leaves that provider's accounts to the plugin (`pluginChecksIn`), so an account is never checked in twice.

Qoder plugins without `auth.checkin` still use [`qoder_checkin.go`](../../internal/provider/qoder_checkin.go) through the plugin's fetch. The server's `CLAIMABLE` status decides whether to claim; the local `[startAt, endAt)` check only recognizes an already-claimed campaign and its expiry, including one that crosses Beijing midnight. Successful results keep the active campaign's end in the optional `until` field of `qoder-checkin.json`. Scheduled checks reuse results from the same Beijing day for at most 30 minutes and never past that end, so a long campaign cannot hide new or reset campaigns indefinitely. The existing loop looks every 30 minutes and on a Beijing day change; a midnight refresh saves today's result for the Usage card and TUI. An `inactive` result is now asked again after 30 minutes rather than settled for the day; failures retain their existing retry schedule. A manual check-in always asks immediately. Saved results without `until` remain readable and use the same 30-minute refresh, with no migration needed. Other vendors and plugins with their own check-in retain their daily settlement.

## How ownership changes

Migration records use four states:

| State | Meaning |
| --- | --- |
| `moving` | Migration is in progress or was interrupted. An explicit retry first rolls back the interrupted move. |
| `plugin` | The plugin owns the built-in provider id. This is the only state recognized by `Moved`. |
| `back` | Ownership returned to the built-in. Automatic handover leaves it there. |
| `failed` | Migration failed and the built-in continues serving. Automatic retry waits six hours. |

The editor's "Move to plugin" action calls `provider.Adopt`. With accounts, adoption delegates to `Move`, which imports and validates accounts and model coverage before committing plugin ownership. With no accounts, adoption verifies the package and the provider it serves, then records plugin ownership without account checks.

Installing the plugin independently can also hand over ownership through `provider.HandOver`. The package must be enabled, meet the mover's minimum version, and serve the expected provider. After add, update, or upgrade, the GUI calls `HandOver(ctx, false)`, which can adopt a built-in with no accounts. Existing built-in accounts wait for `HandOver(ctx, true)` in gateway maintenance.

`KeepRetiringMoved` first runs after 20 seconds, then hourly. Each cycle maintains moved plugin versions, calls `MoveRetiring`, then performs automatic handover. `MoveRetiring` moves subscriptions listed in `provider.Retiring` that have built-in accounts, preserving move-back choices and waiting six hours after a failed attempt. That list is currently empty. Handover preserves a plugin already signed in under its separate `-plugin` id and skips existing migration records except a `failed` record old enough to retry.

`MoveBack` restores built-in ownership and records `back`. Compatible accounts added through the plugin can return too; incompatible accounts can remain with the plugin under its separate id. Removing or disabling a plugin first moves affected built-ins back and stops if that fails. A package installed by the user remains after moving back; a migration-installed package is removed only when no remaining signed-in or moved provider needs it.

New users do not see an unused deprecated built-in in the Add sheet. `unusedSub` leaves it out when it has no account. Until an installed replacement takes ownership, `replacedSub` makes the Add sheet show the plugin tile. The Plugins page is the installation entry point.

`keepMovedCurrent` maintains the required plugin version for moved subscriptions. Raising the owning mover's `min` after publishing a plugin fix triggers this update path. Update failures are logged; disabled packages and local-folder installations are not automatically replaced.

## Constraints for fixes

Determine ownership before choosing the fix location. A bug affecting a moved subscription usually needs a change in the community plugin. A built-in-only fix does not reach moved users.

For a plugin fix, publish the corrected community package and raise the owning mover's `min` to that version. Change the built-in too when the fix must reach users who have not moved.

Keep the migration, host, and upstream behavior separate when investigating failures. A plugin bug can require a community change; a host, provider-card, sign-in orchestration, or migration bug can require a magpie change even for moved users.

The host gets magpie's proxy only as `MAGPIE_*_PROXY` and applies it to each fetch itself. A program a plugin starts, such as the Grok plugin's `grok login`, gets it back as `HTTPS_PROXY`/`HTTP_PROXY` from the spawn wrapper in `host.js`, unless the plugin set one. A sign-in that works in the built-in and fails through the plugin may be a host difference like this one, not a plugin bug.

The host reads its proxy once, when it starts. When the proxy magpie would give a new host differs from the one the running host got (the system proxy set after magpie started, as at login before Clash is up, or Settings changed), the next call to the host replaces it (`proxyMoved` in [`host.go`](../../internal/plugin/host.go), looked at no more than every 15s). Before this, a host started without a proxy kept none, so the Grok plugin's `grok models` couldn't renew its token and the account read as signed out after each restart (#1363).

A plugin's account whose sign-in its vendor refused (`lapsed`, or an allowance read saying so) keeps Sign in again and Remove on its row, whether it is in use or the agent's own (`signedOut` in `renderAccounts`, `app.js`).

The GUI's plugin integration includes `subOf`, `pluginSubs`, and `startPluginSignIn`. A change to sign-in or provider presentation must follow these paths as well as the built-in paths it affects.

## Verification

[`migrate_notice_test.go`](../../internal/provider/migrate_notice_test.go) includes `TestMovedBuiltinsSayTheirPlugin`, which checks that moved built-ins tell contributors which plugin serves them. Migration tests live beside [`migrate.go`](../../internal/provider/migrate.go).

Gateway parity tests in [`plugin_parity_test.go`](../../internal/gateway/plugin_parity_test.go) and other `plugin_*_test.go` files compare or exercise plugin paths. A built-in test alone does not establish moved-user behavior. Inspect each test's fixture to confirm that it covers the provider and operation being changed.

[`plugin_checkin_test.go`](../../internal/provider/plugin_checkin_test.go) runs the fake plugin's `auth.checkin` (`FAKE_CHECKIN`) through the schedule: off, each outcome, no second press the same day, failures only after the retry window, the next day, cards and Settings, and the vendor check-in stepping aside. `plugin-checkin.test.cjs` covers the card row and the Settings tab.

[`plugin_cliproxy_test.go`](../../internal/plugin/plugin_cliproxy_test.go)'s `TestPluginHostTakesANewProxy` starts a real host without a proxy, sets one, and checks that a program the plugin starts gets it. `plugin-signed-out-account.test.cjs` covers a moved Grok account signed out while in use and as the agent's own, in Chromium and WebKit, en and zh, 420 and 1100 wide.

[`quotas_dedupe_test.go`](../../internal/provider/quotas_dedupe_test.go) checks unrelated reset collisions and ZCode deduplication, including plugin usage window conversion for both plugin ids. [`TestPlanQuotas`](../../internal/provider/planquota_test.go) checks the custom GLM endpoint, cached cards, and fallback readings restored from disk. These fixtures do not contact the live vendors or run the community ZCode plugin.

Run the relevant package tests with an isolated home directory. Resolve GOPATH and Go's caches before changing HOME, so repeated runs reuse dependencies and downloaded toolchains. The subshell makes temporary files writable and cleans up its home on exit, including when tests fail, and returns the test command's exit status.

```sh
(
	verification_home=$(mktemp -d) || exit
	trap 'chmod -R u+w "$verification_home"; rm -rf "$verification_home"' EXIT
	verification_gopath=$(go env GOPATH) || exit
	verification_modcache=$(go env GOMODCACHE) || exit
	verification_buildcache=$(go env GOCACHE) || exit
	HOME="$verification_home" GOPATH="$verification_gopath" GOMODCACHE="$verification_modcache" GOCACHE="$verification_buildcache" \
		go test -tags nogui ./internal/provider ./internal/gateway ./internal/plugin
)
```

Some migration and gateway plugin tests require Bun on PATH and use local plugin fixtures; they skip when Bun is unavailable. Report any skipped tests and verify the community package separately when its implementation changes.
