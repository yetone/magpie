<!-- reviewed-through: 8b14afea (2026-10-07 22:09 +0800) -->
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
- Seen 1× (2026-10-05).

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
- Seen 1× (2026-10-06).

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

## The user's own files and settings

**Never let magpie's default override a value the user set themselves.**
- CLAUDE_CODE_AUTO_COMPACT_WINDOW overrode the user's own autoCompactWindow
  for ~150 releases (602d4ec6).
- `magpie claude model default` deleted the user's own ANTHROPIC_BASE_URL and
  dropped the stash for 12 days (fe2603ff).
- Before writing a key, list everywhere the agent reads that setting from.
  Test with user-owned values present.
- Seen 1× (2026-10-05).

**A failed read, parse or hash means "unknown". It never means "empty" or
"equal".**
- relink's `fresh()` treated two unreadable folders as equal and deleted the
  copy (9fb9a8ab).
- readLogins returned an empty list on a parse error, which the next add or
  sign-out wrote back as "no accounts" (d5619d5a only retried for 15ms).
  Fixed in b363327d: the accounts read before are kept, and a file that
  doesn't parse is copied aside before it is written over.
- Done right on 10-07: d40fee80 (#1097) backs up Pi's mcp.json before
  moving it whole, and doesn't move it when the backup fails. 2c2137be
  deletes a dsh session only under its own folder, to the trash, with
  Restore, and was tried with real dsh.
- Seen 1× (2026-10-05).

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

**Take another app's field types from that app's own data or validator.**
- 84554e57 wrote updatedAt as milliseconds, without trying the real Claude
  Desktop. Its skills page hung for 2 days (#863, 50d6c9a3).
- For layered vendor configs, read the vendor's merge code first. 9761248d
  guessed dsh's merge rules (#838).
- 10-06: #966's review asked that Codex's limit_reached count as "held",
  without reading the app. 38 minutes later #996 read ChatGPT.app's app.asar.
  Its composer only stops on rate_limit.allowed === false, so #996 undid part
  of #966. #996 and e2f1c6ba are how to do it: e2f1c6ba sealed its fixture
  the way OpenHanako 1.0 seals provider-catalog.json and was checked on a
  copy of the owner's real ~/.hanako.
- Seen 2× (2026-10-05, 2026-10-06).

## Verification

**"Not tried with the real thing" means don't build more on it, and don't
tell users it works.**
- Cursor Private Inference shipped four times without the real app. The
  real client's key and User-Agent never reached the new path (387afd07).
- Add to PATH was "not tried on Windows" and failed for the site's
  magpie-windows-amd64.exe for 8.5h (90b1ac79). The Windows box was there.
- The Trae CN check-in shipped on three guesses (#808, #821). Design the
  experiment that tells the guesses apart, or ask the reporter to run it.
- A platform report (WSL, Windows) is checked on that platform: ssh to the
  box. A GOOS build is not a test.
- 10-07: b2345078 (#1051) let listed web pages call the gateway with a key.
  Its tests called Handler without lanGuard, never the running server.
  There every such call got 401 until 7837f90d, 15 minutes later. A test of
  a gateway guard goes through the real server stack, or the built binary.
- 10-07: #1050's WSL path (a running and a stopped distro) and cda11d84's
  Windows data folder (tangle778 on X) were checked by unit tests only. The
  Windows box and its WSL2 distro were there. cda11d84 also misses a Desktop
  or Documents that OneDrive moved (the Windows 11 default). It switches an
  existing Downloads-portable user who also has an installed copy to the
  installed data, and says so only in the log.
- Done right on 10-07: 6f65ffb5 checked the WSL CLIs on the Windows box's
  WSL2 distro. a24458ca (#1063) said plainly that no real Copilot Business
  seat was tried, built its fixtures from VS Code's tests and live Pro+
  bytes, and kept the issue open.
- Seen 2× (2026-10-05, 2026-10-07).

**A GUI change runs its tests in both Chromium and WebKit, at narrow widths,
in every language.**
- ~40 GUI commits ran WebKit only, without saying Chromium wasn't run.
- 3ec6b4da squeezed key names to "…" (#841). 653cb8ee shrank the ZCode
  question to 0px at 440px.
- 43a65950 broke gui-ja/gui-de placeholders for 3.5h. Run gui-ja and gui-de
  for every new `t()` string.
- When you reword a string, `git grep` the old text under internal/gui/tests.
  4a87b306 left routing-served red for 8 releases.
- Test the empty case: 6fc0afe8's price editor couldn't price a model with
  no list price (4c001cef).
- 10-06: 4c170cba's Japanese string had a third `{agent}`, and gui-ja was red
  until 34bfb9ca. 29e6d148 (#929) kept a clicked chip in view in Chromium
  only; c6e318e1 did WebKit 40 minutes later.
- Seen 2× (2026-10-05, 2026-10-06).

## Concurrency and tests

**Never send on a channel, or call anything that can block, while holding a
lock the receiver needs before it reads. Set shared state before the
handoff that lets another goroutine look at it.**
- 85fd35ab's emit sent to a full 64-slot segment while holding r.mu, and the
  reader takes r.mu first. It hung macOS -race CI for 20 minutes
  (4fb8e887). -race -count=50 locally didn't catch it. A test that fills the
  buffer first catches it every time.
- The Claude subscription run recorded run.tools after continueWith had
  already answered the agent, so a quick turn read the old tools
  (TestToolSearchLoadKeepsTheRun, c8690ca0).
- Seen 1× (2026-10-06).

**A fake or child process a test starts ends when the test does.**
- TestClaudeSignInByPaste's fake `claude auth login` polled every 50ms for 2
  days after its test, waiting on a file in a TempDir that was gone
  (38a0f470). On 10-07 #1060's kept fake Claude Code runs end with their
  test.
- After a test times out, check `ps` for its binary. An orphaned gateway.test
  spun at 96% CPU for 30 minutes.
- Size -count to -timeout. A timeout panic is not a hang until its stacks
  show one.
- Seen 1× (2026-10-06).

## Red tests and releases

**Flaky tests: find the cause.** "A test that fails on main is a bug to fix
now" is in the code standards. What the reviews found about finding the
cause:
- TestPluginSOCKSProxy timed out at 60s on ubuntu CI and was rerun as "load
  jitter". It takes 3s on a Mac every time, and a dead local proxy should
  refuse in milliseconds, so something on that path waits. Its cause
  isn't found yet.
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
- Done right: 2bd49cc5's race was fixed at its cause in 25 minutes
  (28049bd2, -race -count=40 -cpu 1,2). 60f87223, b1cba654, 443f1cbc and
  395c4bc5 fixed flakes at their cause instead of retrying. 06420a71 fixed
  the macOS first-run check at its cause.
- Seen 3× (2026-10-05, 2026-10-06, 2026-10-07); the rule itself is in the
  code standards.

**Don't tag until a CI Test run on that commit, or one containing it, has
finished green. A cancelled run is not a pass.**
- On 10-05 nearly every CI run was cancelled by the next push. Linux-only
  TestCursorLocal failed from v0.1.877 to v0.1.892 (b7fef387).
- Batch fixes rather than tagging every commit. 294 tags in 51 hours left
  no run to finish.
- Before naming a version to a reporter, check it has all 16 assets.
  v0.1.956 was announced on #856 but never built.
- Before listing a package in market.json, check it resolves with
  `npm view`. 2f135ed4's middleware packages reached npm 5h after the
  release. Done right on 10-07: 35485f2d, 01aaa8e9 and 92e4fc32 each checked
  the package on npm first.
- Seen 1× (2026-10-05).

## Merging and closing

**Never merge a PR whose own new test fails, or with known problems a
review listed.** (Merging only the reviewed head, and writing what was run
before the merge, are in the code standards.)
- #885 and #826 merged with their own new test failing. #846 merged with
  four known-wrong translations. Fix or file the problems first.
- Seen 1× (2026-10-05).

**Keep the issue open until the reporter's case works. Reopen when they say
it doesn't.**
- On #876 the reporter couldn't reopen it themselves and had to file #888.
- Don't close as not-planned what the owner hasn't ruled on. #769 and #871
  were reversed.
- Done right on 10-07: #1050, #1053, #1063 and #958 shipped a fix and stayed
  open for the reporter.
- Seen 1× (2026-10-05).

**One commit, one goal.**
- 653cb8ee bundled ZCode sign-in with WSL session deletion.
- 5464a362 capped six pages at 1200px when #860 asked for a speed display.
- A fix found on the way goes in its own commit. 1e0829b1 hid the ja/de fix
  inside a Usage change, beside two issues (#925, #928).
- 76dc5aed bundled a Routing animation fix, a scrollbar fix and bringing the
  GUI tests up to date.
- Seen 2× (2026-10-05, 2026-10-06).

## Moved to the code standards

These were seen on three or more days. The rules and their evidence are in
[docs/code-standards.md](docs/code-standards.md); a review still checks
each one.

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
