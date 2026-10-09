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
| Checks cover the edges the change touches. | For numbers, check the boundaries. For paths, check each platform (macOS, Linux, Windows). For concurrency, check races and stale state. Pick checks by risk. |

## Tests

| Rule | What a reviewer looks at |
| --- | --- |
| A fix comes with a test that fails without it. | The reviewer puts the old code back and runs the test, and the test must fail. A PR description says what it failed with. |
| Tests use real inputs. | Fixtures look like real requests, real serialized output and real files on disk. That includes missing fields, empty values and defaults. |
| Tests check behavior, not implementation. | Assert what the user or caller needs. Formatting changes and internal refactors shouldn't break a test, unless exact bytes are the requirement. |
| A test that fails on `main` is a bug to fix now. | "Fails the same on origin/main" is not a baseline, and neither is "fails under the full run, passes alone". Fix it at its cause, or open an issue naming the cause, in a commit of its own, before shipping on top of it. From 10-05 to 10-07 dozens of commits shipped past red tests. Some were real bugs: a data race (a45b3e09), test order (bfe8bcaf), and click-scroll red since 5d871723 while dcbae2ef, 76d82528 and b2345078 shipped past it. Run `go test -race -count=20 -run X` before calling a Go test a flake. |
| Tests stay out of the real machine. | Run them under a temp HOME with Go's caches pinned (see the snippet in [provider-plugins.md](subsystems/provider-plugins.md)). Never touch a real agent's config or `~/.config/magpie`. An agent's variable goes in `agentenv.Vars`, so the sandbox clears it. |

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
   failure.
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
- The release in `yetone/magpie-releases` has all 16 assets.
- That release is marked Latest, the highest version.

Check `git tag` for the next free version first, because releases can be
made from more than one place. A PR that only adds tests doesn't get a
release of its own.

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
