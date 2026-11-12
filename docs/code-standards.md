# Code standards

These rules come from what PR reviews keep asking for. A reviewer checks
each one, and a PR that doesn't meet one gets a comment saying which. How to
describe and review a change to a subsystem is in the
[subsystem references](subsystems/README.md).

## What a change contains

| Rule | What a reviewer looks at |
| --- | --- |
| A change does only what its goal needs. | Every change in behavior has a reason. Unrelated refactors, extra features and policy changes go in a PR of their own. |
| A fix covers exactly the cases it means to. | Which cases change, and which neighboring ones stay the same. Different errors are not handled as one. |
| A change holds on the whole path. | Follow it from the user's input to the end result, through the callers, the state it changes and the side effects. One correct function doesn't make the whole operation correct. For example, an agent's model picks are saved under one id and read under another (#926). An update button doesn't update the binary that actually runs (#930). |
| A fix reaches every sibling of the bug. | Grep for the same pattern: every agent, subcommand, guard of the same shape and caller of a changed predicate gets the fix in the same change. That includes the other branches of the same operation and the other messages that name the same endpoint or rule. On 2026-10-05 six fixes needed a second release because they covered only the case in the report. On 10-06 #933's Codex models followed an hour after its sign-in. On 10-07 cd8d64a9 (#1059) offered sub2api's balance URL in one hint, while the error shown after a failed balance read still names only new-api's. Pi's whole-file move of mcp.json had no backup until d40fee80 (#1097), though the merge branch beside it already had one. |
| A change re-derives everything built on what it changes. | When a change alters a formula, a list, a layout breakpoint or the timing of an async step, find every caller, saved copy and test that assumed the old meaning, and run it. 12cba74a kept Zed's old clamp under a new max_tokens. 5d871723 (#860) moved the Requests page's columns to 1150px, and click-scroll was red on main for most of 10-07 (82a36a35). ee2588c2 added an immediate re-read, and the test's assertion about the state in between became a race on Linux CI (3e38bd76). |
| A failed read, parse or hash means "unknown". | It never means "empty", "none" or "equal". Keep what was read before, copy a file that doesn't parse aside before writing over it, and fix the shared reader rather than writing a strict one beside it. A list that guards access (an allow list, a key limit) fails closed when its file can't be read. Test with the file unreadable. readLogins wrote a parse error back as "no accounts" (b363327d), and agent/stash.go had the same bug two days later (270b5257). On 10-12 7a9490638's IP allow list read an unreadable settings.json as an empty list and let every address in (7d9382db2). On 10-13 #2017's patch read an unreadable codex-autoreset.json as "no reset spent" (e297a8ab2). |
| Another app's values come from that app. | When magpie writes another app's file or speaks for it, take each field's type and value from that app's own data, binary, merge code or docs, not only its field names. 84554e57 wrote updatedAt in milliseconds and hung Claude Desktop's skills page (#863). #966 guessed Codex's limit_reached, which #996 undid after reading ChatGPT.app. On 10-13 accc8c090 copied Claude Code's OAuth client names but sent its own redirect_uri (127.0.0.1, where Claude Code sends localhost); e0ecf0161 read the binary and fixed it. |
| Nothing that can block runs under a lock its reader needs. | Never send on a channel or call anything that can block while holding a lock the receiver takes first. Set shared state before the handoff that lets another goroutine look at it, and a struct copied by value that holds a map is not a copy. A fake server's handler runs on many goroutines: guard what it records with a lock, and count only the calls the test makes, since background reads reach it too. A change that starts a goroutine or timer runs `-race` before it is pushed. 85fd35ab hung macOS -race CI (4fb8e887). 01e66e0f's marshal wrote into a shared map (15afd988). On 10-11 7cba316b9's turn-done timer read fields on its own goroutine and went red under -race (a2afc1828). On 10-13 TestKeptLoginStandsAtItsPlace's fake Codex appended from two goroutines (a75de4c21). |
| magpie's default never overrides a value the user set. | Before writing a key, list everywhere the agent or magpie reads that setting from, and test with a user-set value present. A migration that rewrites a setting names the users who set it themselves, and leaves their value alone or says in the release why not. CLAUDE_CODE_AUTO_COMPACT_WINDOW overrode the user's own autoCompactWindow for ~150 releases (602d4ec6). On 10-10 four fixes (603dc0c4, 5aca7d09, c65d9fb4, d9bf7c1c) each started without a test holding a user-set value. On 10-17 5a2414acd turned every unmarked Essentials preset into All, including one a user picked with `magpie plugin preset essentials` (#2749). |
| A size budget is met, not raised. | A change that reaches a budget (app.js, the core scripts) moves something out first: to a plugin page, a lazily loaded file like `updatesource.js`, or code it replaces. A raise names what the owner agreed to, not "nothing left to move". Comments are not what gets cut: the budget counts code. After a rebase, run the budget test again at the pushed head. app.js's budget rose four times in 2.5 days to 1,269,000 (85eef5d87, #2710). On 11-12 coreScriptBudget rose four times in one afternoon, 1,089 bytes, with nothing moved out (d96403141, 05cadcafd, 4519eea71, 09e2eef0d). |
| Checks cover the edges the change touches. | For numbers, check the boundaries. For paths, check each platform (macOS, Linux, Windows). For concurrency, check races and stale state. Pick checks by risk. |

## Tests

| Rule | What a reviewer looks at |
| --- | --- |
| A fix comes with a test that fails without it. | The reviewer puts the old code back and runs the test, and the test must fail. A PR description says what it failed with. A test named for a guarantee goes red when that guarantee is broken by hand: #1318's TestHistoryWriteHoldsNoWaiter passed with the waiter held (#1358). On 10-12 bbe8825c0's memo test passed with its eviction reversed (9dd5917a4), and on 10-13 e21b1dbc1's tests passed with is_error dropped (cc93b566e). |
| A test never reaches the network. | A vendor's page or API is served from httptest, built from bytes saved from the real one. A package comes from a lock file, not a registry's latest. Break the change and watch where the test's requests go. 9755201b opened ChatGPT and hung on its Cloudflare challenge. On 10-11 a library test reached the real GitHub with the icon proxy off (f04792f9). On 10-13 TestPiPackage tested npm's latest pi that minute (f230fa23b), and plugin and cli tests waited 30s on the real models.dev (bde11c778). |
| Tests use real inputs. | Fixtures look like real requests, real serialized output and real files on disk. That includes missing fields, empty values and defaults. |
| Tests check behavior, not implementation. | Assert what the user or caller needs. Formatting changes and internal refactors shouldn't break a test, unless exact bytes are the requirement. |
| A test that fails on `main` is a bug to fix now. | "Fails the same on origin/main" is not a baseline, and neither is "fails under the full run, passes alone". Fix it at its cause, or open an issue naming the cause, in a commit of its own, before shipping on top of it. From 10-05 to 10-07 dozens of commits shipped past red tests. Some were real bugs: a data race (a45b3e09), test order (bfe8bcaf), and click-scroll red since 5d871723 while dcbae2ef, 76d82528 and b2345078 shipped past it. Run `go test -race -count=20 -run X` before calling a Go test a flake. |
| A red's issue is read to its last comment, and a patch on it is taken first. | Before filing, commenting on or shipping past a red, search its test's name and read every comment. "Seen again" is not work on it: take it, or say who has it. A patch posted there is reviewed and ported before anyone writes another. #1794 got four "seen again" comments before anyone took it (10-12). #2444 had ump45nose's patch for 22 hours while two sessions commented, until CI went red at f191d6423 (10-17). JoeyWu-tech's #3568 patch, with red-on-old tests, waited three days until 0a7633136's CI went red and 11c427eaa ported it (10-28). |
| Tests stay out of the real machine. | Run them under a temp HOME with Go's caches pinned (see the snippet in [provider-plugins.md](subsystems/provider-plugins.md)). Never touch a real agent's config or `~/.config/magpie`. An agent's variable goes in `agentenv.Vars`, so the sandbox clears it. |
| Whatever a test starts ends when the test does. | A fake process, a goroutine or an async refresh a test sets going is stopped or waited for before the test returns, or it writes into the next test's folder ("directory not empty") or spins for days. A package that isolates itself in its own `TestMain` instead of `testenv.Run` sets what testenv sets, `testenv.Marker` included. TestClaudeSignInByPaste's fake polled for 2 days (38a0f470). On 10-11 a CLI-identity ask outliving its test broke the next one (#1524): 4ee3a76b fixed it in internal/gateway only, 9c377231 for every package through the marker, which the gateway never set (a349de82). |
| A timing test leaves room on both sides. | Never bound a test by wall-clock time a loaded runner can exceed, and keep a threshold classifier's fixture far from every threshold it has. Reproduce a timing failure by injecting the delay (a held write, a late first arrival), not by rerunning until it fails, and list every timing assumption in the test when fixing one. 319b72c7 fixed one of two loops sharing a 2000-line bound and the other went red 9h later (229ce19a). On 10-11 TestMaxRPM's flake was a real gap: a first request that reached the vendor late let the vendor see one too many in its minute (ce1ecaa4), and the queued-stream tests timed the client's clock under CPU starvation (3f4287e6). |

## Acceptance criteria

A change is merged or released only when it shows each of the following.
Say which ones were run and what they printed. A check that wasn't run is
listed as not run, never left out.

1. **A test that fails without the change.** Put the old code back and run
   the new test. It must fail with a real `FAIL` that names the behavior. A
   compile error or a skipped test doesn't count. Say which test fails and
   what it failed with. Then restore the change and check the test passes.
2. **Tests in a sandbox.** Run Go tests under a temporary HOME with Go's
   caches pinned: `GOPATH`, `GOMODCACHE` and `GOCACHE` taken from `go env`
   before HOME is swapped. Remove the HOME afterwards with
   `chmod -R u+w "$h"; rm -rf "$h"`, because the module cache in it is
   read-only. The snippet is in [provider-plugins.md](subsystems/provider-plugins.md#verification).
   Without the pinned caches, every run downloads 1–2 GB into a new temp
   HOME. A test never reads or writes a live agent config (`~/.codex`,
   `~/.claude`, `~/.claude.json`, `~/.gemini` and the rest) or
   `~/.config/magpie`. Packages set this up in their `TestMain`, most with `testenv`.
3. **The build and the suite.** Run `gofmt -l` on the changed files (it
   prints nothing), `go vet ./...`, `GOOS=linux go vet -tags nogui ./...`,
   `GOOS=windows go vet ./...`, `go build`,
   `GOOS=linux go build -tags nogui`, `GOOS=windows go build` and
   `go test -tags nogui ./...`. A cross-OS build doesn't compile
   `_test.go` files; vet does. On 10-08 bbf99143's test called
   `syscall.Getsid`, which Linux's syscall package doesn't have, and
   bb8a3e30 and e7de897f shipped unformatted. In a chain of commands, set
   `set -o pipefail`, so that `go test | tail && git push` stops on a
   failure. Run `./...`, not the packages you think you touched: the
   repo's guard tests live in packages a change doesn't touch.
   TestFolderVarsAreListed (internal/agent) fails when a new agent
   variable isn't in `agentenv.Vars`, TestNoCommandBypassesProc
   (internal/proc) when code calls `exec.Command` instead of
   `proc.Command`, and an output format change breaks another package's
   test that pinned the old text. On 10-13 four commits ran chosen
   packages and turned main red this way: 6fa3109ea (494609866),
   43d8a5633 (24146c031), 5617281f5 and cdb7b5ff7 (c6de0d2a5).
4. **GUI tests in both engines.** A change to `internal/gui/assets` runs the
   Playwright tests it touches in Chromium and WebKit (`make test-ui`, or
   `BROWSER=webkit node --test …`). The suite must also pass in Chinese,
   English, and `gui-zh-tw`/`gui-ja`/`gui-de` (every string has its
   Traditional Chinese, Japanese and German, with the same placeholders).
   CI doesn't run this suite, so it is run locally. Run the whole suite, not only the touched page's tests, when a
   change adds an API the GUI calls, a selector or class other pages share,
   a CSS feature older WebKit lacks (`:has()`), or a layout breakpoint. On
   10-07, 479865bc, 134d388b, 61838f7c and 51626258 cleared four tests left
   red by commits that had run only their own page's tests. On 10-08
   aff4f9f2's zh-TW was built before 685fa5dc's and 9dc0121f's strings
   (b1e795e5 added four 10 minutes later), and usage-ledger was red for
   about 7 hours after 586f2bce. When you reword a string, `git grep` the
   old text under `internal/gui/tests`, and test the empty case (a model
   with no list price, an account with no usage).
5. **Real use where possible.** A change to a provider, subscription or
   agent is also checked against the real thing: a real account or key, the
   agent's real config format, the vendor's real reply. Do this in a sandbox
   HOME with copies of the credentials. Never touch the live gateway or a
   running magpie. If a real check isn't possible, say so. What wasn't
   tried with the real thing isn't built on and isn't told to users as
   working. A platform report (Windows, WSL, Linux) is checked on that
   platform; the Windows box and its WSL2 distro are there, and a GOOS
   build is not a test. A test of a gateway guard goes through the real
   server stack or the built binary (b2345078 called Handler without
   lanGuard, and every such call got 401). When the guesses about a vendor
   differ, design the check that tells them apart, or ask the reporter to
   run it. On 10-08 bbf99143 said "not tried on Linux" and
   broke Linux vet; #1185's Linux-only reap failure was reviewed on macOS
   only. a24458ca (#1063), 5d0c1dca (MSIX not tried, said plainly) and
   #1328 (on the Windows box, six break checks) are the model.
6. **The UI rules** under [GUI](#gui) hold: `t()` strings, the app's own
   menu instead of `<select>`, no scroll on click, no colored left-border
   stripes.

A PR is merged only after a maintainer has pulled it, merged it onto
current `main` and run the checks above on the result. The merge names the
commit that was reviewed: `gh pr merge --match-head-commit <reviewed sha>`.
A push made during the review then stops the merge, rather than shipping
code nobody ran. A head pushed after the review is reviewed again, and the
comment names the new head (#974 was merged without that). Write on the PR
what was run, at which head, and then merge: a merge with no record of what
was run can't be checked afterwards (#981, #1000, #1030, #1037). On 10-07
#1045, #1046, #1047, #1049, #1057, #1060, #1061 and #948 are the model.
Each names its reviewed head and what was run, posted before the merge. A
Draft PR can't be merged until it is marked Ready for review.

A release counts only when all of these hold:

- A CI Test run on the tagged commit, or a later one containing it, finished
  green. A run cancelled by the next push is not a pass.
- The tag `vX.Y.Z` is on `main`, an ancestor of `origin/main`.
- The release in `yetone/magpie-releases` has all 17 assets.
- That release is marked Latest, the highest version.

Check `git tag` for the next free version first, because releases can be
made from more than one place. `git fetch --tags` and check the commit
isn't tagged already: on 10-12 two sessions tagged 396a6a3d4 a minute
apart, and the lower v0.1.1192 became Latest. A tag is not a release:
check the release exists with its 17 assets before naming the version to
anyone, and file a release whose build failed instead of letting the next
tag cover it. v0.1.1213 (hdiutil "Resource busy") and v0.1.1218 (notarising
lost the network) were never published, and nobody noticed the first for
1.5 hours (#1846). The same holds for a plugin version: `npm view
<package>@<version> --prefer-online` before a mover's `min` or a reply
names it. A plugin version published for a moved subscription
raises its mover's `min` to that version the same day, since
`keepMovedCurrent` installs only `min`: zed 0.1.9 (10-18) and factory
0.1.38 (10-24, raised 10-28 in 490c0a4ad) reached nobody until a review
compared every mover with `npm view --prefer-online`. A PR that
only adds tests doesn't get a release of its own.

## Reviewing a PR

These patterns come from recent reviews. A reviewer checks each one that
applies:

- **Run it.** Pull the head, merge it onto current `main` (resolving
  conflicts if needed) and run the acceptance checks. Reading the diff alone
  isn't a review.
- **Break it on purpose.** Revert the fix, or break the code it guards in
  several ways, and check that the new tests catch each break (#901 broke
  `backup.go` four ways). A reviewer may add review tests of their own
  (#829–#831).
- **Check the vendor, not the PR's account of it.** Read the upstream source
  for a vendor's behavior (#870 read Codex's
  `uses_openai_actor_authorization`). Try the change against a real upstream:
  #853 found that most Chat upstreams don't continue a trailing assistant
  message.
- **Check each platform and architecture.** An `int` conversion that is safe
  on arm64 can wrap on `GOARCH=amd64` (#877). Windows reports a refused
  connection as `WSAECONNREFUSED`, which `syscall.ECONNREFUSED` doesn't
  match (#805).
- **Follow the whole path.** Check that a pick is saved and read under the
  same id (#926). Check that a button works on the thing that actually runs
  (#930). Check that aliases and User-Agents don't collide with another
  agent's (#926). Every name in `agentenv.Vars` is treated as a folder path
  unless it is in `agentenv.NotPaths` (#922).
- **Classify narrowly.** A mark or error class covers only the case it
  describes: #873 counted every failure on a turn as "turned away".
- **A preview writes nothing.** A dry run, plan or preview has no side
  effects (#827).
- **Don't reverse deliberate decisions.** The scroll guard, the button
  cursor and other product decisions are the maintainer's to change (#820,
  #921). A PR that changes one is left for the maintainer.
- **GUI suites.** Compare pass counts with `main`. A test that fails on
  `main` too is not a baseline (see [Tests](#tests)). A PR that says its
  failures "fail the same on origin/main" gets that checked, and gets them
  fixed or filed before the merge.
- **Docs match the source.** A reference is checked against the code it
  links, and every link must resolve (#889).
- **Keep the PR to its goal.** Unrelated tests and refactors go in a PR of
  their own. Comments name functions, not line numbers. Fixture names match
  what they stand for (#901).

## Go

- Write `any`, not `interface{}`.
- Read environment variables that name a folder with `appdir.Getenv`, which ignores a relative path. A variable in `agentenv.Vars` that names no folder goes in `agentenv.NotPaths`.
- A comment says what the code is for, in a sentence or two. Name functions instead of giving line numbers, which go stale.

## GUI

- Every string the user sees goes through `t()` and gets its translations in `i18n.js`. Tests run in Chinese and English.
- Use no native `<select>`. Dropdowns use the app's own menu (`openProtoMenu`).
- A click never scrolls the page (`scrollOnPurpose`).
- Don't use colored left-border stripes. Mark state with a dot or a swatch.

## Commits

A commit message names the areas it changes, then says what the user now
sees, written as plain behavior: `gateway: a 429 that says the balance is
spent is out of credit, not a rate limit`. Give the issue number or the
reporter. The body says how it was verified, and which test fails without
the change.

The record of what was run is in the commit body, where a later review can
read it: not in push notes, a chat or only on the PR. A large commit with
no body can't be reviewed afterwards: 586f2bce changed 5133 lines with a
subject only. On 10-12 fjrtdk's ea3d480ed (+237) and liubing's 1cd814004
(+505) were applied with no record, and their review commits found real
bugs in them (1f0ff81df, 9935119f1). On 10-13 3ccbe6bc1 (plugin runtime
P0, 2137 lines) put its full-suite results in "the push notes"; it had
left two GUI tests red, found by 2dcc51417 the same evening. When a
community commit is applied without a body, the review commit says what
was run against that author's code.

One commit has one goal. Two reports fixed together are two commits,
each with its own record, so a revert takes back one and keeps the other,
and a review can tell which test guards which change. A fix found on the
way goes in a commit of its own. 653cb8ee bundled ZCode sign-in with WSL
session deletion, and 1e0829b1 hid a ja/de fix inside a Usage change. On
10-28 6e080ca04 put psikent's /v1/models tools flag and windy's routing
group Context hint, two reports in two layers (gateway, GUI), under one
subject that names only the first.

A checkout, a worktree and the logs in it belong to the session that made
them. Work in a worktree of your own (`git worktree add --detach`), never
check out, commit from or clean another session's, and after committing
check that `git show --stat HEAD` lists your files and only yours. On
10-09 2018882e carried the davsync subject but held another session's GTK
fix, taken from a shared index. On 10-12 3e88fc09d carried the auto-mode
subject but held the Get started card (#1825). On 10-17 another session
checked out origin/main in 7660399a1's worktree mid-run, and another
deleted part of 17557132c's GUI log.
