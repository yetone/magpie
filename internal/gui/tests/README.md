# Dropdown browser regression

`settings-proxy-auth.test.cjs` checks optional SOCKS5/SOCKS5h usernames and
passwords in Settings, using the real page with isolated API fixtures. It
covers saving and reloading, escaped and non-UTF-8 credentials, masked
addresses/status, clearing authentication, Tab and Escape, mode switches,
HTTP compatibility, and narrow windows in English and Chinese on Chromium
and WebKit.
## Installed desktop fonts

`fonts.test.cjs` exercises the interface/code font controls with installed
family/style fixtures: independent traits, literal names, search and keyboard
selection, rapid queued saves and rollback, reload, unavailable faces,
discovery failure and refresh, empty collections, platform fallback,
Omarchy precedence and browser-mode isolation. It checks narrow windows and
no scroll on click in Chromium and WebKit, in English and Chinese.
`ARTIFACT_DIR` retains screenshots; `MAGPIE_FONT_ASSETS` can point to an older
asset directory to verify that the test fails on the missing picker.
`font-cascade.test.cjs` checks that a reset restores the original weights
under bold parents, while explicit component emphasis survives a choice.

```sh
node --test internal/gui/tests/fonts.test.cjs internal/gui/tests/font-cascade.test.cjs internal/gui/tests/win-fonts.test.cjs
go test -v ./internal/fonts
go test -tags nogui ./internal/settings ./internal/gui -run '^TestFont'
```

The native test reads the operating system's actual installed font metadata.
Run `go test -v ./internal/fonts` without `nogui` on each desktop platform.
Browser fixtures never read or write user settings, install fonts or contact
a live gateway.

## Gateway Caller Keys

`gateway-caller-keys.test.cjs` checks the named caller-key list on the
Gateway page, including creation, copying, renaming, disabling, enabling,
rotation and deletion. It verifies key-level usage overview, request filtering and
CSV export in Chinese and English on Chromium and WebKit. The fixtures do
not access local credentials. Gateway keys appear only while sharing is on, with one create entry point
and confirmation before rotation/removal (Cancel and Escape send no mutation).
Creation, renaming and confirmation focus their controls without scrolling.
Gateway is the only key-management page;
Settings controls sharing and shows addresses. The test checks that toggling
sharing retains the key, and that a removed default key is recreated as
Magpie and appears in Gateway without a reload.

`gateway-connect-keys.test.cjs` selects loopback and LAN addresses and enabled
caller keys for Shell, curl, Python and Node examples across all four APIs.
It checks rotation, disabling, removal, stale list responses, empty states,
literal custom names and narrow layouts in Chinese and English on both engines.
Sharing off keeps the original API-key field and hides the gateway-key picker;
sharing on names the arbitrary local option separately from the Magpie key.

`gateway-takeover.test.cjs` checks the Gateway card's button: with an older
magpie serving the gateway it reads "Quit magpie {v} and take over" and posts
`/api/gateway/take-over`; once this magpie serves it is "Restart gateway" and
posts `/api/gateway/restart`; a magpie of the same version gets no button. A
refusal (a program that isn't magpie on the port) is said under it with the
program's path and pid; a container's forwarder on the port (OrbStack
Helper) is said as a container to stop, with `docker stop`, and in dark mode
the button is bordered so it reads as one. No click scrolls, nothing has a
left border, in Chinese and English.

`gateway-key-limit.test.cjs` checks a gateway key's own limit (#585): each
limited key's badge and line (used, limit, left, reset, requests in flight,
calls without a price), a spent key's "refused until", and the limit editor
under a key's row. Window, tokens, cost and cache reads are staged, marked
unsaved and sent only by Save; Cancel drops them, a bad amount disables Save,
and No limit + Save sends `null`. No click scrolls the page, nothing has a
left border, and the open editor fits a 560px window, in Chinese and English.

`gateway-key-models.test.cjs` checks the models a gateway key may use
(#882): the badge on each key's row ("All models", or how many), the app's
own menu (no native select) that stays inside a short window, picks sent
only when it closes, and "All models" taking the restriction off. No click
scrolls the page, nothing has a left border, and the row fits a 560px
window, in Chinese and English.

`api-key-usage.test.cjs` exercises the existing provider key list: adding,
enabling and disabling, choosing the first key, renaming and removing.
The Usage page's Gateway key rows, request filter and CSV exports identify
client keys, not those provider credentials. A client using different
upstream keys stays grouped together; renaming an upstream key does not
rename the client. It also checks historical records, Chinese and English,
and the narrow window in Chromium and WebKit.
The API fixtures use test keys and never read local user configuration.
The existing `usage-ledger.test.cjs` also checks request-route and caller-key
filters together, CSV export, and restoring the previous caller filter when
the request filter is cleared. A caller absent from the period's options clears
the filter and reloads the unfiltered rows from the first page.

`api-key-theme.test.cjs` checks Gateway's enabled and disabled keys, creation
and renaming inputs, rotation and key picker, plus Settings' LAN address
controls against the global palette.
Light and Dark override the OS; System follows live OS palette changes.
The settings theme picker is also switched and reloaded in Chromium and
WebKit. No separate colours are defined for gateway keys.

```sh
node --test --test-concurrency=1 internal/gui/tests/gateway-caller-keys.test.cjs internal/gui/tests/gateway-connect-keys.test.cjs internal/gui/tests/gateway-key-limit.test.cjs internal/gui/tests/gateway-key-models.test.cjs internal/gui/tests/api-key-usage.test.cjs internal/gui/tests/api-key-theme.test.cjs
```

## Other Browser Regressions

`purpose-filter.test.cjs` checks Usage and Routing purpose filters (#742) in
English and Chinese on Chromium and WebKit. It covers title aliases, literal
unknown names, unmarked records, pagination and totals, combined failure
filters and CSV export, route navigation, session grouping, historical days
and the narrow layout. Routing also covers keyboard dismissal and clearing
the purpose while keeping the selected day and session grouping. It uses
isolated API fixtures. Run with
`node --test internal/gui/tests/purpose-filter.test.cjs` and the Playwright
environment described below.

`routing-purpose-state.test.cjs` covers #771's follow-ups: selected-purpose
counts (including broken-off 200s and failures without an HTTP status, while
excluding informational notes on successful 200s), the failed-request link,
the latest matching story through live updates and returning to the page,
preserving a picked request, an empty historical selection, and both ways
to clear the compact purpose control. Unfiltered
live counters retain gateway lifetime totals; scoped counters describe the
completed requests kept in the selected list. Run with
`node --test internal/gui/tests/routing-purpose-state.test.cjs`.

Request-kind aliases and English labels live in `internal/usage/purpose.go`.
After changing them, run `go generate ./internal/usage` to update
`assets/purposes.js`; `TestPurposeAssetMatchesUsage` rejects a stale asset.

`routing-sealed-task.test.cjs` checks that routing explains why an encrypted
subagent task excludes non-ChatGPT providers and why the parent account goes
first (#619). It also checks plain tasks, old traces, direct model requests,
short-window allowance wording and rules with no eligible candidate, in
English and Chinese on Chromium and WebKit:

```sh
node --test internal/gui/tests/routing-sealed-task.test.cjs
```

`routing-sessions.test.cjs` checks the Routing request list in Chromium and
WebKit, English and Chinese: the list defaults to By request and remembers
the grouping choice across reloads; sessions are separated by agent and ID across
model changes, known and partial costs are summed, zero partial estimates
retain their amount and +, single-request headings use the singular, zero prices stay known,
title helpers merge only into their explicit parent (even arriving first),
unnamed Codex memory-only groups explain their background purpose while keeping
separate IDs, costs and folds; named and mixed chat groups keep their titles,
late chat names and renames update while preserving folds and ID tooltips,
missing IDs and old unpriced requests remain visible, folded sessions stay
folded through live updates, costs follow the currency choice, and the original
time-ordered list, history selection and routing story still work. It also
checks the request list at 1440 and 560 pixels. Run with
`node --test internal/gui/tests/routing-sessions.test.cjs` and the same
Playwright environment described below. APIs are isolated fixtures.

`session-terminal.test.cjs` checks the macOS Settings choice for installed
`.command` handlers in English and Chinese. The system default appears once
and is selected at first. It selects Ghostty, changes the theme, then returns
to the system default, reloading to verify both saved choices.
The API is faked and no terminal app is launched.

`menu-scroll.test.cjs` loads the real HTML, CSS and JavaScript with isolated API
fixtures. It checks session folder/model filters and the main model picker in
Chromium and WebKit, including wheel, scrollbar track/thumb, keyboard selection
and dismissal. It does not start the backend or read local user configuration.

`provider-levels-scroll.test.cjs` keeps the provider editor's scrollable model
list in place after saving a reasoning level farther down the list.

`routing-save.test.cjs` checks that a provider's Routing and Stays, in its
editor, are staged and made with the editor's Save: each option clicked shows
what it does and "unsaved", with nothing sent, redrawn or scrolled; Cancel
drops them; one Save carries `routing` and `affinity`. For a subscription
with several accounts and a provider with several keys, in English and
Chinese, Chromium and WebKit.

`click-scroll.test.cjs` holds the rule that a click never moves the page
(see "where the reader is" in `app.js`): on the Routing page, Live and a day
picked in turn with the list scrolled to its end; a click whose handler sets
scrollTop, grows the page above, redraws itself, or scrolls from a timer or a
shared helper; a control under what it unrolls (`data-unrolls`) going down
with it; a click that asks to go somewhere with `scrollOnPurpose(e)`;
the room kept at the foot going as the reader scrolls back; and the wheel.

`routing-days-fit.test.cjs` checks Routing › Requests' day bar with the 30
days the gateway keeps (#961): at 1100px, 733px (1100 at 150% text size) and
440px no pill is cut off at its edge and the days that don't fit are under its
last pill, whose menu lists each with its count; the oldest picked from it is
asked for and names the pill, with the bar held where it was; three days need
no such pill; a window narrowed then widened fits again. Chromium and WebKit,
in English, Chinese, Japanese and German.

`agent-disconnect-preview.test.cjs` checks the disconnect confirmation's
file previews fill the body instead of the editor's label column. omp and
Codex previews, restored values, expanding hidden lines, scrolling, Cancel,
and Codex's separate note are covered at 890×800 and 560×420, including a
scaled viewport for 150% text size, in English and Chinese on Chromium and
WebKit. The test uses the real assets with isolated API fixtures and never
changes user config.
Set `ARTIFACT_DIR` to retain screenshots.

```sh
node --test internal/gui/tests/agent-disconnect-preview.test.cjs
```

`agent-unreachable.test.cjs` checks an agent whose config is right but whose
address doesn't answer (#1013): its line says so in red with the advice as
its tooltip, its pill is "How to fix", which opens the advice in a dialog and
sets nothing, and when WSL reaches Windows at another address now the pill is
"Use <address>" and posts the reapply. The window at 560px and the tray panel
at 440px, in English, Chinese, Japanese and German on Chromium and WebKit.

```sh
node --test internal/gui/tests/agent-unreachable.test.cjs
```

`agent-layout.test.cjs` keeps the main window's agent names readable at 520,
560 and 600 CSS pixels, while the model and effort controls stay inside their
rows. At 601, the default 660 and 960 pixels, controls remain aligned beside
the names. English and Chinese, Chromium and WebKit.

`signin-callback.test.cjs` checks a sign-in's pasted callback (the field a
sign-in with pasteCallback shows) in a narrow Chinese
dark window: invalid input remains editable, retry reaches the callback route,
and a pending or accepted submission cannot be submitted twice.

`signin-paste-plugin.test.cjs` checks a plugin's browser sign-in that comes
back to a port on magpie's machine: the plugin's words, the pasted-address
field posted to the sign-in, and the plugin's API key way, in a 360px window.
English and Chinese, Chromium and WebKit.

`plugin-updates.test.cjs` checks the dot on Plugins while a plugin's update
waits for the reader, gone once it's updated, and the "Auto-updated" chip on a
plugin magpie updated by itself.

`plugin-signin.test.cjs` checks a provider an OpenCode plugin signs in to:
the add sheet lists it under "From plugins"; its sign-in asks the way, the
method's questions (a pick, then a text the plugin checks), then takes the
code the browser page shows; an API-key way opens the account. English and
Chinese, Chromium and WebKit, with the API faked.

`plugin-move-overflow.test.cjs` checks a Plugins card that offers a
built-in's accounts: the button is one word beside the name, as Install is
("Move", its title saying whose accounts and how many), on one line, and it
stays inside the card and the plugin's name and "magpie community" aren't
cut, in a three-, two- and one-column window. English and Chinese, Chromium and WebKit, with the API
faked.

`deprecation-notice.test.cjs` checks the deprecation notice over the
Providers list is as calm as the list's rows: the list's own background and
border, an amber dot and no badge, icon tile or filled button, the line naming
Devin and WorkBuddy AI with why under it (no space after a Chinese full stop),
and Not now and Review in Plugins side by side on its right, each on one line;
on a 390px phone they go under the text at its end with no sideways scroll.
English and Chinese, light and dark, Chromium and WebKit.

`plugin-movable-review.test.cjs` checks the line over the Providers list
naming several built-in subscriptions a community plugin can run: its Take a
look opens the Plugins tab, whose cards offer each one's Move, not the first
one's editor. English and Chinese, Chromium and WebKit, with the API faked.

`plugin-key-hint.test.cjs` checks a plugin's API-key way: its label titles
the key's field and its placeholder is the field's hint, while a way labelled
only "API key" keeps "<name> API key"; the provider's own icon shows on its
row. English and Chinese, Chromium and WebKit, with the API faked.

`plugin-market.test.cjs` checks the Plugins tab: Discover lists the
suggested plugins in their two sections, a card installs its plugin and
then offers its sign-in, which opens in the Providers add sheet; a search
filters the list at once and adds what npm has; a card opens the plugin's
page with its README (no pictures, links opened outside); Installed shows
why one didn't load, updates one and removes one. The add sheet's
"More in Plugins" row (the providers list has no button of its own) and
"look for a plugin" link lead there. English and Chinese, Chromium and WebKit,
with the API faked.

`plugin-git.test.cjs` checks a plugin installed from a git repository: the
market's field says it takes a GitHub repo, and Install sends
`github:owner/repo` as typed; Installed names it by the package its
repository holds, its Update fetches the repository again, and its page shows
the README it came with, no npm link and a Source link to the repository.
No click scrolls. English and Chinese, Chromium and WebKit, with the API
faked:

```sh
node --test --test-concurrency=1 internal/gui/tests/plugin-git.test.cjs
```

`panel-fold.test.cjs` expands and collapses on the Agents page with the list
scrolled to its end, in the tray panel (one agent open) and in the window:
"Show {n} more" unrolls the rest under the button, the view going down with
them and never back up; "Show less", and a row opened and closed, leave what
was clicked where it is on every frame.

`rollup-short.test.cjs` folds the Agents page's list in a window taller
than the list rolled up (#355): 24 agents, 5 in use and 19 folded, 4 of
them hidden. "Show {n} more" takes the button up the view; "Show less"
leaves it where it is on every frame painted, the room kept at the view's
foot measured from where the content ends rather than from scrollHeight
(never less than the view is tall), and the room goes once the reader
scrolls back to the top. With Reduce motion and without, Chromium and
WebKit, English and Chinese.

`rail-tip.test.cjs` hovers the model picker's rail in Chromium and WebKit,
English light and Chinese dark: an icon's name shows to its right, level with
it and over no other icon (the browser's tooltip put Devin's name on ZCode's
Z), moves at once to the next icon, and goes on a click, on leaving, and when
Esc closes the picker; a rail icon focused from the keyboard is named too.

`agent-models-all-hidden.test.cjs` takes every model out of DeepSeek
Harness's lists with "Hide all" (#356): the line under its name reads
"Showing 0 / 3 models" (显示 0 / 3 个模型), stays once the list is closed and
the agents are drawn again from the state, and opens the list again, where
"Show all" puts them back; no click moves the page. Chromium and WebKit,
English and Chinese.

`agent-models-picker.test.cjs` takes two of Claude Code · WSL Ubuntu's
three models out in its model list, then clicks its model picker, which
closes the list and opens on the one left at once, while the state that
follows is still on its way (#927). Chromium and WebKit, English and
Chinese.

`agent-models.test.cjs` opens Codex's model list from the line under its
name, in Chromium and WebKit, English and Chinese: the line reads "Showing 8 /
31 models" under the name, opening it moves nothing and puts it on the screen
whole, routing groups come first and OpenRouter's 24 start folded, the model
Codex is set to can't be taken out, a click takes one out at once with the
whole list sent and the count under the name following, a search opens a
folded group, "Shown" keeps one just turned off till the view changes, a
group's "Show all" comes on hover, "Hide all" at the foot sends every one
but the model in use, "Show all" there sends none, and Esc or a
click elsewhere closes it.

`omp-roles.test.cjs` draws omp's subagents, smol and slow roles in Chromium
and WebKit, English and Chinese: each is a square after the model's picker,
not a picker of its own, as they follow the model while unset; unset it is
named "same as model" (「同主模型」), its picker opens on "Same as model" with
the model beside it, and picked it names the model it is on. OpenCode's
small model, which doesn't follow the model, stays a picker.

`subagent-effort.test.cjs` draws Codex's subagent effort (#469) in Chromium
and WebKit, English and Chinese: a square beside the subagents' one, not a
third picker; unset, its label says what that means (the session's effort, or
the subagent model's own default; 「跟随当前会话的推理强度…」), a click opens
the effort slider without scrolling the page, Default its first stop, and a
level picked is posted for `subagent_effort` alone and lights the square.
Claude Code's subagents (#468) get no square until it runs through magpie
(nothing to pick), and one that says "same as model" once it does.

`tier-effort.test.cjs` draws Claude Code's per-tier effort (#536) in Chromium
and WebKit, English and Chinese: no tier's effort is a field of its own; the
tiers' square lists each tier's model and then its effort (the bars, and "the
effort Claude Code asks for" / 「跟随 Claude Code 请求的推理强度」 unset); an
effort entry opens the effort slider without scrolling the page, Default its
first stop, a level picked is posted for that tier's `<tier>_effort` alone and
named in the square's title, and the session's effort stays. Its subagents'
effort is a square that says what unset means for Claude Code, and a Claude
Code not through magpie has no levels, no square and no effort entries.

`gateway-fold.test.cjs` folds Connect on the Gateway page with the view
scrolled: its fields hide, the head keeps the base URL and a copy button, the
head stays where it was, and the fold is remembered across a reload.

`sse-body.test.cjs` opens streamed replies in the Gateway page's recent
calls (Anthropic Messages, Chat Completions with `[DONE]`, OpenAI Responses cut
before it completed): the response body reads as each event's data
pretty-printed under its name, as the reply put together (text, reasoning, tool
calls with their arguments as objects, how it stopped, usage) or as it came,
the pick remembered. A 450-event stream draws 200 at a time from a button under
the box that stays where it is, the page too; a body that isn't a stream is as
before. English and Chinese, Chromium and WebKit.

`json-tree.test.cjs` opens a JSON request and response in the Gateway page's
recent calls: keys, strings, numbers and keywords each have their colour, the
body's text is still the pretty-printed JSON, an object or array folds to
`{…}` with its count and unfolds from its arrow or that summary with the page
kept where it is, a fold outlives the list being drawn again, a value's copy
button (shown on hovering its line) copies a string's text or an object's
JSON, a click on a key copies its name, and a stream's events and its reply
fold the same way. English, Chinese, Japanese and German, 1000px and 440px,
Chromium and WebKit.

`routing-kind.test.cjs` lists calls Codex makes for itself (a guardian review,
a title, memories, a turn on Luna Reserve, a kind it does not know yet): each
has a grey tag by its model in the Requests list, in English and Chinese, the
model keeping its room first, and the request's story says what it was.

`routing-log-icons.test.cjs` opens a request that failed on a Zhipu account and
was answered by an OpenAI key: the story's why line and each try's line lead
with the provider's logo before the account or key they name, 14px and on the
name's line, and what the vendor said word for word has none. English and
Chinese, Chromium and WebKit.

`routing-side-calls.test.cjs` lists what a DeepSeek chat in Codex sends besides
its turns (#314): the new chat's title, which Codex asks of its own Luna on a
hidden thread (known by its turn metadata), and the web searches magpie runs
for DeepSeek on the model it searches with. Each has its grey tag (Title /
标题, Web search / 联网搜索), a search's story names the agent and model it
was for, and picking a request moves nothing. Chromium and WebKit, English
and Chinese.

`routing-effort-row.test.cjs` lists live requests sent at high reasoning, one
under way, one with two tries, in Chromium and WebKit, English and Chinese, at
1440, 1000 and 480px: in every row the "· high" is shown whole and no run of
text is drawn over another (#273: in two columns of ~470px it sat on the time
taken).

`routing-effort-change.test.cjs` lists live requests whose reasoning was
changed on the way ("· medium → low", "· medium → xhigh", one under way, one
picked for the turn) beside "15 s · TTFT 14 s · 6.9k tokens", with one further
down whose numbers run long (three tries, minutes, a million tokens), in
Chromium and WebKit, English and Chinese, in the dark theme, at widths from
1440 down to 400px, and again with the styles as Safari 15 reads them (no
subgrid, no container queries). In every row the reasoning stays within where
it went and no run of text is drawn over another (#435, azir12345: 文字重叠 —
the numbers' column, as wide as the longest numbers, left the reasoning next to
no room and it ran over them); where it went and the level asked for are cut
first, and the level sent is always whole.

`routing-levels.test.cjs` edits a group's reasoning levels in Chromium and
WebKit, English and Chinese (#295: a member with low/high/max took medium and
xhigh from the rest). "Its models' shared" names those levels; "Named" shows a
toggle per level, starting from the shared ones, and toggling them moves
nothing; saved, they go lowest first and the group's family stays. A group with
its own opens on them, none picked is refused, and back to shared saves none.

`group-member-fast.test.cjs` sends a group's member in its vendor's fast mode,
in Chromium and WebKit, English and Chinese (vs on Discord: "gpt-6.1 sol ·
high · fast"). The group's card reads "· fast" after the member's reasoning; in
the editor a model with a fast mode shows a "Standard speed"/"Fast" toggle
beside its reasoning and one without none, toggling moves nothing on the page,
a member whose reasoning changes stays fast under its new id, one taken out
leaves it, and the save sends the members sent fast as fast.

`group-found-off.test.cjs` turns the groups magpie finds on its own off and on
again, in Chromium and WebKit, English and Chinese (蓝猫 on Discord: "路由分组会自动创建，
可以关闭掉吗"). A switch right under the routing groups' head says what found
groups are; clicked off it posts groups/found {on:false}, the found group
leaves the list while the user's stays, the hint says only theirs are served,
and the status line says which agent was moved to the model from one provider;
clicked on, the found group is back. Neither click moves the page, the switch
has no left-border accent, and every string has its Chinese.

`group-id-dots.test.cjs` checks that a routing group's id keeps the dots of a
model's name (#968): a new group named "GPT 6.1 Sol" is said and sent as
`group/gpt-6.1-sol`, and `groupSlug` folds a run of dots and trims them at the
ends as `provider.GroupSlug` does, in English and Chinese, Chromium and WebKit.

`group-new-head.test.cjs` checks that a routing group of any models can be
made from the top of the Routing page, in Chromium and WebKit, English and
Chinese (mintonight, #944: the groups' New group sat below the requests, so
only found groups were used). The page's head has New group with a plus and a
tooltip saying what a group is, in sight while the groups are below the fold;
its click opens an empty group's editor, brings it into view and focuses its
name. The groups' own New group has the plus too. No left-border accent.

`name-suffix-where-named.test.cjs` checks the setting that says whether a
provider's name or "· routing group" follows a name in the agents' lists (#868)
where names are given, in Chromium and WebKit, English and Chinese. The
provider editor's Names & levels has its three choices and says what the lists
show. For the groups it is one setting for all of them, so it sits over the
groups on Routing (PAMI on Discord: in one group's editor it read as that
group's): a group's editor has no choices, only what its group will be called
and that it is set above; a pick there posts settings/plain-names once and the
open editor's line follows it with its unsaved name. No click scrolls, nothing
native, no left border, and every string has its Chinese, Japanese and German.

`routing-manual.test.cjs` routes a group by hand in Chromium and WebKit,
English and Chinese (#317: pick the model, as CC Switch picks a provider). A
manual group's card lists its models, the one every request goes to marked, and
its rules tag says they wait; the reader wheels down to it and clicks another,
which saves the group with that pick and its other fields as they were, the
editor staying shut and nothing on the page moving, and clicking the one picked
does nothing. The editor offers Manual with its hint and keeps the pick on save.

`routing-served.test.cjs` lists a request whose vendor's reply names another
model than the one asked for (gpt-6-sol served as gpt-6-luna), one answered
under the model's dated name and ones naming none: only the first is marked
"served gpt-6-luna" by its answer in the Requests list and "requested
gpt-6-sol · served gpt-6-luna" in its story, in English and Chinese, and the
click that picks it leaves the page where it is.

`remote-group-served.test.cjs` lists a request sent to a remote magpie for one
of its routing groups (group/auto-deepseek-v4-1-flash answered by
deepseek/deepseek-v4.1-flash) beside a real swap: the group's member is shown
plain, not amber, in the Routing page's Requests list, with a title (and a
line in its story) saying the remote magpie's group routed the request there,
and muted with the same title in the Usage page's Requests, while the swap
stays amber; Chromium and WebKit, English and Chinese, and the click that
picks it leaves the page where it is.

`routing-reroute-title.test.cjs` lists a request the plan's account rate
limited and a relay took, and one answered on its first try: the rerouted
one's title names the relay and its model, as its row does, then the account
it was rerouted from, in the window's Requests list and the tray panel's
Routing tab alike; the other keeps "agent · model → provider". English and
Chinese, Chromium and WebKit (#337).

`plain-names.test.cjs` checks the Settings page's "Provider in model names"
row (#335): on by default, Off posts `settings/plain-names` with `mode: "off"`
on its own and lights Off, On posts it back, and neither click scrolls the
page. English and Chinese, Chromium and WebKit.

`model-suffix-own.test.cjs` checks that row's third way (#92): "Not on names
I set" (自定义名称不带供应商) between Off and On, lit by `plainOwnNames`; On and
then it post `settings/plain-names` with `mode: "on"` and `mode: "own"`,
neither click scrolls the page, the hint isn't cut short and the three fit
the row at 900px and 560px. English and Chinese, Chromium and WebKit.

`routing-steady.test.cjs` scrolls the Routing page down to its routing groups,
opens one in its editor and types into its name, then streams six requests in
from the trace (new agents and accounts on the stage, failures and retries in
the story, the lists growing): the groups and the editor stay where they are
on the screen on every layout and scroll, the view's scrollTop moving by just
what grew above them, and the field keeps its focus and what was typed.

`routing-flood.test.cjs` takes the Routing page out of sight three ways
(#302): the window hidden, the window covered and drawing no frames
without saying it is hidden, and another tab open. Meanwhile 30 requests
come and are answered, and 2 are still under way. When the page is seen
again, at most two magpies per request still under way fly at once, those
requests do fly, and all 33 are listed. It runs in English and Chinese.
Before the fix, 39 to 62 birds flew at once.

`routing-busy.test.cjs` streams requests into the Routing page, one trace
update each (#308). The row of a request that hasn't changed stays the same
element, and a click on it still picks it. With another tab open, the list
isn't touched, and coming back lists every request that came meanwhile. It
runs in English and Chinese.

`balance-fix.test.cjs` opens a custom provider whose balance token sits
beside new-api's `/api/usage/token` (and a new one with a token and no
Balance URL): the editor says so, one click moves it to `/api/user/self`
with the quota as its field, the New-Api-User header is asked for until it
is typed, Check balance asks as the form has it and says the fix plainly,
and the Usage page's card does too.

`balance-field-default.test.cjs` leaves a custom provider's Balance field
empty (#881): its placeholder is the field magpie reads from a Balance URL
whose reply it knows (new-api's `/api/usage/token` and `/api/user/self`, a
sub2api panel's `/api/v1/user/profile`, OpenAI's `credit_grants`), following
the URL as it is typed; any other URL keeps the example.

`cli-update.test.cjs` draws the agents' CLI versions on the Agents page
(#202) from a faked API: the version after each name, "Update to x.y.z" only
where magpie knows how the CLI was installed, the rows' height kept, an
update clicked with the view scrolled (busy, then the new version, the page
left where it was), a failed one giving its reason and the pill back, and
the words in Chinese. Nothing is installed.

`panel-profiles.test.cjs` saves a profile in the tray panel with the list
scrolled to its end: "＋ Save current" opens the name field beside it, the
list not moving, field and button in sight, one focus ring; the button then
reads Save and a click on it saves, as Enter does; an empty name keeps the
field; Escape closes it; in English and Chinese.

`profile-details.test.cjs` (#467) clicks a saved profile's chip, in the
window and in the tray panel with its list scrolled to its end: the details
open, applying nothing and leaving the chip where it was on the screen; they
list each agent by name, its fields under their labels (an empty one as the
agent's default, one that reads as a key as dots, all four of Claude Code's
tiers, one left empty as following the main model and which that is, #480)
and what the Library gives it; a second click closes them; their Apply applies the profile, once; ↻ and
× stay on the chip. In Chromium and WebKit, English and Chinese:

`profile-confirm.test.cjs` (#478), in the window and in the tray panel: a
chip's ↻ and × change nothing on a first click, reading "Overwrite?" and
"Delete?" instead, and act on a second; the question goes on Escape or when
left alone. The details' × closes them, the chip staying where it is; Escape
closes the details, then the panel's list, and only then hides the panel;
the list closed with the details open opens again on the chips alone; Apply
closes the details, the tray's list opening again on the chips, and their ×
closes them after it (#489). In Chromium and WebKit, English and Chinese:

```sh
node --test --test-concurrency=1 internal/gui/tests/profile-details.test.cjs internal/gui/tests/panel-profiles.test.cjs internal/gui/tests/profile-confirm.test.cjs
```

`panel-routing.test.cjs` opens the tray panel's Routing tab: the gateway's
latest requests from a faked trace, newest first, each with its agent, the
model asked for, the provider and account it went to, the model that
answered ("served gpt-6-luna" on the one a vendor answered with another, not
on a dated name), a failure's status, and the time; today's calls and tokens
over them; the allowances' tab named Allowances. A click, with the panel
scrolled, asks for the window's Routing page on that request and moves
nothing; the window opened so has that request picked. In English and
Chinese.

`login-import.test.cjs` brings ChatGPT accounts in from CLIProxyAPI's auth
files: offered from the accounts list and from a browser sign-in under way,
the box says the refresh spends the file's sign-in, what is pasted is posted
as it is, and each account's outcome is listed, in English and Chinese.

`agy-launch.test.cjs` gives Antigravity CLI's row the square that copies the
command starting agy on magpie: the command in its tooltip, a click copying
it with the page left where it was, none on other agents, in the tray
panel's opened row too, in English and Chinese.

`currency.test.cjs` shows a cost in dollars by default and in yuan, at
magpie's cached exchange rate, once the Settings page's Currency row picks
cny (#212): the Usage page's total converts, the row's tooltip carries the
rate, picking it with the settings list scrolled well down moves nothing,
and the choice survives a reload — in English and Chinese.

`tray-usages.test.cjs` picks several of the Usage page's cards for the menu
bar in Settings: the menu keeps open as Codex and Claude Code are ticked
beside Copilot, posts nothing till it closes and then the three once, in the
menu's order; the pill reads "3 subscriptions"; a menu opened and closed
unchanged posts nothing; a save of another setting keeps them; Off unticks
them all, closing the menu and the refresh row; no row has a left-border
stripe and no click moves the page; in English and Chinese.

`tray-logos.test.cjs` turns the menu bar's logos off and on in Settings: on a
Mac with a card in the menu bar the Menu bar logos row shows On by default;
Off posts `trayNoLogos: true`, a save of another setting keeps it, On posts it
back; the row has no left-border stripe, no click moves the page, and there is
no row with no card picked nor off a Mac; in English and Chinese.

`tray-bird.test.cjs` turns the menu bar's magpie icon off and on in Settings:
on a Mac with a card in the menu bar the Menu bar magpie icon row, under the
logos row, shows On by default; Off posts `trayNoBird: true`, a save of
another setting keeps it, On posts it back; no click moves the page, and there
is no row with no card picked nor off a Mac; in English and Chinese.

`tray-cell-click.test.cjs` checks menu-bar quota navigation in the panel
and main window: selecting Allowances/Overview, scrolling to and highlighting
the matching account or balance, and keeping a click pending while quotas
load. It checks that the panel loads Wails so native ExecJS can run.
A focus-triggered redraw preserves the remaining highlight animation in both
panel and main window. It expands and remembers folded target accounts,
disables panel-header dragging,
and cancels navigation after five seconds or a purposeful user scroll.
It runs in English and Chinese on Chromium and WebKit, with a mocked API. Run with
`node --test internal/gui/tests/tray-cell-click.test.cjs`.

`panel-arrange.test.cjs` opens the tray panel's Allowances tab at 440px with a
subscription hidden in the settings: it has no card and the foot says one is
hidden. *Arrange* keeps the page where the reader scrolled it and lists a row
a subscription in the shared order, with the note that hiding is the panel's
only; the grip by a logo shows on hover only, and no row has a left stripe or
runs off sideways. *Show* and *Hide* post `panelHidden` without moving the
page, a failed save is put back, Alt+arrow and a drag by the logo post the
`order`, and *Done* draws the cards in it. A menu bar cell for a hidden
subscription still opens its card until the panel is put away, and the Usage
page keeps the hidden card in the same order. It runs in English, Chinese,
Japanese and German on Chromium and WebKit, with a mocked API.

`TestTrayCellClickReleasedPanel` (darwin, cgo, GUI) runs a separate AppKit
process with an isolated config and a minimal page: a quota click recreates
the released panel, queued ExecJS selects the account once Wails is ready,
the next click closes it, and another release can be reopened and focused.
It requires a macOS graphical session and does not start the magpie backend
or create a status item. Run with
`go test ./internal/gui -run '^TestTrayCellClickReleasedPanel$' -count=1`.

`text-size.test.cjs` checks the Settings page's Text size row (100, 110,
125 and 150%): a pick posts to /api/settings/text-size without scrolling
the page, stays picked after a reload (boot.js carries it, and the page's
`--zoom` has it before the first paint), and a save of the other settings
leaves it be; Ctrl/Cmd + = and − step through the sizes and 0 goes back to
100%, in the window and the tray panel, the other modifier doing nothing.
The zoom is the webview's, so here it is as a browser zooms: the smallest
window at 150% (840×630 points) is 560×420 CSS pixels at a device scale of
1.5, and no view (nor the panel at 150%) runs off to the side, the Mac
header still 50 points tall for the traffic lights. At 150% in a 1440×900
window scrolled down, a click on 100% (the window then 1440×900 CSS pixels)
leaves no `.view-room` and no blank under the page, nor scrolls it beyond
the browser's own fit (#911). English and Chinese, light and dark;
ARTIFACT_DIR gets screenshots at 100% and 150%.

`settings-groups.test.cjs` puts the Settings page's warm-ups and check-in
under a tab per service (#124): Codex, Claude Code and WorkBuddy tabs where a
heading would be, in the Usage part after its list, Codex's picked to
begin with and each showing its rows alone, named without the service; the
last warm-up and today's check-in still on the short lines, no left-border
stripe; a daily warm-up's time field only while it is on; each control
posting the setting it did and a tab posting nothing, with the page scrolled
and left where it was; the arrows, Home and End along the tabs; a tab picked
at the page's very end (a shorter card, then a taller one) leaving the tabs
where they were; the tab remembered across a reload, Codex's shown when the
remembered WorkBuddy one is gone; the WorkBuddy tab only with an account
signed in; in English and Chinese.

`settings-sections.test.cjs` checks the Settings page in parts (#471): a tab
list at its top — General, Usage, Network and sharing, Models, Privacy,
Observability, Sync and backup, About — each tab showing its part's rows
alone, posting nothing and leaving the page and the tab where they were; a
part's controls still posting what they did; one tab in the Tab order and
the arrows, Home and End along them; the part kept in the address
(?view=settings&tab=…) across a reload, out of it on another view, and
remembered for Settings opened again without one; a link to a part opening
on it. The tabs fit, on a line more where they must, in the 560×420 window
at 100, 110 and 125% and in magpie web on a phone, nothing running off to
the side. Chromium and WebKit, English and Chinese; ARTIFACT_DIR gets a
screenshot of each size:

```sh
node --test --test-concurrency=1 internal/gui/tests/settings-sections.test.cjs internal/gui/tests/settings-groups.test.cjs
```

`update-check.test.cjs` checks the version row: the button stays, dimmed,
through a check, a second click asks nothing, and the answer puts it back.
A read still out cannot draw "checking" over that answer. A row drawn again
while the check runs keeps the button and catches up when the check answers.
A failed check gives the button back, and a check already under way shows
the button without starting another. The page stays where it was. English
and Chinese.

`update-wait.test.cjs` checks a restart to update with the gateway busy
(#577): Settings → Version says what is in flight (requests, Claude Code
turns awaiting tool results), Restart to update then waits with "Restart
now" and "Cancel", the counts follow the gateway, a wait that ran out says
so, and with nothing in flight the click restarts at once. The header's
Update pill waits on its click and restarts at once on the next (not on a
double click's second half). No click moves the page, no coloured left
border. English and Chinese.

`refresh-page.test.cjs` checks the header's refresh reads again what the
page shown draws (#844): on Agents it looks for the agents again (one just
installed shows, and leaves Install another agent), on Usage it reads the
allowances afresh, on Providers it refreshes the model lists. It never looks
for a newer magpie (Settings › About's Check does), its tooltip says what it
reads, and no click moves the page. Window and panel, English and Chinese.

`update-hide.test.cjs` keeps the header's Update pill away when asked (Wang
Hsiaohi on Discord): its ×, shown with the pointer over the pill, hides it for
that version, through a reload, until a newer one is out; a right-click does
the same. Settings → Update button says which version it is hidden for, with
Show again, and turns it off for good; the version row still offers the
restart. No click moves the page. Panel and window, English and Chinese.

`update-download-percent.test.cjs` checks the Update pill while a failed
update downloads again (inaction on Discord): with the size known and
nothing in yet it says 0%, with no size "Downloading…", then the percent —
never NaN, which it said while the answer left `done` out. The click doesn't
move the page. English and Chinese.

`update-auto.test.cjs` checks Settings → Automatic updates (#472): on, every
6 h, with nothing set; Check every saves 30 min, 1 h, 6 h or 24 h; Off is
saved and kept through a reload, the interval staying in place, dimmed, for
when it is on again; the version row, not asked yet, says automatic updates
are off and its Check asks the backend. No click moves the page. English and
Chinese, Chromium and WebKit.

`add-sheet.test.cjs` opens the Providers page's add sheet as quiet rows:
Subscriptions, Vendors, Relays and On this machine, each with its word on
what it is; rows with no border, no second line and no overflow, three to a
line at 900px; an added provider not faded but marked by a small green dot
after its name, a grey count only past one account (the Claude subscription's
2); short names with the full name, plans and host in the title; a vendor's
global and China presets one row with no tag, the regions' hosts in its
title, its editor picking the region with the key typed kept; the custom provider a line at the foot,
gone while searching; the dialog growing out of the row clicked on a spring
and folding back into it on close, the focus back on the row it came from;
Duplicate and Add another opening a new editor with no sheet, cancelled back
to the list; the search's Escape emptying it and never hiding the tray; in
English and Chinese.

`own-signin-remove.test.cjs` opens the Kiro subscription's accounts with
kiro-cli's own sign-in behind one of magpie's, then alone and first: its
Remove is there both times, its title saying magpie only hides it and it
shows again when Kiro signs in anew, and it posts login/forget; magpie's
own account in use first has no Remove; in English and Chinese.

`saved-signedout-remove.test.cjs` shows Claude Code signed out with two
accounts saved in magpie: the Providers line naming them has Remove, whose
dialog lists them; Cancel and Escape post nothing, Remove posts
login/forget once per account and the line goes; in English and Chinese.

`codex-signout.test.cjs` opens Codex with one account (mamba on Discord: it
couldn't be removed while Codex was signed in to it): its row has Sign out,
asked in the app's own dialog; Cancel and Escape post nothing and go back to
the editor, Sign out posts login/forget and says so. With another account
saved the one in use has no Sign out but Remove, which posts login/forget
(vincentzhang1_55530 on Discord: magpie signs Codex in to another account
first), and a signed_in refusal is said in the reader's language. No select, no left border, the page doesn't move; in
English and Chinese.

`nokey-editor.test.cjs` opens two local Ollama providers saved with no key,
one from the preset and one custom, as /api/providers gives them: a click
on each row opens its editor (the key box saying one is optional) and
nothing throws, where the row only toggled. A plugin's provider (base
plugin://codearts) has no link in its editor's head, where it said
"codearts ↗" and opened https://codearts (Lemon on Discord), while the
Ollamas keep theirs; in Chromium and WebKit, English and Chinese.

`editor-short-window.test.cjs` opens a provider's editor with 40 models in a
short window (800x500, 600x420, 420x340; 悠悠哥 on Discord: a small window's
WorkBuddy editor stopped part way down its models): the dialog fits the
window with its head and Save in sight and uncovered, and the wheel held
over the model chips runs them to the last one and then takes the editor's
body on to its end, where the chips' held ends stopped it; the page never
moves. In Chromium and WebKit, English and Chinese.

`group-from-model-add.test.cjs` opens the Routing page on a new group made
from a model (?newgroup=, as "Make a routing group of it" does; 悠悠哥 on
Discord: "Hy4 preview" did nothing on Add in a small window). Each label
sits beside its field, where a hidden classifier's empty cell put Levels'
label at the right and its choices under the labels; the wheel reaches Add,
uncovered, and Add posts the group, where its draft had no fast members and
Add threw. A save the server refuses shows its error in sight, the editor
stays, and a second Add is posted, where the button stayed busy. At 600x420
and 1000x700, in Chromium and WebKit, English and Chinese.

`group-pick-filter.test.cjs` covers a group's Add a model picker and the
filter over the routing groups (#907; PAMI on Discord). A model in the group
with its reasoning fixed (a/two:max) is ticked, and a click takes that member
out instead of adding it a second time. A model that is in other groups names
them, and the whole note shows on hover. The filter keeps the groups whose
name, id or models match every word typed, and the keyboard stays in it.
When nothing matches it says so, and Escape clears it. The page never moves,
and there is no select or left border. In Chromium and WebKit, English and
Chinese.

`old-webkit.test.cjs` holds the page to Safari 15.0, the WebKit macOS 12 can
have (#220: a regex lookbehind in app.js, a syntax error before Safari 16.4,
left the panel with its headings and nothing working). Every script in
`assets/` is parsed (with the Babel parser Playwright bundles) and may have
no regex lookbehind, v flag or modifiers, class static block, decorator,
import attributes or `using`, in a literal or `new RegExp("…")`; no built-in
newer than 15.0 (`.at`, `findLast`, `structuredClone`, `Object.hasOwn`,
`toSorted`, Set methods, `Object.groupBy`, `Promise.withResolvers` …) unless
`compat.js`, loaded before the rest, fills it in; the styles no `:has()`, no
`:focus-visible` in a list with other selectors, no nesting or dvh units, and
container queries, `subgrid` columns and a `color-mix()` custom property
only with an `@supports not (…)` fallback in the same file. Then, in WebKit
with those built-ins deleted, the panel's tabs and the window's pages are
drawn with no page error. It cannot run an old engine, so syntax is judged
by the parse, not by running it.

`add-button.test.cjs` keeps the Providers page's "Add provider" at the
view's foot over a long list, at the top and at the end alike; one click
with the list scrolled to its end opens the sheet under the list and takes
the view down to it (in WebKit too); the button steps aside while the sheet's
head is in sight and, scrolled back up, takes the view to the sheet again; a
dialog opened and closed over the page keeps every logo it drew rather than
making them afresh; and the sheet closed by its Close, twice over, or by
Escape leaves no blank under the list, the focus back on "Add provider"
(#433 made it a dialog over the list for that blank; it unrolls under the
list again, the editor being the only layer over the page); in English and
Chinese.

`usage-ledger.test.cjs` opens the Usage page's Requests, a row per request
from a faked `/api/usage/requests`: the columns, the model asked for, the
provider and account, the model sent, "gpt-6-luna" amber by the one a vendor
answered with another model and plain on a dated name, a failure's red dot
and 429 and its agent, on another computer whose magpie passed it on, as
"Codex · via office-mac", the totals over them. Older and Newer page through 130 requests
with the pager held where it was; the agent, Failed and search filters and
the period ask the server again from the first page; Export CSV posts the
filters shown with no page and says where the file went. At the window's
narrowest (560) the table scrolls in its box and no tab scrolls the page
sideways; no left-border stripe; in English and Chinese, light and dark.

`usage-ledger.test.cjs` also follows a request from Usage to its routing
story and back to every usage row sharing that route ID. It checks archived
routes with no live gateway, cleared history, rows recorded before IDs were
kept, clearing the route filter and exporting it, in Chromium and WebKit,
English and Chinese.

`sessions-overview.test.cjs` checks the Usage page's Sessions overview: six
figures (sessions with their median and p90, tokens, cost, cache read, active
time, projects with the top one's share), the range as bars up to 100 days
and a calendar past them, counted in tokens, cost, sessions or active time
(active time off while a model is picked), the week by hour in the local
time zone, projects and models as bars that filter without moving the page,
and the top sessions opened in place from their files; nothing overflowing
at 1100 or 560 wide; in English and Chinese. It also checks the heatmap by
messages and output tokens, the session shape by messages, minutes and
autonomy (remembered, the page left where it was), tool use (the top tools,
their kinds and weeks) and the top skills with their last use, agents and
projects.

`sessions-bar-names.test.cjs` checks the Sessions overview's bar rows
(projects, models, tools and skills: a name, a bar, figures): at 1600 wide
every long name is shown whole, each card's bars start at one place and are
at least 5em, at 420 wide the long names are shortened while no bar is under
5em and nothing runs past its card, and wide again the names are whole; in
Chromium and WebKit, English and Chinese.

`usage-sessions-agents-fit.test.cjs` checks Usage › Sessions with 15 agents
(#929): at 860 and 560 wide the page, its main and the Usage view never
scroll sideways; the agent strip fits the window and scrolls in itself, and
the last agent, clicked at the strip's end, stays in view as the strip is
drawn again; in Chromium and WebKit, English and Chinese.

`usage-unpriced-names.test.cjs` checks Usage › Sessions where an agent
recorded tokens under no model name (tony on Discord: the cost's note read
"未计入：，没有已知价格"): the cost header's note, a session row's cost note
and its models name it "a model with no name", with no blank where a name
goes; in Chromium and WebKit, English and Chinese.

`usage-keys-fold.test.cjs` checks the Usage page's card of a provider with
many keys, each its balance (361 on Discord: an OpenRouter card listed every
key's): the first three are in sight, the rest behind "Show N more keys" at
its foot, the keys' sum in its head; the button opens them in place and
folds them again without moving the page, and is remembered across a
reload; DeepSeek's keys fold too, four keys aren't folded, a subscription's
accounts aren't keys, an unread key leaves the sum out, and at 440px nothing
is cut; in Chromium and WebKit, English and Chinese.

`upstream-status.test.cjs` checks the vendors' status pages on the
Providers and Usage pages (#971): with Anthropic's page saying its API is
down, the Anthropic provider's row and the Claude subscription's row and
usage card carry "API outage" beside the name, its title naming the part and
the incident; a click opens the incident and not the row; a relay serving
the same models, OpenAI (its page all well) and Moonshot (its page unread)
carry nothing, and nothing does once the page is well again; at 440px the
badge is whole (the name gives way) and nothing spills; in Chromium and
WebKit, English and Chinese.

`usage-accounts-fold.test.cjs` checks the Usage page's card of a subscription
with many accounts (Linx on X: twenty-odd on one plugin made a very long
card): of 22 WorkBuddy AI accounts the first five are in sight, the rest
behind "Show 17 more accounts" at its foot; of nine Trae accounts the one in
full and the one with the check-in switch stay in sight too; eight aren't
folded. The button opens them in place without moving the page, is
remembered across a reload and folds them again from the foot (the page
resting at its end, the button in sight); at 440px nothing is cut, folded or
open; in Chromium and WebKit, in English, Chinese, Japanese and German.

`sessions-calendar-fill.test.cjs` checks that the Sessions overview's activity
calendar fills its card: 118 days (17 weeks) at 1400 wide take over 90% of
it, the weeks before the range coming in as empty cells without tooltips, the
cells square and the month names over their weeks; at 1000 and 560 wide the
weeks before are fewer and nothing runs past the card, narrower still there
are none and the cells shrink, and wide again they come back; in Chromium and
WebKit, English and Chinese.

`usage-ledger-detail.test.cjs` opens the Requests tab's rows: a failure the
gateway logged shows its status and the vendor's error type in the row and,
opened, what the vendor said with the request's id and endpoint; a call read
from an agent's session file carries a "session log" mark, says "Succeeded" or
the error that ended it, and says in its details that the file records no
status; its models are the one asked for, the one sent as asked and the one that answered, its effort is there, and its time is about (≈) what the file's stamps tell. A click opens and closes a row without moving the page, Enter and Space
do the same, text selected in the details is not a click, and the rows stay open
when the list is asked for again; in English and Chinese.

`usage-ledger-chart.test.cjs` opens the Requests tab on what its requests add
up to: a strip of four totals (tokens, requests, cost, the cache hit rate),
then the trend of one metric — tokens, cost or requests — as columns by the
hour, each told apart by provider, agent or model and standing on the bottom
line in its hour's slot, beside a ranking of the same that is the chart's
legend. The pointer over a column shows what each had of it, over a ranked one
the others fade; a click on a provider or an agent in the ranking, or the
provider picker beside the agent's, lists only its requests while the ranking
keeps the others in sight; the metric and the split are remembered; a click
moves nothing. At 560 the ranking goes under the chart and the totals two to a
row, a wider window redraws it, a metric with no price says so, and with no
request listed there is nothing; in English and Chinese, light and dark.
The daily chart also checks selection, gray bars on other days, filtered rows
and CSV, an empty day, clearing a selection, keyboard input and resizing.

`usage-model-at-provider.test.cjs` splits the Requests tab's chart by "Model ·
provider" (inaction on Discord): one model at two providers is two rows of the
ranking, each named by both with the provider's icon and its own first token,
the faster first by Speed though fewer requests went to it. A click lists that
provider's requests of that model (provider and model both asked for), a click
again all of them, and the page doesn't move. English and Chinese.

`usage-chart-axis.test.cjs` checks the usage chart's side labels, in the
window's Requests tab (1180 and 560 wide) and the tray panel's Usage tab (440
and 340 wide): for tokens, cost in dollars and in yuan, and requests, small and
large ("8000 万", "125 亿", "¥2000"), each label starts no further out than the
card's padding, as far in as the tabs above it, ends before the plot, and
nothing runs past the card or scrolls the page sideways; Chromium and WebKit,
English and Chinese.

`number-units.test.cjs` checks Settings' Number units (John on Discord): in
Chinese the Requests tab says a large count in K / M / B ("1.54B", an axis's
"80M") by default, as English does (#740); picking 万 / 亿 saves chineseUnits,
moves nothing on the settings page, and the totals and chart axis then say
"15.4 亿" and "8000 万" while the page stays Chinese, after a reload and in the
tray panel too, each label still ending before the plot. Every other count
follows (#476): the Overview's tiles and chart peak, the Requests summary
line, the Sessions tab's figures, and the panel's Routing "today", which turns
back to K/M/B when the setting does. In English the row is hidden and counts are
K / M / B anyway; Chromium and WebKit.

`panel-usage.test.cjs` opens the tray panel's Usage tab: the totals, a small
chart and a ranking of five at most for today, seven or thirty days, the
metric switch, and a click on a provider (or the picker) that switches to it —
its models then tell the chart apart. A click on a control in sight leaves the
panel where it is, Open Usage takes the window to that provider's requests,
and the window opened so has the Requests tab with that provider and agent
picked and an address without them. Nothing is cut off at 320, where the
totals go two to a row; available allowances keep their tab; in English and Chinese.
Daily bars select that day's totals and ranking while keeping the whole chart;
refresh and metric changes keep the selection, a period change clears it, and
clicking the selected day clears it too, including days with no calls.

`usage-refresh.test.cjs` sets the Usage page's refresh period, with a clock in
place of time: every 5 s to begin with and no read before that, the picker's
five (off, 5 s, 10 s, 30 s, a minute), off reading no more, a minute reading at
its end and not at half of it, the button beside it reading at once, turning
while it does and saying when in its title, on Overview the summary and the
allowances too, asked for (`usage/quotas?asked=1`) as the allowances' own
Refresh did, with no second Refresh on the page and a title saying so (#486),
and the choice remembered by the next window; in English and Chinese.

`usage-ledger-content.test.cjs` opens the rows of the Requests tab on what was
said in them, read from the agent's session file when the row is opened: loading,
then the input and the output as parts (who said each, a tool's call with its
name), the reasoning and the agent's own context folded, a long part showing some
of itself and unrolling, what is left off a part or the whole said; a gateway
request is asked for by its session and the span it took, a call of a session file
at its own time; a request the files can't tell says why (no session, an agent whose
files aren't read, no such call); a row opened again, or the list read anew, asks no
more; the details fit the table's box at 560; in English and Chinese.

`shared-skills.test.cjs` opens the Library's Skills tab with skills found in
the user-wide `~/.agents/skills` (#227): one row for a skill there that
agents link or junction to, "shared in ~/.agents/skills/…" with those
agents' icons and no "differs in"; a link in the shared folder also saying
where it points; a copy of an agent's own still "differs in ZCode"; Bring in
saying it stays where it is and posting the name; in English and Chinese.

`library-toggle-status.test.cjs` opens the Library's Skills and MCP tabs
and clicks an agent's icon on a row (#332): turned on, the status says
"Written to DeepSeek Harness"; turned off again, "Removed from DeepSeek
Harness", and an agent that had it from the start turned off, "Removed from
Codex"; Chromium and WebKit, in English and Chinese, with `/api/plugins`
faked.

`import-all-skills.test.cjs` opens the Library's Skills tab with three skills
found in the agents: Bring in all beside "In your agents" posts every
name at once, the rows go and the toast says "3 skills are in the library
now", or names the one that couldn't be brought in, which stays listed; the
click scrolls nothing; in English and Chinese, with `/api/plugins` faked.

`every-skill.test.cjs` opens the Library's Skills tab with three skills and
three agents that take skills, and one that doesn't (#443): Turn all on
beside "In the library" posts the three agents with on, at once, and every
chip is lit; Turn all off asks in the page first (a browser dialog fails the
test), Cancel posts nothing, and its own button posts them with off and no
chip is lit; each button is greyed once there's nothing for it to do; the
clicks scroll nothing; Chromium and WebKit, in English and Chinese, with
`/api/plugins` faked.

`by-agent.test.cjs` opens the Library's Skills and MCP tabs with Copilot CLI
hidden on the Agents page but still given two skills and a server (#475): By
agent beside "In the library" opens a sheet of the agents that take them,
each with how many it has; the hidden one is tagged Hidden and has no Turn
all on, and its Turn all off posts it alone to `…/agents-all` with off and
leaves it listed with none; Codex's Turn all on posts Codex alone with on and
lights its chip on every row while Claude Code keeps what it had, and for
servers isn't offered again once the only one it can reach (not the SSE one)
is on; the clicks scroll nothing; Chromium and WebKit, in English and
Chinese, with `/api/plugins` faked.

`remove-all-skills.test.cjs` opens the Library's Skills tab with three
skills, two kept by magpie and one linked from a folder of the user's
(#449): Remove all beside Turn all on/off is red, and asks in the page first
(a browser dialog fails the test), naming the agents that lose them, the two
folders that go to magpie's backups and the one only unlinked; Cancel posts
nothing; its own button posts every skill's name to
`library/skills/remove-all` once, and no row is left; the clicks scroll
nothing; Chromium and WebKit, in English and Chinese, with `/api/plugins`
faked.

`header-centre.test.cjs` sweeps a Windows window from 1100 to 560 points at
text size 100, 110 and 125%, with an Update pill waiting (emo172, #442): the
header's tabs never touch the buttons; whenever they aren't in the row
they're in the middle of the window, and the closer-together tabs are tried
there before the row; in the row they have as much room either side, give or
take the header's padding against the gap before the buttons; Chromium and
WebKit, in English and Chinese, with `/api/plugins` faked.

`library-row-open-scroll.test.cjs` opens the Default set of Library →
Instructions, wheels down to the foot of the agents' list and clicks an
agent's row open and shut (emo172, #458): the Library draws its whole page
again on a click, and the row clicked still stays where it is on the screen,
under a long set in a short window and a shorter set in a tall window.
Chromium and WebKit, English and Chinese, with `/api/plugins` faked.

`header-zoom-fit.test.cjs` sweeps a Windows window from 1300 to 700 points at
text size 110 and 125%, with an Update pill waiting (emo172, #457): a wider
window is never drawn in more than a narrower one, and with room to spare the
header is as at 100%. Chromium zooms the page as the webview does (its default
zoom level), where the buttons' edge measures a hair past the header's
padding; WebKit, which has no page zoom to set, uses the device scale factor.
English and Chinese, with `/api/plugins` faked.

`strip-scrollbar.test.cjs` lays out an overflowing tab strip (`segs regions`,
the Sessions page's agents and a provider's regions) in Chromium with its
scrollbars shown (ARNO on Discord): off a Mac it scrolls under the app's
6px thumb, not Windows' classic bar; on a Mac the system's bar is left.

`own-skill.test.cjs` opens the Library with Claude Code's own impeccable in
the library skill's way (StringKe, JasonLeeForOnly on Discord): the warning
row says so in the reader's language and has "Use the library's" and "Keep
Claude Code's", which post `skills/use-library` and `skills/keep-own` with
the skill and the agent; the warning goes, the toast says what was done, and
the click scrolls nothing; no coloured stripe down the card or the row. A
found skill with a byte copy in another agent shows both agents' icons and
no "differs in"; one that really differs still says so. Chromium and
WebKit, in English and Chinese, with `/api/plugins` faked.

`model-pick.test.cjs` picks Claude Code's model with the Agents page
scrolled while a faked `/api/set` takes 2.5s to answer: the row shows the
new model at once, in the window and in the tray panel's opened row and its
line, the page left where it was; the answer keeps it and says so; a refused
pick puts the old model back with the reason; in English and Chinese.

`claude-direct.test.cjs` shows Claude Code with `"model": "sonnet"` in its
settings.json: the row reads the alias as the model it stands for, with
Claude's logo; its tooltip says a model Claude Code asks Anthropic for
itself isn't through magpie, so settings.json names no magpie endpoint, and
picking one says "straight to Anthropic"; a magpie model says neither, and
no pick moves the scrolled page. Chromium and WebKit, in English and
Chinese.

`omarchy.test.cjs` holds Omarchy's look to Omarchy: with no Omarchy theme in
boot.js the page has no omarchy class or theme style, shows the Appearance
choices and no Bar icon row, and asks nothing of /api/omarchy; with one, the
theme's background, square corners and its name in place of the choices, and
Settings → Bar icon, whose On and Off each post and move nothing, hidden when
the app says Omarchy's bar isn't there; in English and Chinese.

`brand.test.cjs` holds the header's logo and name: shown in `magpie web`
on Windows, macOS and Linux alike, beside the tabs; hidden in the Windows
window, whose title bar has them; shown in the Mac window.

`model-test-one.test.cjs` tests one model on its own from a provider's
editor, in Chromium and WebKit, English and Chinese: a model chip's
right-click opens "Test this model", which posts provider/test with that
model alone; its dot and title show the answer, a second model's test keeps
the first's, and the footer names the model. The right-click neither picks
the chip nor moves the page, Esc closes the menu only, and Test models still
asks every model. The API is faked.

`model-rate.test.cjs` shows a model's credit rate by it (01huadalang on
Discord: pick the cheap Qoder or WorkBuddy models without opening their
apps), in Chromium and WebKit, English and Chinese: the provider editor's
chips carry a small grey badge, Qoder's 0.5× or WorkBuddy's 0.03×, a
discount's price before it struck through (0.5× 0.2×; a free model's FREE
then 0.1× struck through), its title saying what a request costs; a model
with none has no badge. The badge is the context badge's grey, not FREE's
green, and no stripe. A click on it picks the chip and moves nothing. An
agent's model picker shows the same badges. The API is faked.

`provider-detect-models.test.cjs` asks which API serves which model, in
Chromium and WebKit, English and Chinese: Detect APIs' "Each picked model"
posts provider/detect with every model picked (detectModels) and shows model
by API, each a ✓ with the time or a ✗ with why; "Use these" sets the URLs
that answered and gives each model that answered on one API only that API in
Names & levels, unsaved, sending nothing until the Save carries them as
modelPrefs, while a model answering on two stays Auto. A chip's right-click
Test says the API it was asked on ("answered via Responses"). No click moves
the page, nothing has a left-border accent, and every string has its
Chinese. The API is faked.

`provider-add-models.test.cjs` picks a provider's models from its vendor's
list while it is added (#578: 添加供应商的时候，希望添加可以获取全模型的按钮),
in Chromium and WebKit, English and Chinese: the custom provider's add form
has Fetch models beside its Models field, which asks nothing without a URL,
then posts provider/list with the URL and key typed and no id; a vendor
refusing says why in the form; the list comes as chips, a model's name with
its id in the title, filtered when long; a chip ticked is in the field and
an id typed ticks its chip; Pick those shown, Pick none (one typed by hand
stays) and Pick all; the Add sends the models picked. No click moves the
page, nothing has a left-border accent, and every string has its Chinese.
The API is faked.

`whats-new.test.cjs` shows what changed after an update (a Discord user:
to see whether their issue was fixed): after an upgrade the window opens a
dialog with every release's notes since the version last run, newest first,
once (the app is told they were seen, and a reload shows nothing); a fresh
install and the tray's panel show nothing, and magpie web's page shows it as
the window does. Headings, bullets, bold and code are drawn, #464 links the
issue and, like a markdown link, opens in the browser through the app (in a
tab in magpie web) without moving the page; a javascript: link stays text and
HTML in the notes is shown as text, never run. Settings' What's new row (under
Version) opens them again, the waiting update's notes first, marked Not installed
yet. The dialog shown by itself has "Don't show again today" (#525), told
to the app (whatsnew/today) as it is ticked or unticked, without moving the
page; Settings' notes have no such box. Whether it shows at all after a
restart to update is the app's (TestWhatsNewQuietAfterRestartToUpdate). No
left-border accent, every string in Chinese. Chromium and WebKit,
English and Chinese, API faked.

`sessions-manage.test.cjs` opens the Sessions page (TJHHHH on Discord): an
agent's sessions by the folder they ran in, the latest folder open, each with
its resume command; unfolding a folder, opening a row and picking a session
leave the page where it is. Delete asks in magpie's own dialog (a browser
`confirm()` fails the test), Cancel sends nothing, Delete posts the ids
picked to sessions/delete, a session still being written to is said so, the
Trash lists what went and Restore posts its key; an agent whose sessions
magpie can't delete (OpenCode) shows no delete. No left-border accent, every
string in Chinese. Chromium and WebKit, English and Chinese, API faked.

`sessions-talk.test.cjs` shows a session's conversation and carries a Pi
session on in oh-my-pi (sxwedo, #845). Show conversation in a session's
details reads sessions/transcript by agent and id, once, and lists the
user's words, the model's, each tool call by name and its result, thinking
folded; an agent magpie can't read (OpenCode) has no such button. Continue
in is the app's menu, not a `<select>`: copy posts the `omp --fork` command,
the terminal posts agent, id and `in: "omp"`; the details say the command.
No click moves the page, no left-border accent, every string in Chinese,
Japanese and German. Chromium and WebKit, English and Chinese, API faked.

`sessions-usage-open.test.cjs` keeps one list of sessions (#752): each
Sessions page row shows the tokens, cost and models Usage → Sessions showed,
its details what each model spent and where magpie routed it, and its filter
finds a model. Usage's latest sessions keep their numbers but no Resume; a
row there opens the session on the Sessions page (its agent, its folder
unfolded, the row opened and brought into sight by that click, with no
scroll refused), another agent's switches the page to it, and All sessions
opens the page. At 420px a row still fits with its title readable. English
and Chinese, WebKit (Chromium too where it launches), API faked.

`sess-agent-strip.test.cjs` keeps Usage → Sessions' agent filter inside the
window (#929): with eleven agents in range the strip is wider than an 866px
window, and it scrolls in itself instead of the page scrolling sideways, the
way the regions strip and Connect's five APIs do; its first and last option
are both reachable and both choose their agent, the model and folder picks
beside it stay visible and open, and in a window wider than the strip nothing
scrolls and the options keep their own widths. One agent in range still
leaves the filter hidden, and the other strips on the page and elsewhere keep
their behaviour. No left-border accent. English and Chinese, Chromium and
WebKit, API faked.

`sessions-purge.test.cjs` erases sessions in magpie's trash for good (#487):
a trashed row's Delete forever and the Trash's Empty trash each ask in
magpie's own dialog (a browser `confirm()` fails the test), Cancel sends
nothing and leaves the page where it is, the confirm posts sessions/purge
with the row's key or `all`, the list empties, and the note says magpie
never erases them by itself. No left-border accent, every string in Chinese.
Chromium and WebKit, English and Chinese, API faked.

`sessions-folder-delete.test.cjs` deletes every session of project folders
at once (#527): each folder's row starts with a box, as each session's does,
that picks every session of the folder shown, folded or not; the bar counts
sessions, not folders, a session's own box part-ticks its folder's, and a
filter picks only what it shows. The bar's Delete asks in magpie's own
dialog (a browser `confirm()` fails the test), naming the folder and its
path for one whole folder and how many folders for several; Cancel posts
nothing; the confirm posts sessions/delete with their ids, so they go to
magpie's trash as single deletes do; a session still being written to is
left and said so, other folders stay, and an agent magpie can't delete from
has no boxes. No click moves the page, no left-border accent, every string
in Chinese. Chromium and WebKit, English and Chinese, API faked.

`sessions-codex-provider.test.cjs` moves Codex sessions made with another
provider to the one Codex uses now (#887): the Codex tab says how many there
are and which provider, tags each, and Move opens magpie's own dialog with
each ticked; Cancel posts nothing, an unticked one isn't moved, Move posts
sessions/codex-provider with each id and Codex's provider, and Undo posts
each back to the provider it had. No click moves the page, no left-border
accent, every string in Chinese, Japanese and German. Chromium and WebKit,
English and Chinese, API faked.

`sessions-toolbar.test.cjs` uses ten agents to check that fitting tabs stay
visible at 1800px, while 900, 660 and 320px windows use a compact agent menu.
Resizing keeps the current agent; search and Trash fit, the menu fits the
narrow window, arrow keys choose, Escape returns focus, and an outside click
closes it. Choosing an agent leaves Trash and keeps keyboard focus through
loading; 150% CSS zoom switches to the menu and back. Light and dark themes,
English and Chinese, Chromium and WebKit, API faked. Run it with:

```sh
node --test internal/gui/tests/sessions-toolbar.test.cjs
```

`routing-wb-refused.test.cjs` opens a request WorkBuddy refused "from an
unapproved channel" (Codex's system prompt, #182) on the Routing page: the
vendor's words are given without the hint the gateway adds, and the hint is
on its own line in English and Chinese; another 400 gets none, and picking
the request leaves the page where it is. Chromium and WebKit, API faked.

`routing-blocked.test.cjs` opens a request a vendor's edge firewall blocked
(Alibaba Cloud's 405 page in front of zcode.z.ai) on the Routing page: what
the gateway made of it is given without the hint it adds, and the hint (the
firewall blocked this IP; wait, or switch network or proxy) is on its own
line in English and Chinese; another 405 gets none, and picking the request
leaves the page where it is. Chromium and WebKit, API faked.

`routing-zcode-blocked.test.cjs` opens a request ZCode's Start Plan turned
away (#425: 405 "request has been blocked due to unusual activity") on the
Routing page: what the gateway made of it is given without the hint it adds,
and the hint (ZCode still turned it away though magpie sends it as the ZCode
app does: the network blocked this IP, or ZCode checks something new; use a
GLM Coding Plan or another provider in the group) is on its own line in English and Chinese;
another 405 gets none, and picking the request leaves the page where it is.
Chromium and WebKit, API faked.

`routing-shape.test.cjs` opens a group request whose first member answered
422 because its API couldn't read the request (#350: SuperGrok and Codex's
additional_tools item) and whose next member answered: the Routing page says
it went on to the next and that the first doesn't rest, tagged "request not
understood", in English and Chinese, never "an error another account
wouldn't fix"; picking the request doesn't move the page. The API is faked.

`routing-proxy.test.cjs` opens a group request whose first member's proxy
wasn't listening (#381: "proxyconnect tcp: … 127.0.0.1:1082: connection
refused") and whose next member answered: the Routing page says the request
never reached the vendor, went on to the next and that the first doesn't
rest, tagged "proxy not reachable", in English and Chinese, never "an error
another account wouldn't fix"; picking the request doesn't move the page.
Chromium and WebKit, API faked.

`s3-sync.test.cjs` sets up sync to an S3 bucket from Settings (#296). It
works as follows, in English and Chinese:

- WebDAV is picked first. Picking S3 shows the fields for the endpoint,
  bucket, prefix, region, access key, secret and path-style, each label
  fitting its column. Switching between WebDAV and S3 leaves the page where
  it was, and what was typed for WebDAV is still there after switching back.
- With no bucket, the form says so and posts nothing.
- Save posts `s3://bucket/prefix`, the access key as the user and the
  secret as the password, together with the endpoint, region and path-style.
  The row then reads "S3 sync" with the bucket and server.
- Edit opens the form with S3 picked, marked "S3 · on", and the fields as
  saved. The secret field is empty, with "saved" as its placeholder.

`sync-other-kind.test.cjs` checks that moving sync between WebDAV and S3
keeps the other's settings (ARNO on Discord: trying S3 wiped the WebDAV
address, user and password). With S3 synced to and a WebDAV server kept,
the row ends "WebDAV settings kept", the form marks S3 "on" and says the
WebDAV settings are kept; picking WebDAV, which leaves the page where it
was, shows the kept address and user, the password as saved, says saving
moves sync there and the button reads "Move sync to WebDAV". Saving posts
that server with the password left empty for the one kept, and the row
then reads "WebDAV sync", "S3 settings kept". English and Chinese,
Chromium and WebKit, API faked. Before the fix the WebDAV fields were
empty and nothing said which was on or kept.

`webdav-settings-refresh.test.cjs` checks the Settings page's sync row
against a setup changed behind the window (#305: magpie webdav / magpie s3
at the terminal): the faked `/api/davsync` answers differently between
reads, and the window's focus — or the page becoming visible again — must
re-read it, the row, its status and the form's ticks following what the CLI
left (a WebDAV setup with agents and the library out, then an S3 one) rather
than what the page first read. A focus while a form is open never rebuilds
it: what was typed stays (#104). Off, the description is built from the
view's toggles and names only what is turned on — providers without their
API keys, no agents' models — as the CLI's list does; a view that says
nothing of the toggles reads as the whole lot. English and Chinese,
Chromium and WebKit. Before the fix, the row kept the first view it had
read and the description was the hardcoded whole list.

`market-have.test.cjs` checks that the Library's Discover cards follow
the library without the window being focused again (#300). Removing a
server (Remove from the library) turns its card from "✓ Added" back to
Add at once and asks the market again; a server added by hand with the
same name is marked added; a skill removed turns its card back to Add. No
window focus or visibility change happens during the test, the click does
not scroll the page, and the checks run in English and Chinese.

`sessions-today.test.cjs` checks that today's bar in Usage › Sessions › By
day keeps its tooltip while the sessions are read again (#309). The fake API
has 30 days up to today (Asia/Shanghai), and today's tokens grow with each
read. The last bar is today, and its title gives today's usage. With the
pointer on it and the page clock run past the 15-second reread, the same bar
element is still there and hovered, its title shows the new total, the
chart still has 30 bars and the page has not scrolled. A metric picked after
that draws the latest numbers. The checks run in English and Chinese.

`provider-remove.test.cjs` checks Remove in a provider's editor. If the
delete fails after the provider is already gone (an agent's file could not
be rewritten), one click closes the editor, the provider's row disappears,
the other rows stay, and the status line shows the error; no second Remove
is needed. If the delete succeeds but leaves an agent on a model magpie no
longer serves, the status line warns about it and has no left-border
accent. If the delete fails while the provider is still there, the editor
stays open and shows the error. The checks run in English and Chinese, in
Chromium and WebKit.

`signin-link.test.cjs` checks a sign-in that is waiting (Factory's device
flow). Its link appears on one line inside the box and is selected whole
with a click, with no left-border accent. The copy button beside it
(titled Copy link) posts the whole link to /api/copy, and the status line
says it was copied. Open again is still there, and nothing scrolls. The
checks run in English and Chinese, in Chromium and WebKit.

`copy-command.test.cjs` copies from a tab with no clipboard API, as
`magpie web` on a NAS is when opened from another computer over http:
/api/copy refuses and navigator.clipboard is missing, so the copy command
copies the sign-in link, the status line says it was copied, the textarea
it used is gone, and nothing scrolls. English and Chinese, in Chromium and
WebKit.

`signin-close.test.cjs` puts away a sign-in for another account left
unfinished (#526). In Qoder's editor at the issue's window size, Add another
→ Sign in anyway waits with Qoder's long device link: the box and every
button in it stay inside the account list (the link is cut short, Cancel in
sight), and Cancel is the one button that puts it away: Open again stands
alone, with no Close beside it doing the same. Cancel posts
signin/<id>/cancel, stops the polling and brings Add another back without
moving the page, and so do the editor's Cancel and Save, so the editor
opened again has no box. English and Chinese, Chromium and WebKit;
the API is faked.

`signin-paste-codex.test.cjs` checks a ChatGPT sign-in finished from its
pasted address, for a magpie the browser can't reach (on a server, in
Docker). While the sign-in waits, the box says to copy the address of the
page that won't load, and a Callback URL field sits inside it with no
left-border accent. Finish sign-in stays off until something is typed, then
posts the address to the sign-in's callback route once, and the account is
named in the status line. Nothing scrolls. The checks run in English and
Chinese, in Chromium and WebKit.

`signin-paste-key.test.cjs` checks Command Code signed in to from a browser
that can't reach magpie (on a server, in Docker), where Command Code's page
posts its key to magpie unseen and leaves no address to paste. While the
sign-in waits, the box says to make an API key on Command Code's keys page,
a link opens that page, and a hidden API key field sits inside the box with
no left-border accent; Finish sign-in posts the key once to the sign-in's
callback route. A plugin's browser sign-in offers "Use an API key instead"
while it waits: the click cancels it and asks the plugin's API key way.
Nothing scrolls. The checks run in English and Chinese, in Chromium and
WebKit.

`tray-inuse.test.cjs` checks Settings, Allowances in the menu bar. A
subscription with two accounts lists "Account in use" before each account;
one with a single account does not. Ticking it saves `claude|*`, the pill
names the subscription, and reopening the menu shows it ticked. No click
moves the page. The checks run in English and Chinese, in Chromium and
WebKit.

`codex-auto-reset.test.cjs` checks the Auto-use toggle beside a Codex
account's resets, on the Usage page's card and on the menu bar panel's. It is
off until turned on; a click posts `settings/codex-auto-reset` for that
account, shows it pressed and says so, and a second click turns it off. A GLM
team's resets get no toggle. No click moves the page, and nothing has a
left-border accent. The checks run in English and Chinese, in Chromium and
WebKit.

`library-all.test.cjs` opens the Library with an All chip ahead of each
server's and skill's agent chips (Discord: MCP 服务器 SKILL 都得一个个选):
one click posts the agents that can take it in one write — not Pi for a
server (no MCP file), not Codex or Droid for an SSE server (no SSE), a
hidden agent keeping what it has — the chips light, the toast says it is on
for all of them or, when one couldn't take it, "on for 1 of 2 agents" and
which and why; a second click takes it from all; the click scrolls nothing
and doesn't open the row. Chromium and WebKit, in English and Chinese.

`library-own-folder.test.cjs` (#595) shows a skill put in the library's own
folder by hand among the skills found, said to be "in the library's folder,
not listed" with its path, and Bring in saying it is listed where it is; a
library skill kept in ~/.agents/skills has Codex's and Gemini CLI's chips
lit and disabled, each saying it reads that folder itself, and a click on
Pi's chip or on All never sends them; Remove says a linked skill's folder
stays and the library's own is moved to the backups. No click scrolls the
page, and nothing has a left-border accent. In English and Chinese.

`routing-auto-reset.test.cjs` checks the Routing page's story for a Codex
reset used by itself: a try out of its week says whose reset was used and
that the request was asked again, and Codex's own sign-in answering after
one says so before it answered; English and Chinese, Chromium and WebKit.

`claude-risk.test.cjs` checks starting a Claude sign-in first says Anthropic
may ban the account, as Command Code's does, and asks nothing of the backend
until "Sign in anyway"; the click moves nothing, no left-border accent.
English and Chinese, Chromium and WebKit.

`claude-usage-asked.test.cjs` checks a Claude account's usage is only asked
for when the reader opens Usage or presses Refresh (the header's, the only
one since #486): opening the page and Refresh load `usage/quotas?asked=1`, the timed reload and the window coming
back don't; an asked load isn't swallowed by one already on its way.
Chromium and WebKit.
The same test covers cached Claude snapshots on Usage, the tray's Allowances
tab and the provider's account editor: historical percentages stay dated
and visibly say when an expired window's current allowance is unknown;
past reset times say they have passed. A fresh reading clears those markers.
The tray's cached subscription date is as short as a balance card's, while
expired-window warnings remain visible and the tooltip keeps the explanation.
English and Chinese, Chromium and WebKit.
`panel-effort.test.cjs` opens a row in the tray panel whose effort is not one
of the levels offered (omp at auto, an agent with none set): the slider shows
it as it is ("auto", "default"; 自动 in Chinese) at a stop of its own, a touch
there posts nothing — it had shown the lowest level and a touch wrote it —
and the next stop is the lowest level. A level past the ones offered (max,
the levels going to high) stands after high, both ends reading "low … max"
(低 … 最高) rather than "max … high", and a touch on it posts nothing. The
effort icon and the panel's bars light as far as the slider's stop goes: max
past high all of them, medium between low and high two of three. auto, none
and the default light no bar, where minimal lights one, and none is no level
to count: Hermes at low, second of five, lights one of three. Chromium and
WebKit.

`claude-effort.test.cjs` checks Claude Code's effort and ultracode by its
model (#352, #353, #354): Opus 5.5's row has an ultracode square, and Opus
4.5's and Haiku 4.5's rows don't; Haiku 4.5 has no effort control. With the
page scrolled, a click on the square posts `ultracode` `on`, lights it and
says that an open Claude Code session must restart. A second click posts it
off. Neither click moves the page. In the tray panel, Opus 4.5's slider ends
at high, and a step posts that level and says the same restart. Chromium and
WebKit, in English and Chinese, with `/api/plugins` faked.

`remote-magpie.test.cjs` adds a Remote magpie (another computer's magpie,
shared on its network) from the Add sheet: its tile, the address field with
its placeholder and a hint on where the other magpie shows its address and
key; saving without an address says the other magpie's address is needed,
and saving posts the `remote-magpie` preset with the key and the address as
typed. OpenAI's editor has no address field. No click moves the page;
English and Chinese, Chromium and WebKit.

`qoder-cn-tile.test.cjs` checks Qoder CN (#349): the Add sheet has a Qoder CN
tile beside Qoder's, which is labelled the international site's (qoder.com);
each tile's title says which accounts it is for, Qoder CN's sign-in warning
says it takes qoder.cn accounts (Alibaba Cloud or phone number) above Qoder's
risk note, nothing is asked of the backend until "Sign in anyway", which asks
for `qoder-cn`, and the click moves nothing; English and Chinese, Chromium and
WebKit.

`library-loading.test.cjs` holds the Library while it loads: the real tabs,
the last one open picked, its rows in outline that come in only after a
moment; a tab picked while waiting shows its own outline and stays picked;
the Library folder waits. When the library comes the outline goes, the tabs
get their counts and don't move.

`provider-typed.test.cjs` tries a key pasted over a provider's saved one
before a Save: Refresh, Test models, a model's own test and the endpoints'
Test send the editor's form with `typed: true`, the editor stays open with
the key still typed, nothing is saved; a new key's box focuses the key and
its name says it is optional. In English and Chinese, Chromium and WebKit.

`phone-web.test.cjs` opens magpie web on an iPhone 13: the magpie and the
icons over the tabs, which take a row of their own at a size to read and tap
and slide to keep the one open (Plugins) in sight, nothing wider than the
screen, iOS kept from zooming into a field; no setting's name cut short. A
flick's scrolls after the finger lifts stay the reader's (they were put back,
the page jerking), while a tap's click is no flick. A desktop browser and the
app's window keep the one-row header. Chromium and WebKit.

`usage-alert.test.cjs` sets the usage alerts (#368) on the Settings page:
"Allowance alert" and "Low balance alert" start Off; On saves 80% (and 5 for a
balance), the field beside it saves the number typed and puts back a share
past 100 unsaved, and a setting saved later (the currency) keeps both
alerts. With magpie's notifications turned off in the system, both rows say
so. No click moves the page and no row has a coloured left border; English
and Chinese, Chromium and WebKit, with `/api/plugins` faked.

`quota-families.test.cjs` checks Antigravity's allowance one figure a model
family (01huadalang on Discord: several accounts, every level of every
model, read as bloat): windows that name a `family` show as one a family,
Gemini and Claude first, each the most used of its models, on the Usage
page's card, the menu bar panel's rings and the provider's account rows;
each family's tooltip lists its models level by level, and "Every model"
turns an account's card to each window and back without moving the page.
Claude Code's windows, naming no family, are as they were. No left-border
accent; English and Chinese, Chromium and WebKit, with `/api/plugins` faked.

`provider-search.test.cjs` checks a custom provider's "Searches the web by
itself" (#359): the editor's Web search row opens as saved, ticked for one
saved as searching, and Save posts `searches` ticked or not. A relay with
only a Chat address has no such row; it comes once an Anthropic URL is
typed under More endpoints and goes with it, and Save then posts
`searches: false`. The row says the relay can be named as the searcher but
is not picked automatically. A signed-in account (Codex) has no such row.
Every string has its Chinese; English and Chinese, Chromium and WebKit.

`lan-docker.test.cjs` checks Share on local network in a container (Discord:
magpie in Docker on a NAS showed the container's own 172.17.x address).
Opened over the network from `magpie web`, the address is the host the page
was opened at, on the gateway's port, with an "In a container" row saying so;
the OpenAI/Anthropic switch keeps that host and doesn't move the page. Opened
at localhost, the container's address stays and the row says it is the
container's own and to set `MAGPIE_PUBLIC_URL`. Outside a container nothing
changes. No coloured left border; English and Chinese, Chromium and WebKit,
with `/api/plugins` faked.

`routing-time.test.cjs` checks a routing group's rule for hours of the day
(Discord: send a provider's peak-price hours, DeepSeek's or GLM's, to
another). The group's rule and the trace read it as its hours and days
(22:00–08:00 Mon–Fri; 周一–周五 22:00–08:00); in the editor the hours are two
times typed into one pill, a click on it types the first and moves nothing,
past midnight is noted, a time that isn't one is marked once left, the days
are picked like the agents (weekdays, then Saturday too: Mon–Sat), and the
rule is saved as `time: {from, to, days}`. English and Chinese, Chromium and
WebKit, with `/api/plugins` faked.

`unlisted-models.test.cjs` checks a provider set to "Only through routing
groups" (只通过路由分组使用; 悠悠哥 on Discord: hy4 vanished from the pickers).
The model picker's filter, finding such a model, says why it isn't offered:
hy3, in the group Mine, is reached by picking Mine ("Pick Mine" sets it);
hy4, in no group, can't be used, and "Make a routing group of it" opens the
Routing page on a new group of it, added with `members: ["hunyuan/hy4"]`.
The provider's editor names its models in no group under the tick (not once
unticked), each a click from a group; `?view=routing&newgroup=` opens the
window on one, and the tray panel asks `window/main` for it. The page doesn't
move, no left-border accent; English and Chinese, Chromium and WebKit.

`request-archive.test.cjs` turns the request archive on and off from its
switch at the top right of the Usage page's request list (Jorben on Discord):
off unless turned on, refused with sync not set up with an s3:// bucket (the
reason in the footer, the switch left off), on with one, the bucket in its
tooltip, and a failed upload said beside it. The Gateway page has no switch
any more, and its recent calls, even one the archive kept, show their own
bodies with no "Fetch from archive" under them. No click moves the page; no
left-border accent. English and Chinese, Chromium and WebKit, with
`/api/plugins` faked.

`request-archive-usage.test.cjs` opens the request archive from the Usage
page's Requests (jorben, #447): a request the archive kept names it in its
row's details, with "Fetch from archive" and "Download"; fetched, a body past
256 KB is said by its size and a note to download it, and an archive too large
to read on the page says so. Download in the app posts `archive/export` and
says where the file was saved; in magpie web it follows a download link to
`archive/file`. A request with no copy, with the archive on, says "Not
archived". No click moves the page; no left-border accent; every new string
has its Chinese. English and Chinese, app and web, Chromium and WebKit.

`providers-off-fold.test.cjs` checks that providers switched off are kept
apart (01huadalang on Discord): the Providers list holds the ones on, and the
ones off sit below it under a "Turned off (N)" fold (已关闭的供应商), folded at
first. Opening it with the page scrolled leaves the head and the page where
they are, the fold is remembered across a reload, and a provider switched
off moves under it. English and Chinese, Chromium and WebKit, with
`/api/plugins` faked.

`account-return.test.cjs` checks the accounts of Codex after magpie signed
it in to another because the first was all but used up (#408: 智能路由会自动把
所使用账号设为首选). The account it stands in on says "First for now" (暂为首选),
its title naming the one it goes back to, and that one "First again once it
has room" (额度恢复后切回首选); with nothing to go back to, the account Codex
is on is just "First". The Routing note says magpie moves back. The page
doesn't move; English and Chinese, Chromium and WebKit.

`keep-login.test.cjs` checks the tick under Codex's accounts that keeps
Codex signed in (#524: 始终登录首选账号), and the pick beside it that keeps
it signed in to an account of the user's choosing rather than the first
(Koohoko on #524: 首选只是为了按顺序先消耗它的额度). Picking one posts
provider/keeplogin with keepLoginAs; the order holds, the first saying First
and the chosen one Signed in (已登录), Make first on it posting
provider/arrange rather than a sign-in; picking "the first account" goes
back. The Routing note follows each, In order says magpie moves Codex on
once used up (#530), and one account has no tick. The page doesn't move;
English and Chinese, Chromium and WebKit.

`signin-again.test.cjs` checks a sign-in to an account magpie lists
already (#413: WorkBuddy's page offers the account WorkBuddy is signed in
to). Adding another WorkBuddy account comes back done with `again`: the
status says it is already listed and its sign-in renewed (该账号已在列表中，
已更新登录), not "added"; a new account still says added. The page doesn't
move; English and Chinese, Chromium and WebKit.

`web-page.test.cjs` checks the page `magpie web` serves to a browser (Jorben
on Discord: no request archive switch and no usage chart icons in the remote
web UI, and a 404 for `/wails/runtime.js`). With `web` set the page never
asks for `/wails/runtime.js`, which the browser has none of, and nothing
fails to load; the app's window still loads it. The request archive's switch
over the Usage page's requests is there and posts `settings/archive` without
moving the page, and the Usage page's chart draws its columns with each provider's icon
in its ranking. English and Chinese, Chromium and WebKit, with the API and
`/api/plugins` faked. Go's `TestPageFilesRevalidate` checks that the page's
files go out with `Cache-Control: no-cache` and an ETag of their content, so
a cache in front of `magpie web` can't keep an older version's `app.js`.

`balance-parts.test.cjs` checks a custom provider's balance card (#420): a
balance field with several amounts shows each on a line of its own, the
user's label quiet and the first amount the larger, "Balance" only where no
label was given; a percent is a meter, amber from 90%. Each card says when it
was read ("Updated 3 minutes ago", 3分钟前更新), or "As of … — couldn't be
read just now" (截至 …，暂时无法获取最新额度) when it stands in for a reading
that failed, windows' cards too; the menu bar panel's Balances show the same
amounts, the meter and "As of" (截至). One amount reads as it did. No
left-border accent, nothing runs out of a card; English and Chinese, Chromium
and WebKit, `/api/plugins` faked.

`search-api.test.cjs` checks Settings' Web search section (#419: a search API
of one's own for when no provider can search). It lists the search APIs in
the order they are tried and has a row to add one: picking SearXNG asks for
its address, Add posts `{vendor, key, url}`, an API that needs a key sends
nothing without one, a key magpie refuses is said in the row with the API
and what was typed kept, and Remove posts `{vendor, remove: true}`. No click
moves the page. English and Chinese, Chromium and WebKit, with `/api/plugins`
faked.

`searcher-pick.test.cjs` checks the Web search section's "Searches for other
models" row (01huadalang on Discord: pick which provider searches for a model
that can't). With none named it shows Automatic and magpie's own pick; its
picker offers Automatic first, each provider that can search by its small
model, and each of its models, including a relay said to search; the row says
those relays require manual selection and are not automatic fallback choices.
Costs and fallback are explained for every search provider; providers left out
are described as unavailable in the current configuration, with both other
search providers and configured search APIs named as alternatives.
A model picked is saved as `searcher:
"<provider>/<model>"`, a provider by its small model as `searcher:
"<provider>"`, and shown; another setting saved still sends the pick; one
named that magpie can't use (turned off) is said in the row with magpie's
pick shown instead; Automatic sends `searcher: ""`. No click moves the page.
The picker is checked in English and Chinese; the help also in Japanese and
German, at 560 and 1000 pixels in light and dark themes, including a selected
relay and empty relay/unavailable lists. Desktop text keeps its single-line
ellipsis and full hover title; the existing narrow-web layout is unchanged.
Chromium and WebKit, with the API faked.
Set `ARTIFACT_DIR` to retain screenshots of the help.

`provider-file-error.test.cjs` checks a providers.json that is there but
can't be read (#415's review): the Providers page says over the list that the
file can't be read (无法读取供应商配置文件), was left unchanged and is to be
fixed or moved aside, with magpie's reason; the Add sheet doesn't open as for
a first use, a signed-in account still listed stays under it, and a file that
reads shows none of it. English and Chinese, Chromium and WebKit, with
`/api/plugins` faked.

`public-url.test.cjs` checks the address MAGPIE_PUBLIC_URL puts beside this
computer's on the Gateway page's Connect section, and what its hint says
about the gateway: loopback, open to the network (listening beyond loopback
with nothing shared), and a network address that needs an enabled gateway
key. English and Chinese, Chromium and WebKit, with `/api/plugins` faked.

`provider-picks-stay.test.cjs` checks that the models picked in one
provider's editor never go to another's (#464): a Refresh or a Forget whose
list comes back after the editor was left for another provider's leaves that
editor with its own picks only, none shown as added by hand, and its own
Refresh and Forget keep them. In the editor they were pressed in, a pick not
yet saved and an id typed in that the vendor doesn't list (added by hand)
outlive both, and Save sends them. No click moves the page. English and
Chinese, Chromium and WebKit, with `/api/plugins` faked.

`quota-curve.test.cjs` checks the quota cards' curves (#651): a line a
window under the meters, broken at a reset, a dashed even burn and a now
line, the 2 days / Cycle control turning every card without moving the page,
no curve without readings, the theme's chart colours in light and dark, and
the tray card's thin current-cycle line, in Chinese and English.

`balance-curve.test.cjs` checks a key's balance over time on the Usage page:
its readings as a line, the least-squares line since the last top-up dashed
on to zero, "Runs out in 3d at this pace" and the spend a day, a click (or
Enter) enlarging the plot and another taking it back without moving the
page, no curve for a key of one reading, and the allowances' Off taking it
away, in Chinese and English, light and dark.

With Node.js and Playwright available, `make test-ui` manually runs the whole suite:
every `internal/gui/tests/*.test.cjs`, discovered automatically. Files are
independent and run a few at a time (`UI_TEST_CONCURRENCY`, default 2); set
`UI_TEST_CONCURRENCY=1` to run one at a time when diagnosing a flaky file.

```sh
make test-ui
```

If Playwright is installed outside the repository, set `NODE_PATH` to the
directory containing its package. The suite uses Playwright's Chromium and
WebKit binaries (`playwright install chromium webkit`). No frontend dependency
is needed by the app itself. Tested with Playwright 1.62.1.

For the callback test, `PLAYWRIGHT_CHANNEL=chrome` uses an installed Chrome
instead of Playwright's Chromium.

Set `BROWSER=chromium` or `BROWSER=webkit` for one engine. Set `ARTIFACT_DIR` to
an external directory to retain screenshots and Playwright traces, including
failed assertions. These browser checks run through `make test-ui`, separately
from `make test` (which is Go only) and from the UI preview workflow (which
only records screenshots).
The Test workflow does not run this suite.

`usage-stale-answer.test.cjs` and `providers-fetching-retry.test.cjs` use Node's
VM and need no browser. Set `MAGPIE_APP_JS` to another revision's `app.js` to
run their behavior checks against that source.

The request-ledger review regressions also cover the explicit data-source notes
(including the exclusion of local rejections), the local-session label, and native
panel fitting including Usage (with the desktop's 560px maximum). The detail
and content tests, along with ledger pagination and filters, bring controls into sight through real wheel input before a
click, so WebKit's wheel steps and the app's scroll protection do not fight
Playwright's automatic scrolling. Empty allowances hide their panel tab.

`session-identity.test.cjs` checks historical emails and recorded official vendors
on session-log rows. Models and current configuration do not establish historical
routes; session rows without routing evidence display "Local session", including
with a known session creator. Unknown gateway providers retain their own label.
Creator emails and recorded provider IDs appear separately in request details.
Third-party OpenCode gateway calls retain the actual relay. These regressions
run in Chromium and WebKit, English and Chinese.

`otel.test.cjs` checks OTLP export and metrics start off, endpoint and masked
headers save in English and Chinese, environment overrides are indicated, and
other preference saves retain OTLP choices. It also checks all three content modes,
reload persistence, and readable consent and full-width fields in narrow windows.
It runs in Chromium and WebKit
with the real assets and an isolated API fixture:

```sh
node --test internal/gui/tests/otel.test.cjs
```

`account-arrange.test.cjs` drags a subscription's accounts and a provider's
keys by their rows (no handle) in every routing mode, and by Alt+↑/↓: the
first row, its First mark and the order saved for routing agree, one save
goes out per drop, Escape and a failed save put the rows back, a disabled
account is not made first, and account text stays selectable. It runs in
Chromium and WebKit with an isolated API fixture:

```sh
node --test internal/gui/tests/account-arrange.test.cjs
```

`routing-account-order.test.cjs` checks that Routing's live stage seats each
provider's accounts and keys in the order its page lists them and a drag sets
— the order the gateway tries them — not by name nor as one request weighed
them (#217): one request through a group of a subscription, a provider's keys
and a plugin's accounts, weighed least used first, reads in each list's order;
after the subscription's accounts are moved with Alt+↓ in its editor and the
plugin's rearranged, the stage, shown again without a reload or a new request,
reads in the new orders. It runs in Chromium and WebKit, in English and
Chinese:

```sh
node --test internal/gui/tests/routing-account-order.test.cjs internal/gui/tests/update-auto.test.cjs
```

`seg-contrast.test.cjs` checks the header's view switch and the text buttons
against WCAG AA (#477): with the window light, dark, and dark by the system's
choice, it reads the colours the browser computed, each background laid over
the ones under it, and asserts that the switch's other views on its track, the
view shown on its thumb, and a text button on the page, a card and the inset
editor read at 4.5:1 with margin (4.7), and that the thumb stands off the
track by more than it did (1.2:1 light, 1.5:1 dark). It runs in Chromium and
WebKit, in English and Chinese:

```sh
node --test internal/gui/tests/seg-contrast.test.cjs
```

`agent-grip.test.cjs` checks an agent row's grip: with the pointer away no
row in the Agents list draws it (always there it was clutter, the owner:
一直出现太丑了), the row under the pointer draws it in its left margin, beside
its logo, and it goes again when the pointer leaves; the logo stays the logo
on hover (no second grip over it); dragging the grip
moves the row, and clicking it opens the row's menu, whose Hide puts the row in the fold
at the foot of the list. Opened, the fold's Show button is there without the
pointer on its row, and brings the row back; no click moves the page. It runs
in the window and the tray panel, in Chromium and WebKit, in English and
Chinese:

```sh
node --test internal/gui/tests/agent-grip.test.cjs

`account-models.test.cjs` keeps an account of a subscription, and a key of a
provider, for some of the provider's models only (#474): an account with a
list of its own has a badge saying how many, always shown, and one without
says All models on hover; a click opens the provider's models under the row
as chips, Save waits for one picked and posts `provider/accountmodels` with
the account (or the key's id) and the models picked, All models posts none,
and the badge then says what it has. A click moves no row and scrolls
nothing; a subscription with one account has no badge; no border stripes,
every string has its Chinese. It runs in Chromium and WebKit, in English and
node --test internal/gui/tests/account-models.test.cjs

`favorite-star.test.cjs` checks a model's favourite star in the model
picker (#482): not a favourite, an outline star on hover; a favourite, a
filled star shown always, with its title and aria-pressed to match. In the
window and the tray panel, light and dark, English and Chinese, Chromium and
WebKit:
node --test internal/gui/tests/favorite-star.test.cjs

`claude-picker-fold.test.cjs` checks Claude Code's model picker (#496): the
models magpie serves on the very account Claude Code is signed in to fold
into one row after Claude Code's own, though the account comes after the
providers the user added (Claude Code headed once); a click or Enter opens
and closes it in place, the page not moving and the keys left to the
filter (the arrows, Esc); the value set is a row of its own; a query shows
every row it finds, so an id typed in full is picked with Enter; an alias
and its dated id are one row, the dated one's while it is the value set or
starred where the alias isn't. Chromium and WebKit, English and Chinese,
the API faked:
node --test internal/gui/tests/claude-picker-fold.test.cjs

`list-sort.test.cjs` checks the installed lists' order, the reader's pick
(#481): the Library's MCP servers and the installed plugins by name A→Z (the
plugins no longer in the order they were installed) or Z→A, and the
Library's skills by source, as before, or as one flat list by name A→Z or
Z→A; each list keeps its own pick through a reload, and a pick leaves its
control where it was on the screen. It runs in Chromium and WebKit, in
English and Chinese:
node --test internal/gui/tests/list-sort.test.cjs

`quota-pools.test.cjs` checks Antigravity's allowance a row a pool of
models, each with its 5-hour and its weekly window (a user on Discord: the
three models read the same, show the 5 hours and the week left): windows
that name a `pool` show as the pool's 5 hours then its 7 days, Gemini
first, on the Usage page's card (the pool's models in the tooltip), the
menu bar panel's four rings (Gemini 5h, Gemini 7d, Claude 5h, Claude 7d,
each label whole) and the provider's account rows (a pool's name, then its
5h and 7d meters); "Every model" lists the models' own windows, not the
pools' again, and back, without moving the page. Claude Code's windows are
as they were. No left-border accent; English and Chinese, Chromium and
WebKit, with `/api/plugins` faked:
node --test internal/gui/tests/quota-pools.test.cjs

`plugin-progressive.test.cjs` checks the Plugins page is drawn in parts
(#488) while `/api/plugins/market` never answers: Installed from
`/api/plugins` at once (no skeleton, no update chip yet), Discover's cards
from `/api/plugins/listings` with their buttons waiting on npm, and once
`/api/plugins/npm` answers (held until then) the downloads, the version,
Install and the installed plugin's "v… out". It runs in Chromium and
WebKit, in English and Chinese:
node --test internal/gui/tests/plugin-progressive.test.cjs

`lightweight.test.cjs` checks Settings → General's Lightweight mode row
(#580): off by default, On and Off saved as `lightweight` true and false
without the page moving, another setting's save keeping it, no left-border
accent, and no such row on a browser's page (`web`). English and Chinese,
Chromium and WebKit, with `/api/settings` faked:
node --test internal/gui/tests/lightweight.test.cjs
```

## Mobile Web

`mobile-web.test.cjs` checks all eight web pages and their navigation in
Chromium and WebKit, English and Chinese, at touch widths 360/390/430/820.
Agent fixtures include model/effort controls, Codex's subagent/sign-in squares
and omp's three role squares. Every phone field must stay inside its row, model
names must be readable, and each picker must open; desktop comparisons use
those same populated rows.
It checks that Library's folder button is reachable, Usage tokens, costs and
stat explanations are readable, and chart dates do not overlap. A released
touch's continuous scroll can outlast the input window, while scripts cannot
move an idle page, including after momentum stops. This uses Settings → Allowances
at 390×600, where the real settings rows leave room to scroll. The API is faked;
no user configuration is read or changed.

At desktop widths 900/1280, screenshots are compared with the same pages from
`origin/main`. Dimensions must match; a pixel counts as different only when
any RGBA channel differs by more than 48, and more than 300 such pixels fails.
This tolerates Chromium's small antialiasing differences while still detecting
layout changes. Fetch that branch before running; set `BASE_REF` to another
local Git ref (`HEAD` for a self-comparison) to select a particular baseline.
PNG decoding uses the copy bundled with Playwright, without another dependency.
The empty allowance fixture completes after usage totals render, so both pages
compare the same completed API state rather than a loading-order difference.

```sh
node --test internal/gui/tests/mobile-web.test.cjs internal/gui/tests/old-webkit.test.cjs
```

## Routing request metrics

`routing-request-metrics.test.cjs` checks request-row cache hit rates (cache
reads divided by uncached input, cache reads and cache writes across billable
tries) and output speeds (reply output over the time after its first token).
It covers known zero hits, missing historical tiers, unstreamed and burst
replies, retries, unfinished and failed requests, the cache-rate description
in the story, weighted session cache rates and decode speeds (with coverage
in tooltips), folds, history, live updates, narrow layouts and persisted
metric choices. Missing values are omitted from request rows and session
headings; known zero cache rates and costs remain visible. The first-token
label matches the metric picker and the Chinese routing story.
Request and session token totals have tooltips with exact uncached input,
output, cache-read and cache-write counts. Billable retries are included and
explained; missing historical tiers and partial session coverage are explicit.
The six choices apply immediately while the menu stays open, with focus
kept on the checkbox. Mouse and keyboard changes work in both directions,
including when none are selected.
It runs in English and Chinese on Chromium and WebKit. Set `ARTIFACT_DIR`
to keep screenshots. APIs are isolated fixtures.

```sh
node --test internal/gui/tests/routing-request-metrics.test.cjs
```

`routing-columns.test.cjs` checks that side-by-side requests get more room
than accounts, whose column stays between 280 and 400 CSS pixels. It checks
aligned list bottoms, account model labels, and no horizontal overflow at
940, 1100, 1440 and 1920 pixels, then the full-width stack at 700 pixels.

```sh
node --test internal/gui/tests/routing-columns.test.cjs
```

## Automatic Codex title grouping

`routing-sessions.test.cjs` checks automatic grouping after a Codex title write,
conflict revocation, costs and folds, plus name refresh on large history days.
It uses isolated API fixtures in English and Chinese on Chromium and WebKit.

```sh
node --test internal/gui/tests/routing-sessions.test.cjs
```

## Names on an agent's settings

`agent-field-names.test.cjs` checks that every setting in a connected agent's
opened row is named, in the window and in the tray panel, in English and
Chinese. A setting nobody has picked is the agent's own logo on the shut row,
for want of room, so Aside's four task roles and its picture model all read
"default" under one logo there; the panel already names them as its rows open
and the window now does the same. It also checks that no name is cut short by
its column (Aside's `standard`, Claude Code's `thinking`, in the panel's 44px).
It uses Settings → Agents at 1240×900 and the panel at 440×720. The API is
faked; no user configuration is read or changed.

```sh
node --test internal/gui/tests/agent-field-names.test.cjs
```

`import-row.test.cjs` checks the row of an app magpie is added to by a link
of its own (Cindy) on the Agents page: a line under its name says whether
magpie is added (green once it is), its button stands where the other rows'
model picker does, as wide and lined up with it at 960 and 700px, and a click
opens the app's link. English and Chinese, Chromium and WebKit.

`privacy-hides-accounts.test.cjs` checks that Privacy's Mask personal data
hides the accounts on screen too (inaction on Discord): until Hide accounts is
chosen on a computer it follows the setting, so Usage's email address is
blurred and its button pressed; an address shown on purpose with the button
stays shown; on Settings › Privacy, turning Mask personal data on turns Hide
accounts on, and Privacy's own Hide accounts row says so and turns it off,
without scrolling. English and Chinese, Chromium and WebKit.

`cli-path.test.cjs` checks Settings › About › Command line (PAMI on Discord):
it says what `magpie` in a new terminal runs, with a dot for each shell (no
left border, no native select); Add to PATH posts `/api/cli` with no shell and
the row turns green, a shell whose PATH lacks the folder has a button naming
its profile that posts that shell alone, and no click scrolls. On Windows it
lists PowerShell and cmd with no profile buttons, and a program not named
magpie.exe (the site's portable download, #942) is said to get a magpie.cmd
that runs it, in Add's tooltip, the row and the status; a translocated app gets no
button. Every string has zh, ja and de. English and Chinese, Chromium and
WebKit; the API is faked, no shell profile is read or written.

```sh
node --test internal/gui/tests/cli-path.test.cjs
```

`update-mirror.test.cjs` checks Settings › About › Download source (#893):
the row says updates come from GitHub; Use a mirror… opens an address field,
an http:// address is refused with the reason in red and the draft kept, an
https:// one saved with Enter posts `/api/settings/update-mirror` and is
shown, and Use GitHub posts an empty mirror. A download that failed through
the mirror says so on the Version row and offers Download from GitHub, which
posts the empty mirror then `/api/update/install`. No native select, no left
border, no click scrolls. English and Chinese, Chromium and WebKit; the API
is faked.

```sh
node --test internal/gui/tests/update-mirror.test.cjs
```

`lane-limit.test.cjs` checks each key's and account's own limit on requests
at once (#892): its row's pill says what runs and waits under it while
requests are out ("1/1 running · 2 queued"), read again from `/api/lanes`
every two seconds and written into the same pill, else its limit (its own or
the provider's); a row with none shows it on hover only. A click opens the
app's menu (no native select, no scroll): a number, the provider's (null) or
Other… typed in place, each posted to `provider/accountconcurrency`. The
editor's Queue size and Queue wait sit under Concurrency, as narrow, labels
on one line, saved with it and refused out of range. No left border; every
string has zh, ja and de. English and Chinese, Chromium and WebKit.

```sh
node --test internal/gui/tests/lane-limit.test.cjs
```

`ledger-protocol.test.cjs` checks the protocol each request was sent
upstream in, on Usage › Requests (蓝猫 on Discord): a row translated from
its agent's protocol is tagged "Chat → Anthropic", one sent as it came
"Anthropic" or "Responses", one read from a session file nothing. A
request's details say Protocol and the upstream's own stop reason, when it
gave one. A click opens the details and moves nothing; no left border;
every string has zh, ja and de. English and Chinese, Chromium and WebKit.

```sh
node --test internal/gui/tests/ledger-protocol.test.cjs
```

`library-rtk-nohook.test.cjs` checks that an agent RTK has no hook for
(DeepSeek Harness) is listed in Library › RTK with a No RTK hook tag saying
why, and that its switch can't be turned on, while Pi's can. English and
Chinese, Chromium and WebKit.

```sh
node --test internal/gui/tests/library-rtk-nohook.test.cjs
```

## Local Provider Discovery

`provider-discovery.test.cjs` checks the automatic Providers-page hint on
Chromium and WebKit: discovery runs without blocking the list or resetting an
Add-sheet search, details are read only when the picker opens, and only selected
entries are imported. It covers refresh after partial and complete imports,
late scan responses, a prominent first-use hint and a compact entry beside Add
provider for existing users, a later visit finding new configurations,
persistent dismissal across reloads and partial imports, newly added or changed
configurations without selecting previously ignored entries, manual import of
ignored entries, a scan containing only an ID collision or a disabled Alma
provider (no hint, but both remain selectable in manual import), an empty scan,
a failed scan, and a narrow window. The main flow runs in English and
Chinese; all credentials and API responses are fixtures. Run with
`node --test internal/gui/tests/provider-discovery.test.cjs` and the Playwright
environment described in this file. `TestLocalProviderDiscovery` exercises the real
readers and summary endpoint in an isolated home, including duplicate and
unimportable entries, unreadable sources, stable fingerprints despite ID
collisions, changed configuration fingerprints, excluding unticked collision and
disabled Alma entries while preserving the add-key case, and keeping credentials
server-side.
