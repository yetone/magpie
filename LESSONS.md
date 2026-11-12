<!-- reviewed-through: e297a8ab2 (2026-10-13 23:30 +0800) -->
# Lessons from merged work

magpie's code is written, reviewed, merged and released by agents. Each night
the day's commits are reviewed for what had to be fixed again, what shouldn't
have merged and what was verified too thinly. What we learned is kept here.
Read this before changing code. Each lesson is a rule, then why, then the
evidence. "Seen" counts the days a review found it. A lesson seen on 3 or
more separate days moves into [docs/code-standards.md](docs/code-standards.md).

The day of 2026-10-05 had 220 commits and ~170 releases. About 40 of them
fixed something released earlier the same day or the day before. Most
fixes didn't need a second try. The ones that did fall into the patterns below.

On 2026-10-07 four lessons were seen a third day and moved into the code
standards: a fix reaches every sibling, a change re-derives what is built on
it, a red test on main is a bug now, and a merge records what was run at
the reviewed head. They are listed under
[Moved to the code standards](#moved-to-the-code-standards).

2026-10-08 had 192 commits and 18 releases (v0.1.1105 to v0.1.1122), each
tagged after a green run. Two more lessons were seen a third day and moved:
real use before building on it, and GUI tests in both engines and every
language. The acceptance checks now include `gofmt -l` and a Linux and
Windows `go vet`, since a cross-OS build skips `_test.go`.

2026-10-09 had 109 commits and 10-10 had 74 up to 13:51 (v0.1.1127 to
v0.1.1154). Every tag waited for a green run, every release has its 16
assets, and every commit over 400 lines has a body that says what was run.
What went wrong was earlier in the work. A privacy exemption was keyed on a
field's name, which let tool arguments go out unmasked. Two sessions
committing from one checkout shipped each other's files under the wrong
subjects. A known DNS-rebinding hole sat documented in a subsystem page
until the review reproduced it (fixed in c431225a). Three lessons were seen
a second day.

From 10-10 13:51 to 10-11 05:20 there were 191 commits and 18 releases
(v0.1.1155 to v0.1.1172), each tagged on a green run. Most bodies say what
was run, what was broken by hand, and what wasn't tried. What went wrong was
in the tests and in what "the same on main" meant. A test-isolation fix was
made twice and missed a package each time (#1524). Four commits shipped past
a red they called load or "the same on main" without filing it, and one of
those reds was a real rate-limit gap. Two lessons were seen a third day and
moved into the code standards: whatever a test starts ends when it does, and
a timing test leaves room on both sides.

From 10-11 05:20 to 10-14 04:00 there were 602 commits and 56 tags
(v0.1.1173 to v0.1.1228). Every tag the review checked was on a green run,
but v0.1.1213 and v0.1.1218 were never published (#1846), and two sessions
tagged one commit twice. What went wrong was mostly running less than the
suite: four commits on 10-13 ran chosen packages and turned main red through
guard tests in packages they didn't touch, and two left GUI files red. Seven
lessons were seen a third day and moved into the code standards: a failed
read means unknown, nothing blocks under a lock its reader needs, another
app's values come from that app, a test named for a guarantee fails without
it, a test never reaches the network, a tag waits for a green run and is not
a release, and a large commit has a body with what was run.

From 10-14 04:16 to 10-15 07:30 there were 275 commits and 16 tags
(v0.1.1229 to v0.1.1244), each on a green run with its 16 assets. Two
security holes were reported and fixed where every route passes: a page
sending `Origin: null` (#2242) and a `*.localhost` page that WebKit
resolves through DNS (#2256) both spent a subscription without a key. Main
went red six times from tests that measured time instead of the state
they were about: five waited a fixed duration, and one pinned a date that
was tomorrow in UTC. A community layout change was merged and reverted the
same day (#1354). Three plugin versions were bumped but never reached npm
(plugins#56).

From 10-16 04:00 to 10-17 04:00 there were 73 commits and 16 tags
(v0.1.1261 to v0.1.1276), each on a green first attempt with its 16
assets. Most bodies say what was run, what was broken by hand and what
wasn't tried, and the reds they name have issues. What went wrong was at
the edges of a run. v0.1.1276 moved every deprecated built-in onto its
plugin for good, with no real move on record and #2596 asking for one. A
run of chosen GUI files left i18n-keys red under a tag. A suite run at an
older base passed while the pushed head failed. Parallel full suites on
one machine made every run time out until the sessions took turns.

From 10-17 04:00 to 10-18 04:00 there were 77 commits and 7 tags
(v0.1.1277 to v0.1.1283), each on a green run with its 16 assets. The
retire stack (0f1666b38 to de671719a, about 30k lines out) took all 11
deprecated built-ins out of magpie, with a dead-code diff and real
accounts for 8 of them. What went wrong was again at the edges of a run.
Four bodies recorded a run at a tree other than the one pushed. Reds
"under load" were filed (27 such issues since 10-16) and then commented
on with no owner until CI went red on them. A check added to the fallback
path read every passing reply's whole body, and a 24MB reply waited past
its client's timeout on CI (7ac20247a). Two lessons were seen a third day
and moved into the code standards: magpie's default never overrides a
value the user set, and a checkout belongs to the session that made it.

## Reading the report

**Reproduce the reporter's exact case, on the surface they use, before you
call it fixed.** Use their provider, plan, agent and layout, and their GUI, TUI
or CLI. Check their goal (it drags, the title shows, the reason is readable),
not a step on the way to it.
- #834 came back 4 times (v0.1.920 → .925 → .931 → .938). Each round fixed
  only the agent in the screenshot. #792, #743, #791 and #933 were closed and
  reopened within hours.
- 4788cf2b put gnayiab's Cursor list error in the GUI. The reporter uses the
  TUI in WSL. Fixed in f3c05b5e.
- 60b910b7 made a warm /api/state faster. The report was the cold start
  (~8.5s of SyncCatalog). Fixed in 2a4042a9.
- 10-13: def3a41e3 built on a request its reporter (Fallmonkey) had put on
  hold ("先别做"), and was reverted in 2c30e5cee. Read the whole thread,
  to its last comment, before starting.
- Seen 2× (2026-10-05, 2026-10-13).

**Build fixtures from the reporter's literal bytes, not a similar sample you
made up.**
- #823: 878a8489 "proved" the Trae case with an invented `<tool_call>{json}`.
  The screenshot had no JSON. The reporter came back (c353f7bd), and the
  parser has grown 2 more formats since.
- 3f76105b's test used a short check-in reason. The real one was ellipsized
  (#821, eb5edced).
- 26706c33 used a different skills layout from #791's.
- For model-output parsers, collect several real failing turns first.
  Truncated and empty bodies go in the tests.
- Seen 1× (2026-10-05).

**When the vendor refuses magpie but serves its own client on the same
account, diff every header of the failing request against the official
client's. Do that before adding retries or fallbacks.**
- #256 Copilot Auto took four releases (719a6820, 026158b0, 251ef218, then
  4660fd69). Each added another fallback. The cause was a missing
  X-GitHub-Api-Version beside Copilot-Session-Token.
- Seen 1× (2026-10-05).

**Decide whether a refusal is about the account, the model or the request
content before you rest an account.**
- 5353ed54 counted WorkBuddy's "unapproved channel" (an agent's system
  prompt) as an account fault. It rested every WorkBuddy AI account for 5
  days (2033df0f).
- Test that the same request fails the same way on a second account.
- Done right on 10-07: bed306ae (#1062) told Google's 400 for an unsupported
  thinking level apart from thinking_config being turned away. It now
  remembers that the model refused reasoning off, not that the provider
  refused thoughts.
- Seen 1× (2026-10-05).

**Fix what the error is about, not only how it is worded.**
- e63fbb0e (kkgg on Discord, "GLM 直接429了"): Zhipu answered a GLM Coding
  Plan key sent to its pay-as-you-go endpoint with 429 1113 "余额不足". The
  commit relabelled the 429 as out of credit, and nothing in magpie tells the
  user the key belongs on the Coding Plan endpoint, which is what would make
  their requests work.
- When a report shows a wrong label, also ask why the request failed. Fix
  that, or point the user to the fix in the message itself.
- 10-10: 1134c7a8 reworded the "restart codex" advice. luci hit the same
  stale app-server daemon on the next release, and e611fc65 then restarted
  it from magpie.
- Seen 2× (2026-10-06, 2026-10-10).

## Fix the class, not the sample

**Scope a vendor rule to that vendor.**
- e950eb08 capped every model at 272K over one DeepSeek report. Claude [1m]
  models were capped too (531dc0aa).
- 71d2da1f priced Anthropic's 1-hour cache write onto OpenAI models
  (3a30ac87).
- Check the claim about the vendor's behaviour against its source or docs.
  Add a test that another vendor's model is unchanged.
- Seen 1× (2026-10-05).

**A restriction covers every route that reaches the thing it guards.**
- 46f03154 (#882) held keys to some models. count_tokens and System One's
  decision model weren't covered until d7a8ddc8.
- Done right on 10-07: 6b6113aa let a gateway key take the user's own value.
  It found that a local request with such a value would skip key limits, and
  moved managedKey onto access.Named, with a test that fails on the old
  check. c83c0d5f (#1112) kept a pinned MAGPIE_ADDR host while sharing, and
  checked that requests behind Tailscale Serve still count as remote.
- Seen 1× (2026-10-05).

**Scope a privacy or security exemption by where the value sits in the
protocol, never by a key's name.**
- 7137feb4 kept any object holding a "signature" or "encrypted_content" key,
  and any `*_id` key, unmasked. A tool call's input is written by the model,
  so `{"signature": …, "body": "DB_PASSWORD=… 13800138000"}` or a `user_id`
  in tool_use input went to the vendor unmasked. 1965e1f0 limited the
  exemption to thinking/reasoning blocks and Gemini's thoughtSignature, and
  never keeps anything under a tool call's arguments.
- Test the exemption with the protected key placed where the user's data
  goes: tool arguments, message text, a nested object.
- 10-28: 3e96197da let an sk- value with no capital and no run of 16
  between hyphens go unmasked, so SK hynix article addresses are no key.
  Its tests held four key shapes, none of them the closest to a slug: sk-
  and a UUID in lower case passed as words and went to the vendor
  (bad05e1b7). An exemption by shape is tested with the real secret that
  looks most like what it lets through.
- Seen 2× (2026-10-09, 2026-10-28).

**When one access list must sit inside another, prove the containment
for each pair of entry kinds; never try one entry on the other's text.**
- bf4d8f3e5 (11-11) let a gateway key's model be a `re:` pattern and
  judged an issued key's glob by matching the issuer's pattern against
  the glob's text. `[^-]` and `.` take the `*`, so an issuer held to
  `re:^openai/[^-]*$` issued `openai/*` and the issued key used
  openai/gpt-4o; `re:^gr` covered `gr*` and with it group/… ids. Its body
  said only that each test was red with the match switched off, and no
  test put an issued glob under an issuer's pattern. Fixed in 08cca7311.
- List the kinds (exact id, glob, pattern, group, account-derived) and
  test each child kind under each issuer kind with an id the issuer may
  not use. When containment can't be shown, refuse it.
- Seen 1× (2026-11-11).

**A security gap a doc names is an open bug, not a description.**
- docs/subsystems/gateway-routing.md said "There is no `Host` check, so a
  page whose own name resolves to 127.0.0.1 (DNS rebinding) counts as
  same-origin". On 10-10 the review reproduced it on main. A rebound page
  got 200 on /v1/models, /v1/chat/completions and /v1/magpie/quotas without
  a key. Fixed in c431225a.
- A same-origin exemption needs the Host checked too. A rebound page's GET
  carries no Origin at all.
- When writing such a sentence, file an issue in the same commit.
- Seen 1× (2026-10-10).

**An origin exemption judges every Origin a browser can send, not only
the http(s) names it expects.**
- c20bb76c4 (#2242, JasonBuildAI): `foreignPage` judged only http and https
  origins, so `Origin: null`, which a sandboxed frame sends, passed
  corsGuard without a key and spent the user's subscription. Null now needs
  a gateway key.
- d182294be (#2256, ggbdpq): every `*.localhost` counted as this computer
  because "browsers resolve it to loopback". Chrome and Firefox do; WebKit
  before macOS 26 asks the system resolver, so a hostile network's DNS
  serves such a page from anywhere. Now it needs a key too, except
  tauri.localhost and wails.localhost on Windows, where those webviews
  live.
- List what can arrive: null, each scheme, each engine's resolution of the
  name, and a missing Origin (the 10-10 rebinding case). Test each one
  without a key.
- Seen 2× (2026-10-10, 2026-10-14).

**A file another process rewrites is read as possibly half-written.**
- #1928's host.js `mdev()` read models.json while catalog.Sync rewrote it
  in place, and could drop the catalog on a half-written read (194b2fd3c).
- Done right: d5847f198 writes Codex's auth.json to a flushed temp file
  and renames it over the old one.
- Write through temp-and-rename, and keep the last good read when a parse
  fails.
- Seen 1× (2026-10-14).

**A pinned hash or a new download source is checked against the vendor,
and trusted only as far as who can write it.**
- 4e9335c96 pinned Bun's windows-aarch64 SHA-256. The review checked it
  against Bun's own SHASUMS256.txt, not the commit's word.
- 118f4f28b installs a plugin npm doesn't have from the plugins repo's
  `dist` branch. Its sha512 comes from the same branch, so it only guards
  against a bad mirror. The trust is in who can push `dist`: publish.yml
  on a push to main (pull_request runs get no write token).
- Seen 1× (2026-10-14).

**Text magpie adds to a prompt that goes to a vendor names neither magpie
nor the vendor's limits.**
- 5d33f060b (10-12) headed the models it added to Codex's spawn_agent
  description "(Codex names only five above; magpie serves these too)".
  That text goes upstream, to OpenAI too. 3c4f8f9ae made it neutral, and
  its test fails when the text names magpie, Codex or "five". The cost is
  ban risk, so a prompt change gets the same care as a header change.
- Seen 1× (2026-10-12).

**A new reader of a live object reads each field the way its writer
writes it.**
- 7f827b3b0 (10-27) added /v1/magpie/claude-runs, which lists every
  run and calls pid() on each. pid() read cmd.Process without a lock
  while a run's launch was still in cmd.Start writing it. It was red
  under -race on ubuntu CI at 6104e88aa only (run 40698575099), since
  most runs were cancelled by the next push. 983fff23d reads the run's
  tree under mu first; it was red with a 50ms sleep injected at launch,
  and not in 30 runs without it.
- Before adding a list or status route, find who writes each field it
  reads and under which lock. Run its test under -race with a delay
  injected into the writer.
- The fix moved when the pid shows, and the test waiting on it wasn't
  re-read: TestClaudeRunsNamesEachProcess took a listing, then read the
  fake's hang.pid, and could end on a list made before the tree was set
  (PID 0), red on CI at 5bfd76cd8. 3dd59428e waits for a run listed
  with that pid; red 3 of 3 with a sleep injected in the listing before.
  A change to when a value appears re-reads every wait on it.
- Seen 1× (2026-10-28).

**A bit or enum value has one owner.**
- d4c2512d2 (#1874) gave a usage row's Flags bit 256 to rowOwnCost while
  a rebase of #1463 gave it to rowReasoningHidden, so every reported cost
  read back as hidden reasoning. 8179ce768's TestRowFlagsApart fails when
  a bit is used twice. After a rebase, grep the values both sides claim.
- Seen 1× (2026-10-13).

**A cache entry is reused only when every field read back from it
matches what the key rounded away.**
- 059d995d5 (11-11) kept AllOf's counted windows under window{key,
  start's Unix second, scope}, and reused the kept prefix, start and
  all. A 10-day limit reset and made a 5-hour one 1.7ms later shared
  the key, so the 5-hour window reported the 10-day start and counted
  its rows. TestGatewayKeyLimitCLI was red on ubuntu and macOS CI;
  8b2fb6b08 reuses an entry only when its start equals the limit's.
  The same commit's package regexps skipped lazyre and turned
  TestNoEagerPackageRegexps red (d62220d43): it ran chosen tests, not
  the packages it touched.
- Test two values that round to one key, in the same second.
- Seen 1× (2026-11-11).

**A command never takes a flag, or a value it doesn't know, as data to
write.**
- `magpie snow --help` took "--help" as a model: it deleted
  profiles/magpie.json and wrote "--help" into the user's profile. 76db4c6c2
  (#1457) made --help print usage on every command, and Snow and Gemini CLI
  refuse an unknown value. Try each command with --help and a bad value in
  a sandbox HOME.
- Seen 1× (2026-10-11).

**A sibling the body names and leaves alone gets an issue in the same
commit.**
- d4a1b9baa (#2634) ended a think block at the first `</think>` no
  longer, and said "Not changed: the plugins repo's think-tags middleware"
  with the same rule. Nothing was filed until the review's plugins#176,
  fixed half an hour later.
- Seen 1× (2026-10-17).

**A fix that holds only for new data says what happens to the old, for
every user.**
- dccfefdf7 (#2635) made an imported skill follow the app's versioned
  folder. Skills imported before keep the stale link, and only the
  reporter was told to import again.
- Seen 1× (2026-10-17).

## The user's own files and settings

**Never let magpie's default override a value the user set themselves**
is in the code standards since 10-18. See
[Moved to the code standards](#moved-to-the-code-standards).

**Before deleting or overwriting a live credential, find every record that
could match the same key.**
- 43a65950 matched Codex accounts by display name. Two Team seats of one
  email share it, so removing one deleted auth.json for both (c13f3bfb). The
  sign-out rule was then rewritten again (4c35c1b8).
- Seen 1× (2026-10-05).

**When you hand users a writable copy, test "user edits it, then sync".**
- 3c7ceb68's "Give skills as Copies" undid the user's own edits on the next
  start (20bdf1e9).
- Seen 1× (2026-10-05).

## Verification

**A fixed date in a test is checked against every clock the code reads.**
- 3c4b4dc97 (#1591) held usage's clock at 2026-10-15 12:00 and wrote its
  rollouts then, but /api/sessions/overview counts seven days up to the
  real today. In UTC+8 it passed; on CI, in UTC on 10-14, the fixture was
  tomorrow and main went red (21aa40285). From 10-23 it would have been
  red everywhere.
- Hold every clock the path reads, or write the fixture at time.Now(). Run
  it once with TZ=UTC and once in a far zone.
- Seen 1× (2026-10-15).

**A test never swaps time.Local: the code it tests takes its zone from a
variable of its own.**
- 4028687b8 (11-12) set time.Local to +08:00 in
  TestMemorySearchDatesInLocalTime. Every time.Now reads time.Local, so
  the swap raced an http conn goroutine an earlier test had left
  finishing, and main's Test run at 727e23637 went red on macOS -race.
  af212c945 gave memoryStamp a memoryZone variable and moved the
  saved-zone check into memory, where the clock is already a variable.
- #2949 (TestHeldClock) and #4360 (testenv.Zone's cleanup) are the same
  race and still open. testenv.Zone waits for its own stand-ins only, not
  for whatever another test left running.
- Seen 1× (2026-11-12).

**Work added to a path every request or poll takes is costed at that
rate and at its largest input.**
- 644f4316f's Codex model/effort consumption was read again on every 15s
  Sessions poll. 019199e0f reads it again only when a call, price or
  identity changed, before a release.
- b14784c03 (10-18) put `failure(hw.code(), hw.errBody())` in an if's
  init in serve, so it ran on every try, a passing one too. A JSON reply
  is held whole, and `failure` over a 24MB one took 1m45s, so
  TestPluginStreamLargeReplies' JSON cases waited past their client's
  timeout on ubuntu and macOS CI. 7ac20247a reads the body only once
  the try is known to have failed, as every other `failure` call in
  serve does.
- Put a body scan after the cheap checks that rule it out, and time it
  on the largest reply a test has.
- Seen 2× (2026-10-14, 2026-10-18).

**A new route or setting runs the packages whose guards list them.**
TestEveryRouteHasACommandOrIsNamed (internal/cli) lists every route, and
TestSettingsSaveKeepsWhatItDoesNotSend (internal/gui) every setting.
- 11-11: 747632890 (context-cap) and eb56de0b5 (the catalog's model list)
  ran chosen tests and turned both guards red on CI at 8aae124b0. An hour
  later 58fb07095 (named proxies, 1,714 lines) did it again with three
  routes and two settings, a minute after c1a072636 mended the first.
  Both bodies say what they ran; neither ran `go test ./internal/cli`
  (40s). The same day three commits that each fit the core's script
  budget at their own base left it 4 bytes over together (61a93b531).
- Seen 1× (2026-11-11).

**A full run gives the slow packages room.** `go test ./...` at the default
10m timeout times out in internal/gateway (#2108), and under load in
internal/provider and internal/agent. Use -timeout 45m, so "every package
ok except gateway" stops being a normal run record.
- 194b2fd3c, 2fbbd1694, 3e812ebde and 3ec45e715 each recorded a timed-out
  package.
- Seen 1× (2026-10-14).

**One full suite runs on a machine at a time.**
- On 10-16 one session ran three suites at once (a 150m agent and
  gateway run, a full ./... run and an agent-only run) beside others, at
  load 300 to 400. Every full suite timed out, so no record said the
  suite passed, and the Cursor mover's one-line min waited ~3h behind
  them (#2557).
- Done right from the evening on: a751f5282 and later commits ran
  "through suite-lock", one suite per machine, and their records say ok.
- 10-17: at 10:07 the box had three `go test ./...` runs, ~35 GUI
  runners and over 100 browsers from several sessions, at load 313.
  d4a1b9baa's GUI run was killed at its 30s bar, ebf3f48f7's shared Go
  cache had files trimmed mid-run by another run, internal/agent took
  50 to 58 minutes (#2544), and dccfefdf7's eight AppKit tests were
  killed (#2645). The rule lives in this file only; a lock every suite
  goes through would hold it.
- Seen 2× (2026-10-16, 2026-10-17).

**A suite's log is kept whole until its reds are named.**
- 5b12407e8 (10-17) piped a full run through tail, lost which
  internal/gateway test failed at 2559s, and shipped with an unnamed red.
  `tee` the log to a file of your own and grep it.
- 10-28: ca9b5048e (#3764) recorded "531 ok and 5 red when this was
  written (the run goes on)". Its 200px steps box, which scrolls on its
  own, took reader.cjs's wheel, and routing-model-rest went 20 of 20 red,
  which isn't among the five it names (a8098a886). Write the record once
  the run has ended. A change that adds a box that scrolls on its own
  runs every GUI file that wheels the page (`reach`, `wheelTo`).
- Seen 2× (2026-10-17, 2026-10-28).

**Windows CI only compiles the tests, so a Windows-only red shows
nowhere.** Run the package on the Windows box before calling a Windows
failure "the same on main".
- TestCursor was red on Windows for 3 days, from 585f7f7d until #1248.
- 5d0c1dca's 16 and 76 Windows failures were called the same as main.
  Neither was filed. #923 is the open work to run more of the suite there.
- Seen 1× (2026-10-08).

**What telemetry sends is an allow-list, and a change to what is sent
re-derives every place that discloses it.**
- 169c23c1 sent `omp#` profile ids and the user's own model ids, which
  aren't in the public catalog (fixed in 2cdae80a: only known ids are sent).
- 7d663a27 sent each partner's daily counts, while the READMEs' Privacy and
  the site's "What does magpie send about me?" still listed what was sent
  before (b4aa62af).
- Grep the READMEs (en, zh) and site/public/docs (en, zh, ja) for the
  privacy text in the same commit.
- Seen 1× (2026-10-08).

**A test waits for the state it checks, not for an earlier step of
the same start.**
- TestServingGatesTools waited for the gateway's listener and then read
  the tool gate at once. ListenAndServe sets the listener first and takes
  the gate later, after loading rests, acks and groups. On a loaded mote,
  a full -race run of internal/gateway checked in that gap and went red
  (#6016). With a 300ms pause injected there, it was red 3 of 3.
  6fa02550a waits for the gate itself.
- Before waiting on a "ready" sign, read the code that sets it and check
  that what you assert is done before that sign, not after it.
- Seen 1× (2026-11-11).

**A body written "to be rewritten at ship" is rewritten before the
push.**
- b9f8548d2 (11-10, account export, 1,037 lines; it writes live sign-ins
  to files) went to main with only "WIP commit before the full checks;
  body rewritten at ship." The rewrite never happened, so no record of
  what was run reaches git. This is the 10-15 SUITE_LINE placeholder
  again, in another wording.
- Before pushing, read each commit's body (`git log origin/main..`) for
  "WIP", "TODO", "rewritten" or a template's placeholder.
- Seen 1× (2026-11-11).

**A new file's name is checked against the tree before it is written.**
- 8fa91a852 (11-11) wrote its idle test to internal/agent/dsh_idle_test.go,
  which already held 544b91ca8's streamIdleTimeoutMs tests, and they went
  with it. 1c3056a2e put them back minutes later. ac2340d lost
  three tests the same way.
- Before pushing, `git diff --stat` a "new" test file: it shows
  insertions only. Deletions in a file you meant to create are someone
  else's tests.
- Seen 1× (2026-11-11).

**A test that reads a dependency's source needs the dependency on disk.**
- 884dbdb2's TestTrayMenuOpenFrameIsWails read Wails' source through
  `go list -m` Dir. CI's fresh module cache hasn't unpacked it, so main was
  red on macOS and ubuntu for ~25 minutes (9b6b9966).
- Run `go mod download` for it in the test, or keep the bytes it checks in
  testdata.
- Seen 1× (2026-10-10).

**A check that a value is gone looks past text magpie generates.**
- 6c099edd's redact test checked that "ops" was gone by substring. The
  placeholder's random suffix spelled it ({{HOSTNAME_mlopsiy3}}) on macOS
  CI. f0453192 strips placeholders before the check, in its sibling
  TestMask too.
- 11-10: TestSearchAPIFailureKeepsNoURL looked for its query's "4410"
  in usage.jsonl by substring. A random port (127.0.0.1:44105, #5616)
  and then a route_id (1794294410888, run 42771382484) held it, and
  main went red. Gaoshou101's patch, posted on #5616 three days before,
  made the query letters only (17b9b5b03). A word a test looks for
  can't be spelled by a port, an id, a time or a hash.
- Seen 2× (2026-10-10, 2026-11-10).

**Playwright's WebKit is not WebKitGTK.** A GUI change with motion or a new
browser API is tried in the real Linux app too.
- 18e82adc6 (10-11) traded the layouts in a view transition. The GUI suite
  passed in WebKit, but under Wayland magpie turns WebKitGTK's compositing
  off, and WebKitGTK 2.52 crashed in the transition and froze the window
  (f5dd4d604, a reporter on X).
- Seen 1× (2026-10-12).

**A path that differs by OS is tested with each OS's value.**
- ee7a9b24a (10-11) read Grok App's folder from $XDG_DATA_HOME on Linux.
  The test sandbox set HOME, XDG_CONFIG_HOME and XDG_CACHE_HOME but not
  XDG_DATA_HOME, so the drift test was red on ubuntu CI (f38f65030). Force
  the OS switch in a test, and give the sandbox every variable the code
  reads.
- Seen 1× (2026-10-11).

**Two paths are compared with links followed on both sides.**
- 3bb9575dc followed the rtk path's links and compared it with a winget
  record read as written. macOS's /var is a link to /private/var, so
  TestRTKWingetFolderWithoutRecord was red on mote (#6127), and the same
  mismatch sat in askWingetOwns: an rtk under a junctioned folder read as
  not winget's (a1961f5ca). The test's own expected path was the link too.
- When one side goes through EvalSymlinks, so does the other, in the code
  and in the test's want. Run it where the temp folder is a link (macOS).
- Seen 1× (2026-11-11).

**A path check in a test expects what the code writes, in the file's own
encoding.**
- TestPluginImportCarryOnOffRemove wanted filepath.Join's all-backslash
  path in raw JSON. The import replaces ${CLAUDE_PLUGIN_ROOT} as written
  (`C:\...\acme-kit/scripts/lint.sh`), and JSON doubles each backslash,
  so it was red on Windows only, where CI runs no tests (#6065, fixed in
  ff11275ae).
- Seen 1× (2026-11-11).

**A fix that keeps something past a collection keeps its siblings
too.**
- #2278 took dirent's listings out of a sync.Pool, because "each
  collection emptied the pool before the next folder was read", and
  left the 64 KiB read buffer beside them in one. A collection, a Get
  on another P or -race's dropped Put made a new buffer inside
  TestListingsOfDifferentSizesMadeOnce's 64 KiB window, so it went red
  on macOS CI (#5990). The issue sat 19 hours with no owner before
  6b31147b6 kept the buffers the way listings are, with two
  collections in the test's window: red 5 of 5 on the old code.
- Seen 1× (2026-11-11).

**A release step that waits on a vendor has its own timeout-minutes.**
- notarytool's `--timeout 30m` bounds only its wait for Apple's
  answer. v0.1.3737's mac job hung in "Notarise and staple" for over
  90 minutes, under GitHub's six-hour job limit, holding a macOS
  runner; magpie's mirror job timed out waiting and the version never
  published. magpie-releases c856019a gives the step 45 minutes.
- Seen 1× (2026-11-11).

**Reproduce on CI's Go version.**
- #1184 couldn't be reproduced on go1.27.1 locally. CI ran 1.26.x, and the
  cause was a Go 1.26.8 segfault. Docker `golang:1.26.8` with
  `GOTOOLCHAIN=local` matches CI.
- Seen 1× (2026-10-08).

## Concurrency and tests

**A fake or child process a test starts ends when the test does**, and
**a timing test leaves room on both sides**, are in the code standards
since 10-11. See [Moved to the code standards](#moved-to-the-code-standards).
Two habits from them:
- After a test times out, check `ps` for its binary. An orphaned
  gateway.test spun at 96% CPU for 30 minutes. Size -count to -timeout; a
  timeout panic is not a hang until its stacks show one.
- Reproduce a timing flake by injecting the delay, not by rerunning until
  it fails (0f8e4ebc, 229ce19a, 9e35eef3).
- Wait for the state, or count what the test is about, never a duration.
  On 10-14 five tests went red on CI for a fixed wait:
  TestClaudeRunEndsAfterStructuredOutput (c4c3cafa2),
  TestAgentGoneInTheRetryPause's 100ms (b8070cc6c),
  TestCanceledRequestRestsNobody's 50ms (685521888),
  TestProviderListReadsSettingsOnce's 2s bar, now a count of parses
  (a36319d60), and TestCursorEditorCopiedOncePerChange's 100ms linger
  (2efb71ea0).

**A test that breaks a shared file puts its bytes back, and its cleanup
checks the write.**
- ea3dee96a (10-16) had TestFetchPrivateEntries write "{not json" to the
  gateway package's shared settings.json and restore it with
  `t.Cleanup(func() { settings.Save(was) })`. Save refuses to write over
  a file it can't parse, and the cleanup dropped the error, so the
  broken file waited for the next test that saved settings without
  fresh(t). TestRemoteMagpieAskedToFollow went red on ubuntu CI at
  836d074f1 (11-12), and the two tests run alone were red every time.
  aa744b9ed writes back the bytes it read, and reports a failed write.
- A cleanup that goes through the code under test inherits its guards.
  Restore with the bytes read before, or give the test its own HOME.
- Seen 1× (2026-11-13).

**A value put in a request's context lives as long as anything that
holds the context, a stopped timer included.**
- #5250: TestSocketTurnBodiesFreed kept 1 of 12 turn bodies in full
  runs. A `context.WithTimeout` under the request's context, cancelled,
  stays in an idle P's timer heap with its closure, and reached the
  request through the unrouted watch held as a context value and as
  the inflight's f.unrouted. 032340056 puts the watch behind a pointer
  unwatch clears and drops f.unrouted when it ends; the test is red
  with either half alone.
- Found by reading a heap path after a goroutine dump showed no
  holder, not by adding delays: a 300ms injected delay in the first
  suspect still passed.
- Seen 1× (2026-11-11).

**An allocation bar is relative to the same call at the small size.**
- TestAutoVisionDoesNotBuildListsAgain held searcherModel to a fixed
  50 allocations: 51 on Linux and 130 on Windows with the lists kept as
  meant (#5123). c1b8010a2 compares 400 models against 4, as the
  AutoVision half did, and is still red with the cache broken by hand.
- The same day TestDshRoundWithNothingChangedBuildsNothing's fixed 300
  read 421 on Windows CI. It now compares a settled round over 402
  models with one over 2; with the skip broken by hand that is 78375
  against 1780.
- Seen 1× (2026-11-11).

**A new route or setting field is run against the guard tests that list
every route and field, not only its own tests.**
- f5e1b1091 (11-11) added POST /api/settings/context-cap-mode and
  Settings.ContextCapAll and ran `cli -run ContextCap`. The CLI parity
  test (#6102) and the GUI keep test (#6103) list every route and
  field, and both went red on main; its usage line also broke the help
  column a test checks (#6104). e0cadc312 added three /api/memory
  routes the same day with no parity entry.
- 11-12: 60fde6fb1 added POST /api/library/mcp-account the same way,
  and the parity test stayed red on main until 5a36e46d6.
- Run the whole internal/cli and internal/gui packages for a new route
  or field; `grep -n` the parity map for the route before pushing.
- Seen 2× (2026-11-11, 2026-11-12).

## Red tests and releases

**Flaky tests: find the cause.** "A test that fails on main is a bug to fix
now" is in the code standards. What the reviews found about finding the
cause:
- TestPluginSOCKSProxy timed out at 60s on ubuntu CI and was rerun as "load
  jitter". The cause (9a852ae9d, 10-15): the test freed a port for a dead
  SOCKS proxy, the netproxy bridge's :0 listen was handed that same port,
  and the bridge dialled itself under a one-minute read deadline. Found by
  injecting the reuse (60.03s red, 2.4s green after), not by rerunning. A
  port a test frees can be handed to anything next: d0ccbbebc fixed the
  gateway's proxy-down tests for the same reason.
- TestUnreadLoginsNotWrittenOver needed two writes inside one wall-clock
  second (a `.bad-<second>` name) and failed on Linux CI (237d216b). Test a
  name made from time.Now() across a second boundary.
- Before fixing a red CI run, `git log origin/main` for a commit that names
  that run or test. 85fd35ab and e34e53a1 fixed the same lost tail in two
  sessions at once.
- 10-07: 07e246e5 named 24 GUI failures as "the same on origin/main". Run
  alone on main, all 8 of their files pass, and so do the two the full run
  failed (api-key-theme, fold-show-grip). They fail only under the full run,
  and CI never runs the GUI suite, so nothing shows it. e95852e3 (#948) named
  TestCursor (Windows) and TestOTelRetryAfterBound the same way. Both pass on
  macOS main and on CI. Neither is filed.
- 10-08: 6ab4c16f fixed the run 0f8e4ebc had already fixed. bc7c62dc, #1216,
  a2a7fb11, 5d0c1dca and #950 each shipped past "fails the same on main".
- Done right on 10-08: #1184 found the Go 1.26.8 segfault behind its flake,
  and 30a8ce29 fixed its flake at the cause.
- 10-11: four commits named a red and shipped past it unfiled. b1396d23
  reran TestMaxRPM as load; it was a real gap (the vendor counted a late
  first request in the next request's minute, ce1ecaa4). 2305ab1f's dsh
  test and 20582866a's queued-stream test "pass alone"; both had causes
  (9c377231, 3f4287e6). db965824 said panel-usage "fails the same on
  origin/main 0a11b183", but 0a11b183 was hours old and f823a53d had fixed
  that test; on the real main it passed 3 of 3.
  - "The same on main" means on origin/main just fetched: `git fetch`, then
    name the sha you ran.
  - A red under load is a timing assumption a user will meet on a busy
    machine. File it, with its output, in the same commit.
- 10-11: two sessions again fixed one flake in the same minute (4caae1c8 and
  3f4287e6), and on 10-10 3a1ade97 landed while another fix of the same
  test was being written. Checking `git log` isn't enough while both are
  working; say on an issue, or file one, that you are on it before you
  start.
- 10-12/10-13: #1794 got four "seen again" comments before anyone took
  it (fixed in 6ae6b3402), and #1543 was seen three times with no owner
  (097479dab). A comment that a red came back is not work on it: take it
  or say who has it. 31c1e3ab1 named 8 GUI files that "fail the same way
  on an origin/main 552e974f2 copy" and filed none of them; they were
  fixed one by one over the next day (searcher-pick, profile-in-use,
  library-mcp-tools among them).
- 10-14: reds named in a body and left unfiled again. 56fcd1dc2 shipped
  past a usage-ledger red that "passes alone". hhh2210's
  TestPluginSOCKSProxy red on Bun 1.4.3 was filed only by the review
  (#2207). b476dc5a9's Routing GUI reds on untouched main have #2252 and
  nothing else. A red gets its own issue, with its output, in the same
  commit.
- 10-17: the #1794 pattern on a second day. #2444 had ump45nose's patch
  for 22 hours while two sessions commented "seen again"; it went red on
  CI at f191d6423 and was fixed with that patch (50a703190). #2737 got
  three "Not taking it; recording the numbers" comments before a3b48e0f7
  fixed it.
- 11-11: the same on a third occasion. #5433 had skygunner's patch, with
  a test red on the old code, for three days with no owner. It went red
  on ubuntu CI at 296a9292c and was landed unchanged in 855b97a40. A
  patch on an open red issue is reviewed in the tick it is posted.
  - One test's subtests were filed three times: #2169, #2711 and #2737
    are all TestTranslatedStreamKeepsClientAlive. Search the test's name
    before filing, and file per test.
  - A red filed "under load" is a CI red soon after: bf931f82c filed
    #2711 and shipped past it, and its own CI run went red on another
    subtest of the same test. 27 issues titled "under load" were opened
    on 10-16 and 10-17. Fix the class (wait on state, room on both sides) instead
    of filing one a test.
  - 10-28: the same on a third day. JoeyWu-tech posted a patch for
    #3568 (TestHandedOverGatewayFinishesItsStream, a real handover drop)
    on 10-25, with a reproduction and red-on-old tests. Nobody ported it
    for three days, and it went red on ubuntu CI again at 0a7633136. It
    was ported unchanged as 11c427eaa. Now in the code standards (a
    red's issue is read to its last comment, 77898391d).
- 10-27: the #1794 pattern on a third day. internal/provider has died
  on main since 10-25 in TestUnreadLoginsWriteErrorsKeepCredentials
  (#3632, a synctest fatal, 2 of 2 on mote at 4b0a32e0f). The issue has
  two complete patches (JoeyWu-tech's and skygunner's) and nine
  comments, no owner. #3919 and #3924 have had JoeyWu-tech's patches
  since 10-27 15:37 and 16:24, and #3854 has three TestFetch* reds.
  Every tag since, to v0.1.1780, went out over them, and a provider
  change can only pass its package with those tests skipped.
  - A patch on a red issue is reviewed and merged, or answered with
    why not, the day it is posted.
  - A red that kills its package hides every new red behind it.
    6eb510f78 (10-26) made a Codex ask of the models list that three
    TestFetch* tests counted as a second ask; its provider run died at
    #3632 first, it then ran `-run Codex`, and the three were filed
    only later as #3854. With #3632 fixed (4842f69be), #3854 and #3906
    merged too (759b3b8c4, ed49d1b38), internal/provider passed whole
    on 10-28 for the first time since 10-25.
  - A red step hides every step after it. CI's Race step runs only
    when Test passes, so 9a888323a's allocation bound (#3472), red
    every time under -race, first showed on 10-28 once Test was green
    (#3956, fafce6545). A test that bounds bytes runs once under
    -race before it lands: sync.Pool drops a quarter of what is put in
    it there, and regexp and zstd take their state from one.
  - A red assertion hides every assertion after it. #3410 (routing-pause)
    was filed on 10-23 and failed at its first press, from 91b0be836's
    motion. On 10-25 c888ef272 saved a rule's hours with a zone, and the
    fixture's later expectation, without a zone, stayed wrong unseen
    until 7b007e986 fixed the press on 10-28. #3408 to #3411 sat five
    days with no owner, and #3104 (usage-ledger) has had none since
    10-20. A filed red is still a test that checks nothing after its
    first failure.
- A cancelled run tests nothing. On 10-14 nine pushes in ten minutes
  cancelled each other's runs, and 3c4b4dc97 never had a run of its own;
  its red showed first at 84f14e555.
  - 10-28: of main's last 58 Test runs, 49 were cancelled by the next
    push. 0b99b8fbe's new allocation bound went red in macOS's Race job
    (sync.Pool lets go under -race) at 25df3187b, the first run after it
    that finished, and sat red until d3f3f3381. A test that bounds
    allocations or a pool's savings checks `raceOn` (race_on_test.go)
    and is run once with -race before it is pushed.
- Done right: 2bd49cc5's race was fixed at its cause in 25 minutes
  (28049bd2, -race -count=40 -cpu 1,2). 60f87223, b1cba654, 443f1cbc and
  395c4bc5 fixed flakes at their cause instead of retrying. 06420a71 fixed
  the macOS first-run check at its cause.
- Seen 3× (2026-10-05, 2026-10-06, 2026-10-07); the rule itself is in the
  code standards.

## Merging and closing

**A change no user can undo gets a real run per vendor before its tag.**
- 9fcf6a6e7, f9ceac162 and 7d7dba31a (10-16) moved every deprecated
  built-in's accounts onto its plugin, dropped the check that held a move
  back while a model was in use, and took away the way back. The owner
  asked for it. The bodies have unit and GUI tests, broken by hand, and a
  full suite, but no move of a real account and no "Not tried" line.
  #2596 asked for a sandbox move per subscription, the models a plugin
  lacks, Factory's min (0.1.31 while npm had 0.1.32) and a rollback
  story. v0.1.1276 shipped it to every user with none of them answered.
- "No way back" raises the bar. Move each vendor's real account in a
  sandbox HOME, list what the user loses, and answer the open issue
  before the tag.
- Seen 1× (2026-10-16).

**Never merge a PR whose own new test fails, or with known problems a
review listed.** (Merging only the reviewed head, and writing what was run
before the merge, are in the code standards.)
- #885 and #826 merged with their own new test failing. #846 merged with
  four known-wrong translations. Fix or file the problems first.
- Seen 1× (2026-10-05).

**An issue that names two problems is closed when both are fixed.**
- #2449's title named a lost tail and plugin hosts outliving their
  test's HOME. 6641e1b0a fixed the first and it was closed on 10-15. The
  second came back on macOS CI at 84cd686af on 10-17 with no owner
  (#2702).
- Seen 1× (2026-10-17).

**A size budget raised whenever a change reaches it bounds nothing.**
- app.js's budget was raised four times in 2.5 days, the last to
  1,269,000 by 85eef5d87, while app.js grew 53KB and nothing moved out
  (#2710). 17557132c met the same budget without a raise.
- A raise moves something out first, or follows a growth rate the owner
  set.
- 11-10: the cap held, so it was met another way. The budget counts
  comments, and five features that reached it (ecc16fe97, b72256b59,
  b3a57b3d8, d374168d0 and the check-in time) each went red on CI and
  were brought back under by deleting comments that say why the code is
  as it is: 12 lines in b596da773, 44 in 0eac4e6aa (11-09). Nothing moved
  out to a plugin (#5918). A budget that counts comments makes them the
  cheapest thing to cut. Measure what the budget is about, and run the
  sub-second guard before pushing a change near it.
- 11-12: coreScriptBudget was raised four times in one afternoon, by
  367 (d96403141), 151 (05cadcafd), 458 (4519eea71) and 113 (09e2eef0d),
  1,089 bytes in all, each saying there was no page left to move it to.
  09e2eef0d's 113 came from a rebase: 6055f626e fit at its base and the
  two commits that landed under it filled the room.
- Seen 3× (2026-10-17, 2026-11-10, 2026-11-12); moved to the code
  standards.

**A release published after a newer one takes Latest, and with it the
update feed and the tap, unless the publish step checks.**
- 10-15: v0.1.1255 was published 25 minutes after v0.1.1256, and
  GitHub marked the older one Latest (moved back by hand then).
- 10-28: v0.1.1891 finished 16s after v0.1.1892. 1891 became Latest,
  which usemagpie.ai's /api/latest serves, and the Homebrew tap went from
  1892 back to 1891 (tap 1d47b64). v0.1.1890, still notarising, would
  have done it again. magpie-releases ee73d7a now passes `make_latest`
  only to the highest version and runs the tap update only then.
- A fix made by hand once is a fix for that release only. When it comes
  back, fix the step that does it.
- Seen 2× (2026-10-15, 2026-10-28).

**A publish run that stays red for one known package hides every new
failure behind it.**
- think-tags 0.1.1 can't publish (E404, publish.yml isn't its trusted
  publisher, plugins#180), so every plugins publish run since b3a860c is
  red, and a new package's failure would look the same.
- Check each package with `npm view --prefer-online`, not the run's
  colour, until the known red is fixed.
- Seen 1× (2026-10-17).

**Keep the issue open until the reporter's case works. Reopen when they say
it doesn't.**
- On #876 the reporter couldn't reopen it themselves and had to file #888.
- Don't close as not-planned what the owner hasn't ruled on. #769 and #871
  were reversed.
- Done right on 10-07: #1050, #1053, #1063 and #958 shipped a fix and stayed
  open for the reporter.
- Seen 1× (2026-10-05).

**One commit, one goal** is in the code standards since 10-28. See
[Moved to the code standards](#moved-to-the-code-standards).

**A commit's file list is checked against its goal before pushing.**
- 698ed4adb (11-12, "a busy port names its holder") was committed from a
  tree made before two commits pushed eight minutes earlier, and took
  both out with no word in its body: 919780414's Usage table view
  (quotatable.js and its test deleted, its app.js, app.css and i18n
  hunks reverted) and 88d2962e0's 16-byte sessions hashes (digest.go
  deleted). Both were gone from main until c14fa0ce9 put them back.
- The same day, three hours later: d31988c95 ("the Mac's plain title
  bar is done as the main window is first shown") also reversed
  1648fcfe2 and 95eab9a66, Mistral's prompt_cache_key: gateway.go's
  withMistralCacheKey, mistral_cache_key_test.go and two
  TestOwnCacheKey rows, none of them in its body. 2858566a1 put them
  back. A GUI change has no reason to touch internal/gateway.
- Before pushing, read `git show --stat HEAD` after the rebase. A deleted
  file or a hunk in a package the goal never names means the tree is
  stale: rebuild the change on origin/main, don't push the old tree.
- Seen 1× (2026-11-12).

**State added for one provider or one feature is keyed by it, not
held process-wide.**
- b70563ec6 kept one "accounts reordered at" stamp for the whole
  gateway. Reordering Claude's accounts dropped every other provider's
  conversation stick, so a Codex conversation lost its account and its
  prompt cache on the next turn. In tests, any earlier reorder made
  three cache tests red in package runs (#6249 and #6262, filed twice
  for one cause). 04e38e604 keys the stamp by provider and has tests
  forget it.
- A test of such a value resets it in `t.Cleanup`, and a case where
  another provider changes it shows the scope is right.
- A process-local value set by the CLI never reaches the running
  gateway (#6265).
- Seen 1× (2026-11-12).

**A commit saved early stays local until it is finished; "wip" never
reaches main.**
- f1e6705c5 (11-12 12:10), subject "wip" and no body, pushed the
  turn-effort and thinking-summary code to main. Its test change came
  35 minutes later in 9d47b441e, a one-line commit that carries the
  whole record, and in between
  TestClaudeSubscriptionPromptKeepsForeignHarnessOutOfSystem was red on
  main (the CLI's --settings gained showThinkingSummaries). Its CI run
  was cancelled, so nothing showed it.
- Committing at once (the machine reboots) is right; pushing it isn't.
  Before a push, `git log origin/main..HEAD --format=%s` names no
  "wip", and the change and its tests go out as one commit.
- Seen 1× (2026-11-12).

**A squash merge's message is written, not GitHub's default.** Give
`gh pr merge --squash` a `--subject` and a `--body-file` holding the PR's
own change and what was run at the reviewed head.
- 708ae4144 (#852) and 81b7016e6 (#1028) each carry a ~520KB body. Their
  branches had main merged in (100 commits on #1028), and the default
  squash message lists every commit's message. #1028's own run record
  isn't in git at all: the body after its subject is other commits.
- Before pushing, check that `git log -1 --format=%B | wc -c` is a few KB at
  most.
- 10-14: b6e82c6c5 put a paragraph in the subject line. The subject is one
  short line; the rest goes in the body.
- Seen 2× (2026-10-13, 2026-10-14).

**A commit message file is made fresh for this commit, and its subject
is read back before the push.**
- plugins bc9f611e (11-12) went out as "qoder 0.2.60: the business record
  names the model", another session's stale message file. Its content was
  qoder 0.2.67, a different change; 7b42e404 had to note it after the
  push, since main isn't rewritten.
- 10-15: ce1251cc9, the Windows dsh fix, shipped under a lessons subject
  for the same reason: zsh's noclobber made `cat >` onto the existing
  file fail, and the commit took the old text.
- Name the file for this change in your own scratch folder, write it with
  `>|`, and check `git log -1 --format=%s` against the diff before
  pushing.
- Seen 2× (2026-10-15, 2026-11-12).

**A community PR that changes how a page looks is not merged on review
alone.**
- #1354 (Nxir) rearranged the Routing page with no issue asking for it. It
  passed review and shipped, users rejected it the same day ("v0.1.1231
  没有这个问题"), and bfef5d51b reverted it whole. The fault was the merge,
  not the author.
- A layout change needs an issue or the owner asking for it. The default is
  no.
- Seen 1× (2026-10-14).

**Two sessions never commit from the same checkout** is in the code
standards since 10-18, widened to a worktree and its logs. See
[Moved to the code standards](#moved-to-the-code-standards).

## Moved to the code standards

- **A red's issue is read to its last comment, and a patch on it is
  taken first** (10-12, 10-17, 10-27, 10-28). On 10-28 JoeyWu-tech's
  #3568 patch sat three days until CI went red on it again (11c427eaa).
- **A mover's min is npm's version when the change lands** (10-16,
  10-18, 10-24). On 10-16 19e9fc47f raised Cursor's min a version behind
  npm, and 53009f81a left Factory one behind (#2557). On 10-18 zed 0.1.9
  was published with no min raised until the review's ccb2eea15.
  On 10-24 factory 0.1.38 (plugins #349, an option a user asked for)
  was published and the min stayed 0.1.37 for four days, until
  490c0a4ad. The rule now also says a plugin version raises the min the
  same day.
These were seen on three or more days. The rules and their evidence are in
[docs/code-standards.md](docs/code-standards.md); a review still checks
each one.

- **One commit, one goal** (10-05, 10-06, 10-28). On 10-28 6e080ca04
  answered psikent (/v1/models says every model takes tools) and windy
  (a group's Context hint names the member a longer conversation skips)
  under one subject that names only the first.
- **A size budget is met, not raised** (10-17, 11-10, 11-12). On 11-12
  coreScriptBudget rose four times in one afternoon (d96403141,
  05cadcafd, 4519eea71, 09e2eef0d), 1,089 bytes, with nothing moved out.
- **Never let magpie's default override a value the user set themselves**
  (10-05, 10-10, 10-17). On 10-17 5a2414acd brought every plugins.yml
  without `settled` up once, Essentials to All. A user who had run
  `magpie plugin preset essentials` themselves wrote the same file, and
  the body doesn't name them (#2749).
- **A checkout, worktree or log belongs to the session that made it**
  (10-09, 10-12, 10-17). On 10-17 another session checked out
  origin/main in 7660399a1's worktree mid-run, and another deleted the
  first part of 17557132c's GUI log.

- **A fix reaches every sibling of the bug** (10-05, 10-06, 10-07). Seen
  six times on 10-05. On 10-07 cd8d64a9's sub2api balance URL was left out
  of the hint shown after a failed read.
- **A change re-derives everything built on what it changes** (10-05,
  10-06, 10-07). On 10-07: click-scroll red since 5d871723, four GUI tests
  left red by page-only test runs, and ee2588c2's test race (3e38bd76).
- **A test that fails on main is a bug to fix now** (10-05, 10-06, 10-07).
  On 10-07 dcbae2ef, 76d82528, b2345078, 07e246e5 and e95852e3 each shipped
  past "fails the same on origin/main".
- **Merge only the reviewed head, and write what was run before the merge**
  (10-05, 10-06, 10-07). On 10-07 #1037 merged with no record.
- **Real use before building on it, or telling users it works** (10-05,
  10-07, 10-08). Acceptance criterion 5. On 10-08 bbf99143 "not tried on
  Linux" broke Linux vet with the WSL2 distro there, and #1185 was reviewed
  on macOS only.
- **GUI tests in both engines, at narrow widths, in every language**
  (10-05, 10-06, 10-08). Acceptance criterion 4. On 10-08 aff4f9f2's zh-TW
  missed later strings, and usage-ledger was red ~7h after 586f2bce.
- **A fix reaches every sibling** was missed again on 10-08: #1239 fixed
  Air 9h after a8290d27, and 173ab71e fixed Gemini bodies after #950.
  Done right on 10-08: a8290d27 itself, 9fcd332b (cmd.exe quoting at one
  choke point, tried on the Windows box), 74a809dc with 84f37ac9
  (re-derived account keys), #1271/#1275 (built-in and plugin together),
  3aeb7d1b (a refusal scoped to the model) and #1288.
- **10-09/10-10, criteria still missed.**
  - Sibling: 12c0550a said "Only aBlock had this" while #1445 (open for an
    hour) listed the class. 64179e73, 13356b9d and d7186e51 followed.
  - The suite: 2da8fbe4 ran six packages, not `go test ./...`. Its testenv
    exec broke internal/proc's TestNoCommandBypassesProc, red ~50 min until
    b934d64a.
  - Real use: a3a8e257 shipped GitHub plugin icons without loading the real
    list, and the owner found none showing (52f6b3a3). aa82288f's Volcengine
    plan windows went out "not tried against the real API". It was said
    honestly, and #1427 stays open.
- **Whatever a test starts ends when the test does** (10-06, 10-08,
  10-11). On 10-11 a CLI-identity ask outliving its test wrote into the
  next test's folder (#1524). 4ee3a76b fixed it in the gateway's TestMain
  only; 9c377231 fixed it for every binary through testenv's marker, but
  the gateway isolates itself without testenv and never set the marker, so
  #1524 came back on CI (a349de82). A guard test now fails if the gateway's
  TestMain doesn't set it.
- **A timing test leaves room on both sides** (10-08, 10-09, 10-11). On
  10-10 8424a004 assumed two concurrent handlers let go in order
  (3a1ade97). On 10-11 TestMaxRPM and the queued-stream tests (above).
- **A failed read, parse or hash means "unknown"** (10-05, 10-11, 10-12,
  10-13). On 10-12 7a9490638's IP allow list let every address in when
  settings.json couldn't be read (7d9382db2). On 10-13 #2017's patch read an
  unreadable codex-autoreset.json as "no reset spent" (e297a8ab2). When a
  shared reader is wrong, fix it, not a strict copy beside it.
- **Nothing that can block runs under a lock its reader needs** (10-06,
  10-08, 10-11, 10-13). 7cba316b9's timer went red under -race
  (a2afc1828); a fake Codex appended from two goroutines (a75de4c21).
- **Another app's values come from that app** (10-05, 10-06, 10-13).
  accc8c090 sent 127.0.0.1 where Claude Code sends localhost (e0ecf0161).
- **A test named for a guarantee fails when it breaks** (10-08, 10-12,
  10-13). bbe8825c0 (9dd5917a4) and ggbdpq's 44b5b2b96 and e21b1dbc1
  (cc93b566e) passed on the old code. Break it by hand, watch it go red.
- **A test never reaches the network** (10-08, 10-11, 10-13). TestPiPackage
  tested npm's latest pi (f230fa23b); tests waited 30s on models.dev
  (bde11c778).
- **A tag waits for a green run, and a tag is not a release** (10-05,
  10-08, 10-12, 10-13). v0.1.1192/1193 tagged one commit; v0.1.1213 and
  v0.1.1218 never published (#1846). `npm view --prefer-online` a plugin
  version before naming it.
- **A large commit has a body with what was run** (10-08, 10-12, 10-13).
  ea3d480ed and 1cd814004 (community, no record); 3ccbe6bc1's "push notes"
  hid two red GUI tests (2dcc51417, ba0712888).
- **10-11 to 10-13, criteria still missed.**
  - The suite: 6fa3109ea, 43d8a5633, 5617281f5 and cdb7b5ff7 ran chosen
    packages and broke guard tests elsewhere (494609866, 24146c031,
    c6de0d2a5).
  - GUI: 18e82adc6 left 8 GUI files red after a run cut short (054bdb8bb);
    e3a497d7a left gui-ja red ~3h (28aac86bc).
  - Sibling: 4ed379f11 left three deciders on the raw body (0c6752dbd);
    90ac1f038 mended StepCode only (cb003877f).
  - Timing: #1874's TestUsageIngestTouchesNoLimit stamped its calls a
    minute ago and went red a minute after midnight UTC (1acdf3a8f).
  - Whatever a test starts: Devin's held installer (2ff4ecb42) and a
    Claude sign-in's `claude auth login` run (ebbe76420, #1802) outlived
    their tests.
- **10-14, criteria still missed.**
  - A large commit has a body with what was run: 509328ccb and c1e354245
    pointed to "the push" for it. A pointer is not a record.
  - A tag is not a release: zed 0.1.7 and zen-free 0.1.2 to 0.1.4 were
    bumped and never reached npm (plugins#56). Done right: every mover min
    raised on 10-14 (0337ce4bc, 50929661f, bd4cb7a26, 2c18c697c,
    aae49d086) named a version npm had.
- **10-15, criteria still missed.**
  - A large commit has a body with what was run: 113 of the 120 commits
    pushed at 23:08 (728cb2d29 to a7a012202) end "Full suite:
    SUITE_LINE", a placeholder no one filled in. The batch's real record
    is in 386d341e7 alone. Before pushing, grep the bodies for a
    template's placeholder.
  - A tag is not a release, and a release is not Latest: v0.1.1255
    (a1f7b8c13) was published 25 minutes after v0.1.1256 (37fec1240), so
    GitHub marked the older one Latest and the site's downloads served
    it until the review moved the mark. Publish in tag order, or pass
    `--latest` to the newest.
  - A rerun's green hides the first try: a1f7b8c13's run is green on
    attempt 2, its attempt 1 red (#2390, fixed at its cause in
    4a6bf80f4). Read `attempt` before calling a run green.
  - GUI tests in every file a change can reach: three changes ran the
    GUI files they chose and left others red on main, which CI never
    runs. 64b0f3a20 (10-15) had the sidebar ask /api/plugin-pages, and
    menu-scroll's strict fixture threw on it, 24 of 24 red until
    a61a6f954 the next day. 8f55c27ad (#2396) held Settings' tabs at the
    top, and text-size's click landed under them, red ~5h until
    d3ca90afc. 94942f9f3 ran only the i18n files, and
    agent-connect-no-models went 8 of 16 red until cf21b71ff (#2435). A
    new API call or a layout change runs the whole GUI suite.
  - A Windows rule needs each test of that agent judged for the right
    system: 6e2aa8e32 (#2300) made ~\.dsh never gone on Windows, and two
    Mac-app dsh tests that didn't force goneOS went red on Windows CI,
    which now runs the Dsh tests, until ce1251cc9.
- **10-16, criteria still missed.**
  - GUI tests in every file a change can reach: 9b04c4e20 (#2118 hooks,
    v0.1.1273) ran the plugin GUI files and four more. plugins.js used a
    core word as its own, so i18n-keys was red under the tag until
    a4fa7bbd8 (#2586) 35 minutes later.
  - What was run at the pushed head: 1978eabe5's suite ran at a751f5282.
    1d6f461a9 and the 22:58 batch had added ~2.5KB of core script since,
    so TestCoreScriptBudget passed at the base and failed at the pushed
    head (1,426,102 > 1,426,000), red on CI for two runs until 81313b912
    (#2612). Run the size budgets again after a rebase.
  - Each commit's record is its own: 61ca3924c and 83f3316cf carry the
    same body word for word, so neither says which test guards which
    change.
  - A cancelled run tests nothing: 6b3ee5a03 and e243937ca never had a
    run of their own; the next push's run covered them.
- **10-17, criteria still missed.**
  - What was run at the pushed head: d4a1b9baa ran "this change plus the
    next commit's library edit". 252bc61ce said it was rebased on
    cb05dc2d1, but its parent 84cd686af changed internal/agent three
    minutes before, and the pair first ran together on CI. bad4508c4
    and bf931f82c ran at older bases too.
  - Each citation is checked: 04e8aa8f1 cites #2710 for a GUI red that
    is #2552.
- **11-10, criteria still missed.**
  - The suite before the push: two sub-second guards were red on main
    eight times in one day. TestCoreScriptBudget went red after
    ecc16fe97, b72256b59, b3a57b3d8, d374168d0 and the check-in time
    (#5918). TestEveryRouteHasACommandOrIsNamed went red after a Codex
    service-tier route (8417ae6da), 350856f51 (71fbb33a7) and 6a253f8af
    (488d0f9fc). Each was fixed by a follow-up after CI said so. Tagging
    without waiting for CI is not pushing without running `go test
    ./internal/gui ./internal/cli`: a new route or core bytes trip them
    every time.
  - The same class in two more packages that afternoon. d3f862340 put a
    package-level `regexp.MustCompile` in internal/library, and the root
    package's TestNoEagerPackageRegexps was red until 7869a454c.
    d109ec8da added the miniq agent with no Library target and no
    noLibrary line, and TestEveryAgentInLibrary was red until 68cb92e8e.
    A guard test lives where the rule is kept, not where the change is.
    A new agent, route or package-level value runs `go test .
    ./internal/library ./internal/cli ./internal/gui` before the push.
  - And in the evening: e8fbee60e's /api/gateway/pause made the route
    guard red a fourth time (db9f0652a), and 07af9f86a's "billing  pay
    as you go" row turned TestProvidersShowVertexCredentials red, whose
    lineWith(out, " as ") found the billing row before the "as" row
    (ea310a8c9). A test that finds a row by a word in it breaks when
    another row says the word; find a labelled row by its label.
  - Three more the same night, after that line was written.
    9c39edb92 (Avibe) and 4a9142ccd (Obsidian) each added an agent with
    no Library line, and e6f151507's /api/speeds made the route guard red
    a fifth time. All three were pushed and tagged before CI ran.
    Writing the rule here didn't reach those sessions. Until a pre-push
    check runs these guards, run them yourself before every push that
    adds an agent or a route.
  - 86825005e (#5890) wrote two hook-budget tests whose margin was the
    one second between a hook's own timer and the RPC's 11s. That 11s
    counts the plugin host's start, which on a busy CI machine is more
    than a second. Both went red on CI and were fixed one at a time
    (e39ecfec8, then 0659dc66f). When a test's margin is under a
    deadline that also covers starting something, start it first, and
    fix every test in the file that shares the margin.
  - The route guard went red a sixth and seventh time before midnight:
    e053c337a's /api/settings/join and 4623f5419's five
    /api/settings/https routes. 4623f5419's record says it ran
    `go test ./internal/cli -run Help|HTTPS|TLS|…`, and a -run filter
    skips the guard, which was named for none of those words. A change
    that adds a route runs internal/cli whole, with no -run.
  - The same commit's `GET /magpie-ca.crt` was in neither
    clientPrefixes nor readOnlyRoutes, so internal/gateway's
    TestClientPathCoversHandler and TestReadOnlyKeyEveryRoute were red
    on ubuntu and macOS CI for about ten hours (db0f2f20d, 5b28e8599)
    under two tags, until 6b11914df. The CLI fix (20aff50d4) answered
    one guard and stopped there. A route has three guards: the CLI's,
    the one-port page's and the read-only key's. A change that adds one
    runs internal/cli and internal/gateway whole, and a fix for one
    guard reads the other two.
  - plugins#601 (muzimu217) brought back the Doubao plugin declined on
    10-06, now reading Doubao.app's HttpOnly cookies over CDP, clicking
    through its reward campaigns, installing a .dmg into /Applications
    and sending requests through a third-party relay. It was closed
    unrun. A vendor the maintainer declined is checked for before the
    diff is read: search the repo's closed issues and PRs and
    docs/contributors.md for the vendor's name.
- **11-10, criteria still missed.**
  - The suite: 6af54dc9a (internal/library) and b9f8548d2
    (internal/provider) each added a package-level regexp.MustCompile.
    Each ran its own packages, not the root package, whose
    TestNoEagerPackageRegexps went red on ubuntu and macOS CI. It was
    found by yinwu01 (#6020) and fixed in a68f114d9. A new package-level
    var runs the guards in `.` too.
- **11-11, criteria still missed.**
  - The suite, a second day for the same two guards: 55dff77b4 (plugin
    pages behind a cookie proxy) ran `go test -run PluginPage
    ./internal/gui`. Seven new package-level regexps tripped
    TestNoEagerPackageRegexps (fixed in de10c6676, #6145), and its 11
    lines in assets/pluginpages.js put TestCoreScriptBudget over until
    f54920280 cut a comment (#5918). A `-run` of a package's own tests
    is not a run of the package; its guards take a second, so run it
    whole.
  - A red on main had three "seen again" comments and no owner: #5918
    (TestCoreScriptBudget, red from 16ed1a952) was commented on at
    11:51, 12:22 and 13:14, and 8016205e3 shipped past it "commented".
    The cause was the measure. It counted comments, so each red was paid
    for by cutting the comments that said why, about 40 times in the
    budget's history. 6cc182339 counts code only. When a guard keeps
    going red the same way, fix the guard's class; don't feed it
    another cut.
  - A commit's message belongs to that commit: 16ed1a952 carries
    d1a2e92e8's subject and body word for word (a hint card at the
    pointer), while its diff is the plugin-page drag order: a new
    route, a setting and two tests. Its run record names none of them,
    and the new route wasn't named in the CLI route map, so
    TestEveryRouteHasACommandOrIsNamed was red on main for hours
    (#6162, fixed in 1c95c87c2). Before pushing, check that
    `git show --stat` matches what the subject says.
    The same day 85e6ec540 (the Paseo agent, 36 files, 887 lines out)
    was committed on a stale index and took out the six commits before
    it (WebView2, library skills, OpenCode Go endpoints, remote quotas);
    its body records runs that passed on that tree. b52c71114 restored
    them. A stat with files the change never touched is a stale index:
    stop, don't push.
  - A value added to a shared list re-runs every loop over it: ab5824aee
    added "total" to access.Periods and ran internal/access, budget, cli
    and provider. internal/gateway's
    TestKeyLimitReservationsStayInTheirWindow loops over every period and
    crosses each window's end, which a total window doesn't have, and
    went red on CI (#6165, 535f0dd95). Grep the list's name across the
    tree and run each package that reads it.
  - A test that counts work counts that work, not the process's bytes:
    efdca11be's TestPiGroupEntryKeepsItsMembers compared
    runtime.MemStats.TotalAlloc, which every goroutine earlier tests left
    running adds to. Alone it passed; after ~1,000 earlier tests in a full
    run it was red (#6178, a bisect on mote showed no single test causes
    it). 7b9ae7cf9 counts how often the group's members are found.
  - The suite, a third commit the same day: 5a475f90c (OpenCode Go's
    endpoints) and 0706d7a35 (#6160, an image row on ChatGPT cards) each
    ran a `-run` of their own tests. TestAddSameKeyOtherFormat still
    pinned Go's old URL, and TestQuotaSpanCarriesEveryWindowField and
    TestKeptQuotaReportAsksNobody saw the new Lane and the new row, all
    red on main until 7e8214d60. Two of them had issues (#4457, #6187)
    with "still red" comments and no owner. A change to a package runs
    that package whole.
  - The guard tests in the root package: fc615339f (Alibaba Token Plan
    quota) and e174b5515 (library Variables tab) each added a
    package-level `regexp.MustCompile` and ran only their own packages.
    TestNoEagerPackageRegexps, in the root package, was red under
    v0.1.3796 and v0.1.3797 until 4847a087f moved both onto lazyre.
    Their CI runs were cancelled by the next push, and ubuntu fails at
    apt (#6198), so no run showed it. A new package-level value runs
    `go test .` at the root too.
- **11-12, criteria still missed.**
  - The same root guard a fourth day running: 36eebea13 (the Update
    pill) put `regexp.MustCompile` at package level in
    internal/update/behind.go, and TestNoEagerPackageRegexps was red on
    ubuntu and macOS CI at 67f61bd78 until 10e783080. Its body names no
    run of `.`. The rule has been written here on 11-10 and 11-11 and is
    still missed, so writing it again won't hold it: the guard should
    run where every chosen-package run already goes (a vet check, or
    the same test in each package that has package-level vars), not only
    in the root package.
  - A test swapped time.Local (4028687b8), and main went red under
    -race; see "A test never swaps time.Local" above.
  - The same guard again three hours later: 4519eea71 (Settings ›
    About's Report a problem) put a package-level regexp in
    internal/gui/feedback.go, and gave GET /api/feedback no command and
    no notYet line, so TestNoEagerPackageRegexps and
    TestEveryRouteHasACommandOrIsNamed were red at d0a4e0be6 until
    f97415bc0. Its body lists the gui tests and vet it ran, and neither
    guard's package. A new package-level var runs `go test .`; a new
    route runs internal/cli's parity test.
- **Done right on 10-17.** The retire stack (0f1666b38 to de671719a)
  diffed staticcheck's U1000 against its base for dead code, b98f906b1
  was red on the old code for an own-account gap, and 8 subscriptions
  were moved with real accounts. Kiro, Factory and Zed were not, and
  Qoder answers 401 (plugins#181); both said. df3734b95 (#2627) fixed
  the shared reader with the reporter's fish_variables and ran real fish
  3.7.1. f550d3699 tried Bun's Claude Code with a real SOCKS proxy.
  48aae83b8 found an unmasked route to Jev by reading callers. 9f9bfc160
  scoped Codex's 272K window to Codex and checked an OpenAI model
  unchanged. c4aeaec74 checked npm before the Cursor min.
  2042123f4 (timonwong) and 992f288e2 (JJ-Lin) were ported with credit.
- **Done right on 10-16.** a751f5282 (#2567) found its 408 by injecting a
  250ms stall and was red with renewal turned off. 9b04c4e20 ran a real
  Codex Pro sign-in on every protocol. #1717 (deephbz) took four review
  rounds, each with a probe that lost a 1h cache write, and diffed the
  outgoing request against main through a recording proxy. 6641e1b0a
  fixed #2449 at its cause, red 3 of 3 with an injected pause.
  0cad76cd9 ran the whole GUI suite and filed each red. a15e57656 read
  OpenPencil's keys from its own source and ran its real binary.
  ee2a7c7e8 (ggbdpq) has a test red on the old code.
- **Done right on 10-14.** 86cc7ac56 (hhh2210) ran -race -count=20 and
  says its ordering was injected, not captured. f074996d9 named its load
  red's issue (#2066). bfef5d51b checked the reverted page's screenshots
  against v0.1.1231's assets. 21aa40285 ran the fixed test in four time
  zones.
- **Done right on 10-11 to 10-13.** 2dcc51417 found 3ccbe6bc1's reds the
  same evening. 5241de909 guards a caller's image URL at the one place all
  three draw paths download it. 86b2f4d05 fixed revoked sign-ins for every
  built-in at once. 8179ce768 says what it broke by hand. 097479dab fixed
  #1543 at its cause, and ebbe76420 forced each failure by hand instead of
  rerunning.
- **Done right on 10-10/10-11.**
  - 9e35eef3 fixed #1469 half an hour after it was filed, reproducing CI's
    numbers with an injected pause.
  - ede1e32a (#1508) audited all six pickers of the same kind and added a
    guard test that every one can be narrowed.
  - 64262a19 (Prime Agent), 9c11abbd (jcode) and b72e7d4a (#1539) were tried
    with the real binary in a sandbox, and each says what was broken by hand.
  - #1197 scoped Kimi's sampling rule to Kimi Code's endpoints and models.
- **Done right on 10-09/10-10.**
  - 229ce19a, 570b8fdb and ab72c8eb reproduced CI flakes by injecting a
    delay and fixed their cause.
  - 7f62dbf3 named the siblings the reporter's patch missed (Cursor, Muse,
    GUI, CLI) and covered them.
  - 9b51428e found the same hold-out in Factory's list.
  - #1454 stayed open until the reporter confirmed.
  - #1213 and #1282 merged at the reviewed head with the record seconds
    before.
