# Dropdown browser regression

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
node --test --test-concurrency=1 internal/gui/tests/gateway-caller-keys.test.cjs internal/gui/tests/gateway-connect-keys.test.cjs internal/gui/tests/api-key-usage.test.cjs internal/gui/tests/api-key-theme.test.cjs
```

## Other Browser Regressions

`routing-sessions.test.cjs` checks the Routing request list in Chromium and
WebKit, English and Chinese: the list defaults to By request and remembers
the grouping choice across reloads; sessions are separated by agent and ID across
model changes, known and partial costs are summed, zero partial estimates
retain their amount and +, single-request headings use the singular, zero prices stay known,
title helpers merge only into their explicit parent (even arriving first),
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

`click-scroll.test.cjs` holds the rule that a click never moves the page
(see "where the reader is" in `app.js`): on the Routing page, Live and a day
picked in turn with the list scrolled to its end; a click whose handler sets
scrollTop, grows the page above, redraws itself, or scrolls from a timer or a
shared helper; a control under what it unrolls (`data-unrolls`) going down
with it; a click that asks to go somewhere with `scrollOnPurpose(e)`;
the room kept at the foot going as the reader scrolls back; and the wheel.

`agent-layout.test.cjs` keeps the main window's agent names readable at 520,
560 and 600 CSS pixels, while the model and effort controls stay inside their
rows. At 601, the default 660 and 960 pixels, controls remain aligned beside
the names. English and Chinese, Chromium and WebKit.

`signin-callback.test.cjs` checks a sign-in's pasted callback (the field a
sign-in with pasteCallback shows) in a narrow Chinese
dark window: invalid input remains editable, retry reaches the callback route,
and a pending or accepted submission cannot be submitted twice.

`plugin-updates.test.cjs` checks the dot on Plugins while a plugin's update
waits for the reader, gone once it's updated, and the "Auto-updated" chip on a
plugin magpie updated by itself.

`plugin-signin.test.cjs` checks a provider an OpenCode plugin signs in to:
the add sheet lists it under "From plugins"; its sign-in asks the way, the
method's questions (a pick, then a text the plugin checks), then takes the
code the browser page shows; an API-key way opens the account. English and
Chinese, Chromium and WebKit, with the API faked.

`plugin-move-overflow.test.cjs` checks a Plugins card that offers a
built-in's accounts ("Move my 1 Grok (SuperGrok) account"): the button has a
row of its own at the card's foot, so it stays inside the card and the
plugin's name and "magpie community" aren't cut, in a three-, two- and
one-column window. English and Chinese, Chromium and WebKit, with the API
faked.

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

`routing-kind.test.cjs` lists calls Codex makes for itself (a guardian review,
a title, memories, a turn on Luna Reserve, a kind it does not know yet): each
has a grey tag by its model in the Requests list, in English and Chinese, the
model keeping its room first, and the request's story says what it was.

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

`text-size.test.cjs` checks the Settings page's Text size row (100, 110,
125 and 150%): a pick posts to /api/settings/text-size without scrolling
the page, stays picked after a reload (boot.js carries it, and the page's
`--zoom` has it before the first paint), and a save of the other settings
leaves it be; Ctrl/Cmd + = and − step through the sizes and 0 goes back to
100%, in the window and the tray panel, the other modifier doing nothing.
The zoom is the webview's, so here it is as a browser zooms: the smallest
window at 150% (840×630 points) is 560×420 CSS pixels at a device scale of
1.5, and no view (nor the panel at 150%) runs off to the side, the Mac
header still 50 points tall for the traffic lights. English and Chinese,
light and dark; ARTIFACT_DIR gets screenshots at 100% and 150%.

`settings-groups.test.cjs` puts the Settings page's warm-ups and check-in
under a tab per service (#124): Codex, Claude Code and WorkBuddy tabs where a
heading would be, after Preferences, before Local network, Codex's picked to
begin with and each showing its rows alone, named without the service; the
last warm-up and today's check-in still on the short lines, no left-border
stripe; a daily warm-up's time field only while it is on; each control
posting the setting it did and a tab posting nothing, with the page scrolled
and left where it was; the arrows, Home and End along the tabs; a tab picked
at the page's very end (a shorter card, then a taller one) leaving the tabs
where they were; the tab remembered across a reload, Codex's shown when the
remembered WorkBuddy one is gone; the WorkBuddy tab only with an account
signed in; in English and Chinese.

`update-check.test.cjs` checks the version row: the button stays, dimmed,
through a check, a second click asks nothing, and the answer puts it back.
A read still out cannot draw "checking" over that answer. A row drawn again
while the check runs keeps the button and catches up when the check answers.
A failed check gives the button back, and a check already under way shows
the button without starting another. The page stays where it was. English
and Chinese.

`sync-update.test.cjs` checks the header's refresh button looks for a newer
magpie too: one click, one check, and the Update pill shows what it found.
Its tooltip says so. English and Chinese.

`update-hide.test.cjs` keeps the header's Update pill away when asked (Wang
Hsiaohi on Discord): its ×, shown with the pointer over the pill, hides it for
that version, through a reload, until a newer one is out; a right-click does
the same. Settings → Update button says which version it is hidden for, with
Show again, and turns it off for good; the version row still offers the
restart. No click moves the page. Panel and window, English and Chinese.

`add-sheet.test.cjs` opens the Providers page's add sheet as quiet rows:
Subscriptions, Vendors, Relays and On this machine, each with its word on
what it is; rows with no border, no second line and no overflow, three to a
line at 900px; an added provider not faded but marked by a small green dot
after its name, a grey count only past one account (the Claude subscription's
2); short names with the full name, plans and host in the title; a vendor's
global and China presets one row with no tag, the regions' hosts in its
title, its editor picking the region with the key typed kept; the custom provider a line at the foot,
gone while searching; the dialog growing out of the row clicked on a spring
and folding back into it on close; in English and Chinese.

`own-signin-remove.test.cjs` opens the Kiro subscription's accounts with
kiro-cli's own sign-in behind one of magpie's, then alone and first: its
Remove is there both times, its title saying magpie only hides it and it
shows again when Kiro signs in anew, and it posts login/forget; magpie's
own account in use first has no Remove; in English and Chinese.

`saved-signedout-remove.test.cjs` shows Claude Code signed out with two
accounts saved in magpie: the Providers line naming them has Remove, whose
dialog lists them; Cancel and Escape post nothing, Remove posts
login/forget once per account and the line goes; in English and Chinese.

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
with the list scrolled to its end opens the sheet and takes the view down to
it (in WebKit too); the button steps aside while the sheet's head is in
sight and, scrolled back up, takes the view to the sheet again; and a dialog
opened and closed over the page keeps every logo it drew rather than making
them afresh; in English and Chinese.

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

`usage-chart-axis.test.cjs` checks the usage chart's side labels, in the
window's Requests tab (1180 and 560 wide) and the tray panel's Usage tab (440
and 340 wide): for tokens, cost in dollars and in yuan, and requests, small and
large ("8000 万", "125 亿", "¥2000"), each label starts no further out than the
card's padding, as far in as the tabs above it, ends before the plot, and
nothing runs past the card or scrolls the page sideways; Chromium and WebKit,
English and Chinese.

`number-units.test.cjs` checks Settings' Number units (John on Discord): in
Chinese the Requests tab says a large count in 万 and 亿 ("15.4 亿", an axis's
"8000 万") by default; picking K / M / B saves westernUnits, moves nothing on
the settings page, and the totals and chart axis then say "1.54B" and "80M"
while the page stays Chinese, after a reload and in the tray panel too, each
label still ending before the plot. In English the row is hidden and counts are
K / M / B anyway; Chromium and WebKit.

`panel-usage.test.cjs` opens the tray panel's Usage tab: the totals, a small
chart and a ranking of five at most for today, seven or thirty days, the
metric switch, and a click on a provider (or the picker) that switches to it —
its models then tell the chart apart. A click on a control in sight leaves the
panel where it is, Open Usage takes the window to that provider's requests,
and the window opened so has the Requests tab with that provider and agent
picked and an address without them. Nothing is cut off at 320, where the
totals go two to a row; available allowances keep their tab; in English and Chinese.

`usage-refresh.test.cjs` sets the Usage page's refresh period, with a clock in
place of time: every 5 s to begin with and no read before that, the picker's
five (off, 5 s, 10 s, 30 s, a minute), off reading no more, a minute reading at
its end and not at half of it, the button beside it reading at once, turning
while it does and saying when in its title, on Overview the summary and the
allowances too, and the choice remembered by the next window; in English and
Chinese.

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

`sessions-manage.test.cjs` opens the Sessions page (TJHHHH on Discord): an
agent's sessions by the folder they ran in, the latest folder open, each with
its resume command; unfolding a folder, opening a row and picking a session
leave the page where it is. Delete asks in magpie's own dialog (a browser
`confirm()` fails the test), Cancel sends nothing, Delete posts the ids
picked to sessions/delete, a session still being written to is said so, the
Trash lists what went and Restore posts its key; an agent whose sessions
magpie can't delete (OpenCode) shows no delete. No left-border accent, every
string in Chinese. Chromium and WebKit, English and Chinese, API faked.

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
and the hint (ZCode takes only its own app's requests and magpie doesn't
pretend to be it, or the network blocked this IP; use a GLM Coding Plan or
another provider in the group) is on its own line in English and Chinese;
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

`tray-inuse.test.cjs` checks Settings, Usage in the menu bar. A
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

`routing-auto-reset.test.cjs` checks the Routing page's story for a Codex
reset used by itself: a try out of its week says whose reset was used and
that the request was asked again, and Codex's own sign-in answering after
one says so before it answered; English and Chinese, Chromium and WebKit.

`claude-risk.test.cjs` checks starting a Claude sign-in first says Anthropic
may ban the account, as Command Code's does, and asks nothing of the backend
until "Sign in anyway"; the click moves nothing, no left-border accent.
English and Chinese, Chromium and WebKit.

`claude-usage-asked.test.cjs` checks a Claude account's usage is only asked
for when the reader opens Usage or presses Refresh: opening the page and
Refresh load `usage/quotas?asked=1`, the timed reload and the window coming
back don't; an asked load isn't swallowed by one already on its way.
Chromium and WebKit.
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

`usage-alert.test.cjs` sets the usage alerts (#368) on the Settings page:
"Usage alert" and "Low balance alert" start Off; On saves 80% (and 5 for a
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
`searches: false`. A signed-in account (Codex) has no such row. Every
string has its Chinese; English and Chinese, Chromium and WebKit.

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

`request-archive.test.cjs` turns the Gateway page's request archive on and
off: off unless turned on, refused with sync not set up with an s3:// bucket
(the reason in the footer, the switch left off), on with one, the bucket said.
A call the archive kept shows its date and id under its bodies; "Fetch from
archive" reads `archive?date=&id=` and shows the request's and response's
headers and bodies with secrets taken out, a call not uploaded yet says so
and can be asked again, and a call it didn't keep has nothing of it. No click
moves the page; no left-border accent. English and Chinese, Chromium and
WebKit, with `/api/plugins` faked.

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
fails to load; the app's window still loads it. The Gateway page's request
archive switch is there and posts `settings/archive` without moving the
page, and the Usage page's chart draws its columns with each provider's icon
in its ranking. English and Chinese, Chromium and WebKit, with the API and
`/api/plugins` faked. Go's `TestPageFilesRevalidate` checks that the page's
files go out with `Cache-Control: no-cache` and an ETag of their content, so
a cache in front of `magpie web` can't keep an older version's `app.js`.

`balance-parts.test.cjs` checks a custom provider's balance card (#420): a
balance field with several amounts shows each on a line of its own, the
user's label quiet and the first amount the larger, "Balance" only where no
label was given; a percent is a meter, amber from 90%. Each card says when it
was read ("Updated 3 minutes ago", 3分钟前更新), or "As of … — couldn't be
read just now" (截至 …，暂时无法获取最新用量) when it stands in for a reading
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

With Node.js and Playwright available:

```sh
node --test internal/gui/tests/menu-scroll.test.cjs internal/gui/tests/click-scroll.test.cjs internal/gui/tests/panel-fold.test.cjs internal/gui/tests/rollup-short.test.cjs internal/gui/tests/panel-routing.test.cjs internal/gui/tests/gateway-fold.test.cjs internal/gui/tests/routing-kind.test.cjs internal/gui/tests/balance-fix.test.cjs internal/gui/tests/cli-update.test.cjs internal/gui/tests/login-import.test.cjs internal/gui/tests/agy-launch.test.cjs internal/gui/tests/currency.test.cjs internal/gui/tests/usage-ledger.test.cjs internal/gui/tests/update-check.test.cjs internal/gui/tests/omarchy.test.cjs internal/gui/tests/brand.test.cjs internal/gui/tests/library-loading.test.cjs internal/gui/tests/provider-typed.test.cjs node --test internal/gui/tests/signin-callback.test.cjs internal/gui/tests/routing-steady.test.cjs internal/gui/tests/old-webkit.test.cjs internal/gui/tests/session-terminal.test.cjs internal/gui/tests/text-size.test.cjs internal/gui/tests/zcode-site.test.cjs internal/gui/tests/rail-tip.test.cjs internal/gui/tests/sync-update.test.cjs internal/gui/tests/plugin-signin.test.cjs internal/gui/tests/plugin-market.test.cjs internal/gui/tests/s3-sync.test.cjs internal/gui/tests/routing-wb-refused.test.cjs internal/gui/tests/routing-blocked.test.cjs internal/gui/tests/routing-zcode-blocked.test.cjs internal/gui/tests/routing-shape.test.cjs internal/gui/tests/market-have.test.cjs internal/gui/tests/sessions-today.test.cjs internal/gui/tests/routing-side-calls.test.cjs internal/gui/tests/import-all-skills.test.cjs internal/gui/tests/routing-flood.test.cjs internal/gui/tests/routing-busy.test.cjs internal/gui/tests/own-skill.test.cjs internal/gui/tests/routing-manual.test.cjs internal/gui/tests/tray-usages.test.cjs internal/gui/tests/provider-remove.test.cjs internal/gui/tests/signin-link.test.cjs internal/gui/tests/tray-inuse.test.cjs internal/gui/tests/codex-auto-reset.test.cjs internal/gui/tests/routing-auto-reset.test.cjs internal/gui/tests/claude-usage-asked.test.cjs internal/gui/tests/claude-risk.test.cjs internal/gui/tests/library-toggle-status.test.cjs internal/gui/tests/library-all.test.cjs internal/gui/tests/routing-reroute-title.test.cjs internal/gui/tests/plain-names.test.cjs internal/gui/tests/panel-effort.test.cjs internal/gui/tests/agent-models-all-hidden.test.cjs internal/gui/tests/omp-roles.test.cjs internal/gui/tests/claude-effort.test.cjs internal/gui/tests/remote-magpie.test.cjs internal/gui/tests/qoder-cn-tile.test.cjs internal/gui/tests/provider-levels-scroll.test.cjs internal/gui/tests/signin-paste-codex.test.cjs internal/gui/tests/signin-paste-key.test.cjs internal/gui/tests/usage-alert.test.cjs internal/gui/tests/quota-families.test.cjs internal/gui/tests/provider-search.test.cjs internal/gui/tests/webdav-settings-refresh.test.cjs internal/gui/tests/lan-docker.test.cjs internal/gui/tests/plugin-key-hint.test.cjs internal/gui/tests/update-hide.test.cjs internal/gui/tests/tray-logos.test.cjs internal/gui/tests/routing-proxy.test.cjs internal/gui/tests/sse-body.test.cjs internal/gui/tests/remote-group-served.test.cjs internal/gui/tests/plugin-move-overflow.test.cjs internal/gui/tests/routing-time.test.cjs internal/gui/tests/unlisted-models.test.cjs internal/gui/tests/plugin-updates.test.cjs internal/gui/tests/request-archive.test.cjs internal/gui/tests/group-member-fast.test.cjs internal/gui/tests/providers-off-fold.test.cjs internal/gui/tests/account-return.test.cjs internal/gui/tests/usage-ledger-detail.test.cjs internal/gui/tests/usage-ledger-chart.test.cjs internal/gui/tests/usage-chart-axis.test.cjs internal/gui/tests/panel-usage.test.cjs internal/gui/tests/usage-refresh.test.cjs internal/gui/tests/usage-ledger-content.test.cjs internal/gui/tests/claude-direct.test.cjs internal/gui/tests/signin-again.test.cjs internal/gui/tests/sessions-calendar-fill.test.cjs internal/gui/tests/editor-short-window.test.cjs internal/gui/tests/group-from-model-add.test.cjs internal/gui/tests/sync-other-kind.test.cjs internal/gui/tests/web-page.test.cjs internal/gui/tests/balance-parts.test.cjs internal/gui/tests/search-api.test.cjs internal/gui/tests/provider-file-error.test.cjs internal/gui/tests/sessions-manage.test.cjs internal/gui/tests/number-units.test.cjs internal/gui/tests/sessions-bar-names.test.cjs internal/gui/tests/model-suffix-own.test.cjs internal/gui/tests/routing-effort-change.test.cjs internal/gui/tests/every-skill.test.cjs internal/gui/tests/header-centre.test.cjs internal/gui/tests/public-url.test.cjs internal/gui/tests/strip-scrollbar.test.cjs internal/gui/tests/remove-all-skills.test.cjs internal/gui/tests/request-archive-usage.test.cjs internal/gui/tests/agent-layout.test.cjs internal/gui/tests/group-found-off.test.cjs internal/gui/tests/provider-picks-stay.test.cjs
```

If Playwright is installed outside the repository, set `NODE_PATH` to the
directory containing its package. The suite uses Playwright's Chromium and
WebKit binaries (`playwright install chromium webkit`). No frontend dependency
is needed by the app itself. Tested with Playwright 1.62.1.

For the callback test, `PLAYWRIGHT_CHANNEL=chrome` uses an installed Chrome
instead of Playwright's Chromium.

Set `BROWSER=chromium` or `BROWSER=webkit` for one engine. Set `ARTIFACT_DIR` to
an external directory to retain screenshots and Playwright traces, including
failed assertions. These browser checks run separately from `make test`.

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
other preference saves retain OTLP choices. It runs in Chromium and WebKit
with the real assets and an isolated API fixture:

```sh
node --test internal/gui/tests/otel.test.cjs
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
move an idle page, including after momentum stops. The API is faked; no user
configuration is read or changed.

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
