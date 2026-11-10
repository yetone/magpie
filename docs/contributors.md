# Community PRs: what is closed unreviewed, who is credited, and who is blocked

magpie merges community PRs through agents. Two things keep that from
wearing the project down: a PR that could harm users is caught before any
review of what it does, and an author who keeps sending work that costs
more to review than it gives is stopped.

## Look for harm first

Before running a PR, read its diff for anything that reaches past its stated
goal. Any one of these stops the review until the maintainer has looked:

- It changes `.github/` (workflows, actions, permissions, secrets), a release
  or publish script, the Dockerfile, or `site/`.
- It adds a network address, an upload, telemetry, or sends a credential,
  token, cookie or file anywhere it didn't go before.
- It reads or writes a credential file, keychain entry or agent config the
  goal doesn't need.
- It adds a dependency, a `replace` in go.mod, an npm install script, a
  binary, a minified or encoded blob, or code built from a string at run time.
- It adds a relay, reseller or affiliate address, or a vendor the maintainer
  removed (DimAgent).

Say which line stopped it in the review. Don't run the PR's code on a machine
with real accounts until the maintainer has cleared it.

## Close without a full review

Close with a short reason, without running it, a PR that:

- changes what the maintainer has ruled on (see "Don't reverse deliberate
  decisions" in [code standards](code-standards.md)), such as the Codex
  prompt files, which are kept byte for byte;
- is a resubmission of a PR closed for its idea, with nothing new to answer
  the reason it was closed;
- is one of several near-identical PRs opened seconds apart.

## Credit what a reporter gave

PRs are open to collaborators only, so a reporter's fix often arrives in
an issue. Whoever uses it credits it in the commit:

- A fix branch on the reporter's fork is merged with their commits as they
  are, authored by them. The look-for-harm check above applies to it
  first. What has to change goes in a commit of our own on top, and the
  issue says what changed and why.
- A fix that uses the reporter's patch, test fixture, request body, error
  text or root cause carries `Co-authored-by: <login> <email>` for them.
  Use their noreply address, `<id>+<login>@users.noreply.github.com`
  (`gh api users/<login> --jq .id`), when no other address is known.
- A report that only says something is broken is named in the body
  (`#<n>, <login>`), with no trailer.

## Blocking an author

An author is proposed for the block list when any one of these holds:

1. They have 3 or more PRs closed by a maintainer as low-value (wrong,
   untested, unrelated, machine-made filler), and fewer than 30% of their
   PRs merged.
2. They resubmitted a PR after an explicit rejection, twice.
3. They did anything deceptive: hid a change outside the PR's stated goal,
   faked a test or a verification, or anything from "Look for harm first"
   done on purpose.
4. They open PRs in machine bursts (several within a minute) that are
   low-value as in 1.
5. They re-add a removed vendor, a relay or an advertisement after being
   told no.

An agent only proposes. It writes the evidence (PR numbers, dates, what was
wrong) to the maintainer; the maintainer decides. A blocked author's new PRs
and issues are closed with: "Thanks — this account's contributions aren't
accepted in this repository." Nothing more is argued.

An author being watched (one or two low-value PRs, no pattern yet) gets a
normal review; the reviewer notes the PR here if it is low-value too.

## Lists

Blocked: none yet.

### Reviewed with extra care

The maintainer chose not to block these authors, so their PRs are still
reviewed, but more strictly than others:

- Read every line of the diff, not only the part the description talks
  about. Compare the files it touches with its stated goal; anything outside
  the goal is a reason to close it.
- Run "Look for harm first" in full, even on a PR that looks like a typo
  fix.
- Run it yourself on current `main`; don't rely on the PR's claims or its
  CI. Never merge it in the same tick it was opened, and use
  `--match-head-commit`.
- A PR that repeats one already closed is closed with a link to the first.

| Author | Evidence |
| --- | --- |
| lunar-me | #233, #234, #235 opened within 12 seconds, all rewriting `codex_prompt.md`, which is kept byte for byte. #590 resubmitted after the rejection. #592 broke a path in AGENTS.md. #590, #592 and #593 opened within 51 seconds. The same template across many repositories. |
| muzimu217 (commits as blackEvil217) | plugins#601 (11-11) added a Doubao plugin the maintainer declined on 10-06 (it needs bot-detection evasion). It reads Doubao.app's HttpOnly session cookies over CDP, clicks through Doubao's reward campaigns and schedules throwaway jobs to earn credits ("anti-abuse hardening"), copies a .dmg from ~/Downloads into /Applications and pkills the app. It also adds a third-party relay (relay.mirasim.ai), which stops a review by itself. Closed unrun. |

### Watched

Work that is useful but keeps coming back for the same gaps. Not a block
and not extra care: the review checks the named gaps before merging.

| Author | Check | Evidence |
| --- | --- | --- |
| ggbdpq | Break each fix by hand and see its new test go red. Grep for the sibling sites (other deciders, other routes, other providers, other agents) the change didn't touch. Check what a failed read becomes. | On 10-13 two of their batches needed a follow-up. 44b5b2b96 and e21b1dbc1 were merged with tests that pass on the old code. 4ed379f11 decoded `\uXXXX` at four sites but left three deciders reading the raw body, and one site had no test (0c6752dbd). #1919 (90ac1f038) mended a renamed provider's models for StepCode only; Pi, OmO and Prime Agent followed in cb003877f. #2017's patch kept an unreadable codex-autoreset.json aside but still read it as "no reset spent", so its own crash-and-restart case spent a second reset (fixed at the read in e297a8ab2). On 10-14 94e3b3f52 came from a patch marked "wip and unverified" on its branch: json.Valid let an object through to be written over, and the review finished it. Better since: eb0d5750f, ca5dc773c, f348ff2c7, 7a6b10104 and 05107bf03 each have a test red before and green after, and #2256 was a careful security report. |
