# Dropdown browser regression

`session-terminal.test.cjs` checks the macOS Settings choice for installed
`.command` handlers in English and Chinese. The system default appears once
and is selected at first. It selects Ghostty, changes the theme, then returns
to the system default, reloading to verify both saved choices.
The API is faked and no terminal app is launched.

`menu-scroll.test.cjs` loads the real HTML, CSS and JavaScript with isolated API
fixtures. It checks session folder/model filters and the main model picker in
Chromium and WebKit, including wheel, scrollbar track/thumb, keyboard selection
and dismissal. It does not start the backend or read local user configuration.

`click-scroll.test.cjs` holds the rule that a click never moves the page
(see "where the reader is" in `app.js`): on the Routing page, Live and a day
picked in turn with the list scrolled to its end; a click whose handler sets
scrollTop, grows the page above, redraws itself, or scrolls from a timer or a
shared helper; a control under what it unrolls (`data-unrolls`) going down
with it; a click that asks to go somewhere with `scrollOnPurpose(e)`;
the room kept at the foot going as the reader scrolls back; and the wheel.

`signin-callback.test.cjs` checks DimAgent's pasted callback in a narrow Chinese
dark window: invalid input remains editable, retry reaches the callback route,
and a pending or accepted submission cannot be submitted twice.

`panel-fold.test.cjs` expands and collapses on the Agents page with the list
scrolled to its end, in the tray panel (one agent open) and in the window:
"Show {n} more" unrolls the rest under the button, the view going down with
them and never back up; "Show less", and a row opened and closed, leave what
was clicked where it is on every frame.

`gateway-fold.test.cjs` folds Connect on the Gateway page with the view
scrolled: its fields hide, the head keeps the base URL and a copy button, the
head stays where it was, and the fold is remembered across a reload.

`routing-kind.test.cjs` lists calls Codex makes for itself (a guardian review,
a title, memories, a turn on Luna Reserve, a kind it does not know yet): each
has a grey tag by its model in the Requests list, in English and Chinese, the
model keeping its room first, and the request's story says what it was.

`routing-served.test.cjs` lists a request whose vendor's reply names another
model than the one asked for (gpt-6-sol served as gpt-6-luna), one answered
under the model's dated name and ones naming none: only the first is marked
"served gpt-6-luna" by its answer in the Requests list and "requested
gpt-6-sol · served gpt-6-luna" in its story, in English and Chinese, and the
click that picks it leaves the page where it is.

`routing-steady.test.cjs` scrolls the Routing page down to its routing groups,
opens one in its editor and types into its name, then streams six requests in
from the trace (new agents and accounts on the stage, failures and retries in
the story, the lists growing): the groups and the editor stay where they are
on the screen on every layout and scroll, the view's scrollTop moving by just
what grew above them, and the field keeps its focus and what was typed.

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

`add-sheet.test.cjs` opens the Providers page's add sheet as quiet rows:
Subscriptions, Vendors, Relays and On this machine, each with its word on
what it is; rows with no border, no second line and no overflow, three to a
line at 900px; an added provider not faded but marked by a small green dot
after its name, a grey count only past one account (the Claude subscription's
2); short names with the full name, plans and host in the title; a vendor's
global and China presets one row marked "Global · China", its editor picking
the region with the key typed kept; the custom provider a line at the foot,
gone while searching; the dialog growing out of the row clicked on a spring
and folding back into it on close; in English and Chinese.

`own-signin-remove.test.cjs` opens the Kiro subscription's accounts with
kiro-cli's own sign-in behind one of magpie's, then alone and first: its
Remove is there both times, its title saying magpie only hides it and it
shows again when Kiro signs in anew, and it posts login/forget; magpie's
own account in use first has no Remove; in English and Chinese.
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

`usage-ledger.test.cjs` opens the Usage page's Requests, a row per request
from a faked `/api/usage/requests`: the columns, the model asked for, the
provider and account, the model sent, "gpt-6-luna" amber by the one a vendor
answered with another model and plain on a dated name, a failure's red dot
and 429, the totals over them. Older and Newer page through 130 requests
with the pager held where it was; the agent, Failed and search filters and
the period ask the server again from the first page; Export CSV posts the
filters shown with no page and says where the file went. At the window's
narrowest (560) the table scrolls in its box and no tab scrolls the page
sideways; no left-border stripe; in English and Chinese, light and dark.

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

With Node.js and Playwright available:

```sh
node --test internal/gui/tests/menu-scroll.test.cjs internal/gui/tests/click-scroll.test.cjs internal/gui/tests/panel-fold.test.cjs internal/gui/tests/panel-routing.test.cjs internal/gui/tests/gateway-fold.test.cjs internal/gui/tests/routing-kind.test.cjs internal/gui/tests/balance-fix.test.cjs internal/gui/tests/cli-update.test.cjs internal/gui/tests/login-import.test.cjs internal/gui/tests/agy-launch.test.cjs internal/gui/tests/currency.test.cjs internal/gui/tests/usage-ledger.test.cjs internal/gui/tests/update-check.test.cjs node --test internal/gui/tests/signin-callback.test.cjs internal/gui/tests/routing-steady.test.cjs internal/gui/tests/old-webkit.test.cjs
```

If Playwright is installed outside the repository, set `NODE_PATH` to the
directory containing its package. The suite uses Playwright's Chromium and
WebKit binaries (`playwright install chromium webkit`). No frontend dependency
is needed by the app itself. Tested with Playwright 1.63.0.

For the callback test, `PLAYWRIGHT_CHANNEL=chrome` uses an installed Chrome
instead of Playwright's Chromium.

Set `BROWSER=chromium` or `BROWSER=webkit` for one engine. Set `ARTIFACT_DIR` to
an external directory to retain screenshots and Playwright traces, including
failed assertions. These browser checks run separately from `make test`.
