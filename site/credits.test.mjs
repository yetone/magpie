// The credits read from commit subjects (credits.mjs). Every subject here is
// one from yetone/magpie's own history, word for word.
import test from "node:test";
import assert from "node:assert/strict";
import { parse, parseBody, credit, everyone, thanks, what, wall, renderWall, logins, slug, anchors, PAGE_IDS } from "./credits.mjs";
import { authors, excluded, xAccounts, aliases, toLookUp, honoured, xOfLink } from "./credits-cli.mjs";
import { mkdtempSync, writeFileSync, rmSync, readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { join } from "node:path";

const names = (subject, opts) => credit([{ subject, author: "yetone", email: "yetoneful@gmail.com" }], opts)[0].map((p) => p.name);
const via = (subject) => credit([{ subject, author: "yetone", email: "yetoneful@gmail.com" }])[0].map((p) => `${p.name}/${p.via.join("+")}`);

test("a reporter named on Discord or X is credited, with where", () => {
  assert.deepEqual(via("library: an MCP router says which computer it is, so an agent can pick between two (packinggoodsllc_07668 on Discord)"), ["packinggoodsllc_07668/Discord"]);
  assert.deepEqual(via("gateway: a HEIC photo goes to the vendor as JPEG, or PNG with alpha (qikamatata on X)"), ["qikamatata/X"]);
  assert.deepEqual(via("redact: a decimal's digits are not masked as a number of their own (willz, Discord)"), ["willz/Discord"]);
});

// Bodies from yetone/magpie's history, word for word (wrapped as written).
const body = (subject, b, opts) => credit([{ subject, body: b, author: "yetone", email: "yetoneful@gmail.com" }], opts)[0].map((p) => `${p.name}/${p.via.join("+")}`);

test("a reporter a commit's body names is credited, with where", () => {
  // 7f11dfc77 (v0.1.4519)
  assert.deepEqual(body("gateway: a Gemini CLI sign-in goes without Claude Code's billing line, so Gemini's cache reads its prefix",
    "Reported on X by @niyezaimenglima: Claude Code on a Gemini model through\nmagpie showed a cache hit rate of 0.\n\nCause: Claude Code's first system line, x-anthropic-billing-header, carries\na cch= that changes every turn (#1600)."), ["niyezaimenglima/X"]);
  // a1f56720c, 4e7661adc, 225d521cd, cfcd6648f
  assert.deepEqual(parseBody("tail Desktop gives it.\n\nReported by 4 v5 on Discord.\n"), [{ name: "4 v5", via: "Discord", sure: true }]);
  assert.deepEqual(parseBody("Reported by ylorn. on Discord (11-06): choosing apps in Alma, a question\nabout one app was answered with another's facts.").map((n) => n.name), ["ylorn."]);
  assert.deepEqual(parseBody("Reported by 教父 on Discord.").map((n) => `${n.name}/${n.via}`), ["教父/Discord"]);
  assert.deepEqual(parseBody("Reported by MYCF711 (Discord, issue #4420).").map((n) => `${n.name}/${n.via}`), ["MYCF711/Discord"]);
  // 3e3196222: a package in the body's Not changed line is nobody
  assert.deepEqual(body("provider: Request headers to pass, the agent's own values to its provider",
    "  TestSignPassesClientHeaders.\n\nRequested by 3jRzfF52Qx on X.\n\nNot changed: omp (@oh-my-pi/pi-coding-agent 18.10.3) reads only its agent"), ["3jRzfF52Qx/X"]);
  // 5099be8b0: after a semicolon; the branch's porter is not the reporter
  assert.deepEqual(parseBody("Ported from Q呀's (qq01-hub) branch fix/7901-tolerant-model-modalities:\nFiendSaws's fix, with Q呀's shape tests; reported by FiendSaws.").map((n) => n.name), ["FiendSaws"]);
});

test("a Reported-by: trailer credits each name it holds, with where", () => {
  const got = (b) => parseBody(b).map((n) => `${n.name}/${n.via}${n.github ? "/" + n.github : ""}`);
  // fbbdf6e05, c8eb7b387, 48ae0884b
  assert.deepEqual(got("./internal/gateway suite and ./... weren't run here.\n\nReported-by: isjiajia01, sounfury\nCo-authored-by: ggbdpq <ggbdpq@gmail.com>"), ["isjiajia01/null", "sounfury/null"]);
  assert.deepEqual(got("full Go and GUI suites run after the push, under mote's suite lock.\n\nReported-by: 辰林鸭~ (Discord)\n"), ["辰林鸭~/Discord"]);
  assert.deepEqual(got("200 with 0.1.42's code (plugins f2b0dac's record).\n\nReported-by: jihuayu (plugins#461, #4004)"), ["jihuayu/null"]);
  // ff415f39c: a noreply address is the GitHub login, and is never kept
  const ff = "/responses rather than chat is not confirmed.\n\nReported-by: Mmx233 <36563672+Mmx233@users.noreply.github.com>\nReported-by: Edward4226 <Edward4226@users.noreply.github.com>\nReported-by: mahiro233 (Discord)\nCo-authored-by: Mmx233 <36563672+Mmx233@users.noreply.github.com>";
  assert.deepEqual(got(ff), ["Mmx233/GitHub/Mmx233", "Edward4226/GitHub/Edward4226", "mahiro233/Discord"]);
  assert.doesNotMatch(JSON.stringify(credit([{ subject: "x: y", body: ff }])), /@|noreply/);
  // 30a0bfe88, 708ae4144: an alias in the bracket is not a second person
  assert.deepEqual(got("Co-authored-by: mdwsk88 <11055210+mdwsk88@users.noreply.github.com>\nReported-by: mdwsk88 (dawei on Discord)\n"), ["mdwsk88/null"]);
  assert.deepEqual(got("Reported-by: Fallmonkey (GitHub MonkeyLeeT)"), ["Fallmonkey/null"]);
});

test("a body credits only the reporters, not who else it names", () => {
  // a7bd7a0fe: who diagnosed it is not who reported it
  assert.deepEqual(parseBody("Reported by Winnie7572 on X; diagnosed by tkhs101 in #7496.\nCo-authored-by: ggbdpq <ggbdpq@gmail.com>").map((n) => `${n.name}/${n.via}`), ["Winnie7572/X"]);
  // 8b70b7d97: the alias in brackets is the same person, not a second one
  assert.deepEqual(parseBody("Suggested by @faithleysath (matrix0487 on Discord): the usage ledger now\nkeeps what each request received back (down_bytes)").map((n) => n.name), ["faithleysath"]);
  // 72109ea0e, 243594410, a4916aec1: who confirmed, reproduced or filed it is not the reporter
  assert.deepEqual(parseBody("its own means every model, as before. Reported by willywang, confirmed\nagain on v0.1.1320 by LoKi (lokic7123-star).").map((n) => n.name), ["willywang"]);
  assert.deepEqual(parseBody("400 \"invalid tool call arguments\". Reported by xiaobaifly7 and reproduced\nindependently by Shane-Meng (\\n\\n\\n@pytest.mark…).").map((n) => n.name), ["xiaobaifly7"]);
  assert.deepEqual(parseBody("Reported by ChangQiu on Discord and filed as #4853 by ZyphrZero. The\nwithoutLiteMetadata patch and its two tests are ported from").map((n) => `${n.name}/${n.via}`), ["ChangQiu/Discord"]);
  // e50d73aa3, 572a1757c: everyone in the list, nobody after it
  assert.deepEqual(parseBody("Reported by TimWongUp, nativeas, tianhao2316-beep, mxl2498, cunnak and\nTsuki-hash, on Windows and macOS.").map((n) => n.name), ["TimWongUp", "nativeas", "tianhao2316-beep", "mxl2498", "cunnak", "Tsuki-hash"]);
  assert.deepEqual(parseBody("Reported by OldSuns, ggggggggggz and track23; the fix is skygunner's branch fix/3136-agent-magpie-preview-sweep").map((n) => n.name), ["OldSuns", "ggggggggggz", "track23"]);
  // c30d3b7b6, 19f6ce16a, e50d73aa3: the owner, a phrase mid-sentence
  assert.deepEqual(parseBody("couldn't even pause on its own. Asked by the owner: is that by design?\nOnly the lazy first load was."), []);
  assert.deepEqual(parseBody("- TestHubSearchOffGivesToolsOnDemand (gateway, fake hook): hub/claude\n  and hub/claude~<project> list find_tools and call_tool; asked by the\n  bare id; codex keeps every tool;"), []);
  assert.deepEqual(parseBody("cleans it with a bare `defer os.RemoveAll(tmp)` (internal/agent/preview.go).\nTwo consequences reported by OldSuns: when the removal fails"), []);
  // 8031edc78: the maintainer is nobody
  assert.deepEqual(body("agent: AtomCode support — one provider account, one model table per catalog model, as its own sign-in writes (#736)", "Reported by yetone on #736. go vet, builds (darwin, GOOS=linux, GOOS=windows) and go test -tags nogui"), []);
});

test("a body's credit keeps every rule a subject's has", () => {
  const c = { subject: "gateway: a Gemini CLI sign-in goes without Claude Code's billing line, so Gemini's cache reads its prefix", body: "Reported on X by @niyezaimenglima: Claude Code on a Gemini model through\nmagpie showed a cache hit rate of 0.\n", v: "0.1.4519" };
  assert.deepEqual(wall([c]).map((p) => [p.name, p.via, p.releases[0].v]), [["niyezaimenglima", ["X"], "0.1.4519"]]);
  assert.deepEqual(wall([c], { exclude: ["NiYeZaiMengLiMa"] }), []);
  // an X handle a GitHub profile names is that GitHub person, unless two do
  assert.deepEqual(wall([c], { xAccounts: { someone: "niyezaimenglima" } }).map((p) => [p.name, p.github]), [["someone", "someone"]]);
  assert.deepEqual(wall([c], { xAccounts: { someone: "niyezaimenglima", other: "niyezaimenglima" } }).map((p) => p.name), ["niyezaimenglima"]);
  // an agent named as a reporter is still an agent
  assert.deepEqual(body("x: y", "Reported by Codex."), []);
  // a body name lets a lone bracketed subject name be trusted
  assert.deepEqual(credit([{ subject: "routing: the group being edited reads as selected (Jiabubu)" }])[0], []);
  assert.deepEqual(credit([{ subject: "routing: the group being edited reads as selected, its Save in reach (Jiabubu)", body: "Reported by Jiabubu on Discord (#feedback).\n" }])[0].map((p) => `${p.name}/${p.via.join("+")}`), ["Jiabubu/Discord"]);
});

test("several people in one bracket are each credited", () => {
  assert.deepEqual(names("masking: a record names the provider the request went to, not the group's first member (Max You and Linka on Discord)"), ["Max You", "Linka"]);
  // the first is bare, so it needs naming elsewhere: here a commit that
  // credits TJHHHH on Discord
  const commits = [
    { subject: "codex: the restart advice after a model or catalog change names the app-server codex sessions share (\"codex app-server daemon restart\"), on Windows and WSL too (TJHHHH, luci on Discord)" },
    { subject: "codex: a model picked in the Codex app is the one it runs (TJHHHH on Discord)" },
  ];
  assert.deepEqual(credit(commits)[0].map((p) => p.name).sort(), ["TJHHHH", "luci"]);
  assert.deepEqual(credit(commits.slice(0, 1))[0].map((p) => p.name), ["luci"]);
});

test("a lone bracketed handle is credited when shaped like one or named elsewhere", () => {
  assert.deepEqual(names("guard: the local masking model reads text a third faster on amd64 (mrwl666)"), ["mrwl666"]);
  const lone = { subject: "gui: Busy models last takes a custom threshold, 1 to 9999 (axelburks)" };
  assert.deepEqual(credit([lone])[0], []);
  const both = credit([lone, { subject: "search: searchers picked to follow the one named, tried in their order when it fails (axelburks on Discord)" }]);
  assert.deepEqual(both[0].map((p) => p.name), ["axelburks"]);
});

test("a quote after the name is not part of it", () => {
  assert.deepEqual(names("zen: Test with the public key asks each API for a free model Zen serves on it, and leaves out an API no free model is on (z'x on Discord; #2385)"), ["z'x"]);
  assert.deepEqual(parse("zen: Test with the public key asks each API for a free model Zen serves on it, and leaves out an API no free model is on (z'x on Discord; #2385)").refs, [{ repo: "yetone/magpie", n: 2385 }]);
});

test("the maintainer, the owner and descriptions of someone are nobody", () => {
  assert.deepEqual(names("plugins: the GitHub list waits up to 3s for a repository's https icon fetched the first time (yetone: 非官方插件显示不出来 logo)"), []);
  assert.deepEqual(names("tray: with the menu bar's magpie icon off, a click opens the panel on the tab left last, not Allowances (the owner: 每次都会自动跳转到 allowances，我记得之前是可以的)"), []);
  assert.deepEqual(names("gui: a Claude account saved in magpie can be removed while Claude Code is signed out (𝕏 user on Discord: a banned account logged out, and its line in Providers had nothing to remove it with) — saved accounts are listed"), []);
});

test("code, labels and words beside an issue number are not people", () => {
  for (const s of [
    "plugin: gateway key limits are the Key limits plugin on the gateway's budget point (P3)",
    "routing: picking Live or a day keeps the page where it is, and a list with no requests says what it is for (Image #59) — a day or Live picked",
    "gui tests: the Routing page's tests pick a day from the date bar, not the By request button (#860 flake) — remote-group-served's zh case",
    "mcpauth: a sign-in asks for offline_access when the authorization server offers it, so Vercel's MCP stays signed in (#615: \"vercel老是掉\"; Neon, Notion and Supabase stay) — mcp.vercel.com's",
    "codex: a client's access_programs and the account's extraBody reach the ChatGPT backend, and its refusal comes back as it said it (magpie-releases#18)",
    "efd1b697e sessions: a cached file's calls are read into a slice made for them",
  ]) assert.deepEqual(names(s), [], s);
  // a word beside a credit is a person only when named somewhere else
  assert.deepEqual(names("agent: Claude Desktop on magpie keeps the language picked signed in with Anthropic (memory, Osal on Discord)"), ["Osal"]);
});

test("issue and PR numbers are kept for their authors, in either repository", () => {
  assert.deepEqual(parse("redact: a file named like an address (…@2x.jpg) is not masked as an email (#3029)").refs, [{ repo: "yetone/magpie", n: 3029 }]);
  assert.deepEqual(parse("redact: Codex's tool server names, stream_options and snake_case words go as written (#3143, #3144)").refs.map((r) => r.n), [3143, 3144]);
  assert.deepEqual(parse("provider: Devin's plugin min is 0.1.30, its opt-in cache keepalive (plugins#592)").refs, [{ repo: "magpie-community/plugins", n: 592 }]);
  assert.deepEqual(parse("provider: WorkBuddy's mover asks for opencode-workbuddy-auth 0.2.9, whose declared models take images (plugins #572)").refs, [{ repo: "magpie-community/plugins", n: 572 }]);
  const s = "redact: Codex's tool server names, stream_options and snake_case words go as written (#3143, #3144)";
  const issueAuthors = { "yetone/magpie#3143": "hhh2210", "yetone/magpie#3144": "dependabot[bot]" };
  const got = credit([{ subject: s, author: "yetone" }], { issueAuthors })[0];
  assert.deepEqual(got.map((p) => [p.name, p.github]), [["hhh2210", "hhh2210"]]);
  // a name and an issue in one bracket: both
  assert.deepEqual(names("provider: a Max output kept for a model no longer listed doesn't block the editor's Save (#3305, LoKi)", { issueAuthors: { "yetone/magpie#3305": "LoKi" } }), ["LoKi"]);
  assert.deepEqual(names("sessions: Terminal is opened through proc.Command, as every command magpie runs is (spotted by @hezhizhen in #103)"), ["hezhizhen"]);
});

test("a platform after a \"by @\" handle is where, and none leaves it a GitHub login", () => {
  // 9661b8f30 (v0.1.272): the bracket is after " — ", where an older
  // subject's body went, and still says who reported it
  assert.deepEqual(via("gui(routing): on a wide window the request list and Accounts and keys sit side by side at the same width — they were 1.25fr to 1fr, the left wider than the right (reported by @duanckham on X)"), ["duanckham/X"]);
  // ebdf44821 (v0.1.187)
  const s = "sessions: Terminal is opened through proc.Command, as every command magpie runs is — os/exec there failed TestNoCommandBypassesProc and kept main's CI red since 78fe45d (spotted by @hezhizhen in #103)";
  assert.deepEqual(via(s), ["hezhizhen/GitHub"]);
  assert.deepEqual(parse(s).refs, [{ repo: "yetone/magpie", n: 103 }]);
  // a bracket of this shape with no platform keeps its GitHub login
  assert.deepEqual(via("gui: a thing (by @someone)"), ["someone/GitHub"]);
  assert.deepEqual(via("gui: a thing (reported by @duanckham on X)"), ["duanckham/X"]);
  // 0887a9d7e, its head's own credit left out: "served by" after " — "
  // says what serves it, not who reported it
  assert.deepEqual(via("cursor: a model Cursor serves only in Max Mode is asked for again in Max Mode — the built-in Cursor (deprecated, served by @magpie-community/opencode-cursor-auth once moved; that plugin's 0.1.7, magpie-community/plugins 3ae3e43, carries the same two fixes) asked every model with no max_mode in the Run's ModelDetails (7) or RequestedModel (2)"), []);
  // 044e9bd2d, de487aec5, d20f306fc (v0.1.4834): a name, its platform, an issue
  assert.deepEqual(parse("provider: a key's own model list never bars the vendor's decision model (bunnybit0121 on X, #7788)"), { names: [{ name: "bunnybit0121", via: "X", sure: true }], refs: [{ repo: "yetone/magpie", n: 7788 }] });
});

test("a bracket written in quotes is quoted text, not a credit", () => {
  // 9e9332164 (v0.1.5005) put the form it reads in its own subject
  assert.deepEqual(names('credits: a body paragraph that opens "NAME on X:" credits NAME, as does a bracket of handles "(@A, @B on X)" (ZhouYimingsjtu on X)'), ["ZhouYimingsjtu"]);
  assert.deepEqual(names("credits: “(lc on Discord)” is a form, and 「(lc on Discord)」 too"), []);
  assert.deepEqual(parseBody('the form "(@A, @B on X)" is read'), []);
  // a quote on one side only is no quoting
  assert.deepEqual(names('gui: the title says "Done" (lc on Discord)'), ["lc"]);
  assert.deepEqual(names("gui: the list keeps the order (lc on Discord)'s report asked for"), ["lc"]);
  assert.deepEqual(names("gui: a thing (by @someone)"), ["someone"]);
  // and the row's text keeps it
  assert.equal(what('credits: "(reported by @duanckham on X)" credits an X account, and is read after a subject\'s " — " too'), 'credits: "(reported by @duanckham on X)" credits an X account, and is read after a subject\'s "');
  assert.equal(what('credits: a bracket of handles "(@A, @B on X)" (ZhouYimingsjtu on X)'), 'credits: a bracket of handles "(@A, @B on X)"');
});

test("a body paragraph that opens with who said it, or a bracket of their handles, credits them", () => {
  // be400389b (v0.1.4541)
  assert.deepEqual(body("provider: Antigravity counts Gemini 4 Carbon and Argon at their original price, $4 / $20 / $0.20",
    "ZhouYimingsjtu on X: with an Ultra 20x plan, ~$200 of real use gave\nCarbon:Neon:Hydrogen = 1.64:1:0.39. magpie priced them 2:3:1.5 by input\n(0.67:1:0.5), so Carbon read cheaper than Neon while Antigravity counts it\nabove."), ["ZhouYimingsjtu/X"]);
  // e6e0585bd (v0.1.4177)
  assert.deepEqual(body("antigravity: reword Hermes', OpenCode's, Codex CLI's and pi's identity lines, which Google refuses since 11-14 evening",
    "Since about 18:43 on 11-14 Antigravity answers 429 \"Resource has been\nexhausted\" to a system instruction holding \"You are Hermes Agent\"\n(Hermes' current persona), \"You are OpenCode\", Codex CLI's \"You are a\ncoding agent running in the Codex CLI\" or pi's \"a coding assistant\nrunning inside pi\", whatever quota is left (@ZhouYimingsjtu,\n@Lmohn39 on X). Hermes' earlier persona is still refused as \"You're …\ncreated by\", so it keeps its \"built by\"."), ["ZhouYimingsjtu/X", "Lmohn39/X"]);
  // e1c186cfe, d620ff6b5, a16da55d3, 81a24cf8f (two people; a verb before or after)
  assert.deepEqual(parseBody("zzstar101 and m0eaki on X: after a refresh the GPT-6.2 models never appeared").map((n) => `${n.name}/${n.via}`), ["zzstar101/X", "m0eaki/X"]);
  assert.deepEqual(parseBody("Parry_Moody and Cyrus on Discord: Codex on OpenAI through an API key before magpie").map((n) => n.name), ["Parry_Moody", "Cyrus"]);
  assert.deepEqual(parseBody("Ported for nylesssssssss on X: #5584 (Suge8's interactive effort patch, ae3ba37a9)").map((n) => n.name), ["nylesssssssss"]);
  assert.deepEqual(parseBody("ZeroClover asked on X: Auto Group ids group/auto-<slug> can't be looked up on models.dev.").map((n) => n.name), ["ZeroClover"]);
  // not a platform, not a person, or more in the bracket than handles
  for (const t of [
    "Run on mote: go test ./internal/gateway ok", // 3cee9d4ff
    "An R2 user on X: the sync bucket grew by 18 GB in under a month.", // 9dcec6751
    "A user on Discord: the panel stays.", // e4a90aa62
    "a hang (the same hang as f6c24c680's, @imty42 on X)", // bd60473c5
    "several (gengyubo, Codex; xiaozuoyou, Cindy; @zuoyou_dev on X)", // cb0403c89
    "the editor (the one on X) stays", // a bracket with no handle
    "Tiger and a Windows user on Discord: the panel stays.", // a list with one that isn't a name
  ]) assert.deepEqual(parseBody(t), [], t);
});

test("commit authors and co-authors are credited, agents and machines not, and no email is given", () => {
  const per = credit([
    { subject: "gui: a group's Save no longer leaves the Routing page blank (#884)", author: "wizzy-yang", email: "244719629+wizzy-yang@users.noreply.github.com", coauthors: [{ name: "magpie-fix", email: "fix@localhost" }] },
    { subject: "gateway: count usage from multiline and final SSE events (#94)", author: "Charan Rathore", email: "charan-rathore@users.noreply.github.com", coauthors: [{ name: "Instinct Agent", email: "agent@instinct.com" }, { name: "Claude Opus 5.5", email: "noreply@anthropic.com" }] },
    { subject: "docker: a server image and a multi-arch build workflow (#245)", author: "Shelken", email: "shelken@example.com", coauthors: [{ name: "glm-5.3-flash", email: "hakase@ooooo.space" }, { name: "yetone", email: "yetoneful@gmail.com" }] },
    { subject: "Merge pull request #1 from someone/branch", author: "ggbdpq", email: "x@y", parents: 2 },
  ]);
  assert.deepEqual(per.map((l) => l.map((p) => p.name)), [["wizzy-yang"], ["Charan Rathore"], ["Shelken"], []]);
  assert.equal(per[0][0].github, "wizzy-yang");
  assert.equal(per[2][0].github, "");
  assert.doesNotMatch(JSON.stringify(per), /@/);
});

test("the exclusion list leaves a person out everywhere", () => {
  const s = "library: an MCP router says which computer it is, so an agent can pick between two (packinggoodsllc_07668 on Discord)";
  assert.deepEqual(names(s, { exclude: ["PackingGoodsLLC_07668"] }), []);
  const commits = [{ subject: s, v: "0.1.2569" }];
  assert.deepEqual(wall(commits, { exclude: ["packinggoodsllc_07668"] }), []);
  assert.deepEqual(excluded(), []); // the checked-in list has comments only
  const dir = mkdtempSync(join(tmpdir(), "credits-"));
  try {
    writeFileSync(join(dir, "x.txt"), "# comment\nPackingGoodsLLC_07668  # asked on Discord\n\n");
    assert.deepEqual(names(s, { exclude: excluded(join(dir, "x.txt")) }), []);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test("the release notes' thanks line, in English and Chinese", () => {
  const people = everyone(credit([
    { subject: "library: an MCP router says which computer it is, so an agent can pick between two (packinggoodsllc_07668 on Discord)" },
    { subject: "gateway: a HEIC photo goes to the vendor as JPEG, or PNG with alpha (qikamatata on X)" },
    { subject: "guard: the local masking model reads text a third faster on amd64 (mrwl666)" },
    { subject: "gateway: a HEIC photo is sent once (qikamatata on X)" },
  ]));
  assert.equal(thanks(people, "en"), "Thanks to packinggoodsllc\\_07668, qikamatata and mrwl666, whose reports and code went into this release. Everyone who helped: [contributors](https://usemagpie.ai/docs/contributors).");
  assert.equal(thanks(people, "zh"), "感谢 packinggoodsllc\\_07668、qikamatata 和 mrwl666：这次更新里有你们的反馈与代码。所有贡献者见[贡献墙](https://usemagpie.ai/docs/zh/contributors)。");
  assert.equal(thanks(people.slice(0, 1), "en").startsWith("Thanks to packinggoodsllc\\_07668, whose"), true);
  assert.match(thanks(people.slice(0, 1), "zh"), /里有你的/);
  assert.equal(thanks([], "en"), "");
});

test("the page shows what each person's reports led to, newest first", () => {
  assert.equal(what("redact: a decimal's digits are not masked as a number of their own (willz, Discord)"), "redact: a decimal's digits are not masked as a number of their own");
  assert.equal(what("plugin: gateway key limits are the Key limits plugin on the gateway's budget point (P3)"), "plugin: gateway key limits are the Key limits plugin on the gateway's budget point (P3)");
  const people = wall([
    { subject: "search: searchers picked to follow the one named, tried in their order when it fails (axelburks on Discord)", v: "0.1.2561" },
    { subject: "gui: Busy models last takes a custom threshold, 1 to 9999 (axelburks)", v: "0.1.2564" },
    { subject: "redact: a decimal's digits are not masked as a number of their own (willz, Discord)", v: "0.1.2560" },
  ]);
  assert.deepEqual(people.map((p) => [p.name, p.releases.map((r) => r.v)]), [["axelburks", ["0.1.2564", "0.1.2561"]], ["willz", ["0.1.2560"]]]);
  const html = renderWall({ people: [{ name: "<b>x</b>", via: ["Discord"], github: "x", releases: [{ v: "0.1.1", what: ["a & b"] }] }] }, "zh");
  assert.match(html, /&lt;b&gt;x&lt;\/b&gt;/);
  assert.match(html, /a &amp; b/);
  assert.match(html, /releases\/tag\/v0\.1\.1/);
  assert.match(html, /新的在前/);
  assert.match(renderWall({}, "ja"), /一覧/);
  const many = renderWall({ people: [{ name: "n", releases: [1, 2, 3, 4, 5].map((i) => ({ v: `0.1.${i}`, what: ["w"] })) }] }, "en");
  assert.match(many, /<summary>2 more<\/summary>/);
});

test("issue authors GitHub can't give are left out, not guessed", async () => {
  const refs = [{ repo: "yetone/magpie", n: 1 }, { repo: "yetone/magpie", n: 2 }];
  const ok = async () => ({ ok: true, json: async () => ({ data: { repository: { i1: { author: { login: "alice", __typename: "User" } }, i2: { author: { login: "dependabot", __typename: "Bot" } } } } }) });
  assert.deepEqual(await authors(refs, { fetchImpl: ok, tok: "t" }), { "yetone/magpie#1": "alice", "yetone/magpie#2": "dependabot[bot]" });
  const down = async () => ({ ok: false, status: 502 });
  const err = process.stderr.write;
  process.stderr.write = () => true;
  try {
    assert.deepEqual(await authors(refs, { fetchImpl: down, tok: "t" }), {});
    assert.deepEqual(await authors(refs, { fetchImpl: down, tok: "t", known: { "yetone/magpie#1": "alice" } }), { "yetone/magpie#1": "alice" });
    assert.deepEqual(await authors(refs, { fetchImpl: ok, tok: "" }), {});
  } finally { process.stderr.write = err; }
});

// realLemonawa on X is GitHub's Lemonawa: Lemonawa's public profile names
// realLemonawa as its X account (twitter_username).
const lemon = [
  { subject: "gateway: a reported call keeps the model asked for and the one that answered (req, served) (#3534, realLemonawa on X)", v: "0.1.5000" },
  { subject: "gui: an auto restart brings a minimised main window back minimised on Windows (#3495, realLemonawa on X)", v: "0.1.5001" },
  { subject: "groups: a model one provider serves can be a found group too, under Advanced (realLemonawa on X)", v: "0.1.5002" },
];
const lemonAuthors = { "yetone/magpie#3534": "Lemonawa", "yetone/magpie#3495": "Lemonawa" };

test("an X credit is the GitHub person whose public profile names that X account", () => {
  const x = { Lemonawa: "realLemonawa" };
  const people = wall(lemon, { issueAuthors: lemonAuthors, xAccounts: x });
  assert.deepEqual(people.map((p) => [p.name, p.github, p.via]), [["Lemonawa", "Lemonawa", ["X", "GitHub"]]]);
  assert.deepEqual(people[0].releases.map((r) => r.v), ["0.1.5002", "0.1.5001", "0.1.5000"]);
  // the release notes thank them once, by the GitHub name
  const once = everyone(credit(lemon, { issueAuthors: lemonAuthors, xAccounts: x }));
  assert.deepEqual(once.map((p) => p.name), ["Lemonawa"]);
  assert.equal(thanks(once, "en").startsWith("Thanks to Lemonawa, whose"), true);
  // as a Map, and with the handle written "@reallemonawa"
  assert.equal(wall(lemon, { issueAuthors: lemonAuthors, xAccounts: new Map([["Lemonawa", "@reallemonawa"]]) }).length, 1);
  // the logins to look up are the GitHub people credit() found
  assert.deepEqual(logins(credit(lemon, { issueAuthors: lemonAuthors })), ["Lemonawa"]);
});

test("names are never joined by a similar spelling, another platform or a shared claim", () => {
  // nothing names the X account: two people, as credited
  assert.deepEqual(wall(lemon, { issueAuthors: lemonAuthors }).map((p) => p.name).sort(), ["Lemonawa", "realLemonawa"]);
  // a profile naming another X account joins nobody
  assert.equal(wall(lemon, { issueAuthors: lemonAuthors, xAccounts: { Lemonawa: "lemonawa" } }).length, 2);
  // a Discord name isn't an X account
  const discord = [{ subject: "redact: a decimal's digits are not masked as a number of their own (willz, Discord)", v: "0.1.2560" }];
  assert.deepEqual(wall(discord, { xAccounts: { someone: "willz" } }).map((p) => [p.name, p.github]), [["willz", undefined]]);
  // two logins naming one X account: either could have typed it, so neither
  const two = wall(lemon, { issueAuthors: { ...lemonAuthors, "yetone/magpie#3495": "someoneElse" }, xAccounts: { Lemonawa: "realLemonawa", someoneElse: "realLemonawa" } });
  assert.deepEqual(two.map((p) => p.name).sort(), ["Lemonawa", "realLemonawa", "someoneElse"]);
  // an X handle on the exclusion list stays left out
  assert.deepEqual(wall(lemon.slice(2), { xAccounts: { Lemonawa: "realLemonawa" }, exclude: ["realLemonawa"] }), []);
});

test("X accounts GitHub can't give join nobody", async () => {
  const ok = async (_, req) => {
    assert.match(JSON.parse(req.body).query, /u0: user\(login: "Lemonawa"\) \{ login twitterUsername socialAccounts\(first: 20\) \{ nodes \{ url \} \} \}/);
    return { ok: true, json: async () => ({ data: { u0: { login: "Lemonawa", twitterUsername: "realLemonawa" }, u1: { login: "alice", twitterUsername: null }, u2: null } }) };
  };
  assert.deepEqual(await xAccounts(["Lemonawa", "alice", "gone"], { fetchImpl: ok, tok: "t" }), { Lemonawa: "realLemonawa" });
  const down = async () => ({ ok: false, status: 502 });
  const err = process.stderr.write;
  process.stderr.write = () => true;
  try {
    assert.deepEqual(await xAccounts(["Lemonawa"], { fetchImpl: down, tok: "t" }), {});
    // a failed read keeps what the last list knew
    assert.deepEqual(await xAccounts(["Lemonawa", "alice"], { fetchImpl: down, tok: "t", known: { Lemonawa: "realLemonawa" } }), { Lemonawa: "realLemonawa" });
    assert.deepEqual(await xAccounts(["Lemonawa"], { fetchImpl: ok, tok: "" }), {});
    // asked fresh: a profile that dropped its X account no longer joins
    const dropped = async () => ({ ok: true, json: async () => ({ data: { u0: { login: "Lemonawa", twitterUsername: null } } }) });
    assert.deepEqual(await xAccounts(["Lemonawa"], { fetchImpl: dropped, tok: "t", known: { Lemonawa: "realLemonawa" } }), {});
  } finally { process.stderr.write = err; }
});

test("a profile's X link among its social accounts names its X account too", async () => {
  // dou_dev's link as GitHub gives it: https://x.com/dou_dev
  assert.equal(xOfLink("https://x.com/dou_dev"), "dou_dev");
  assert.equal(xOfLink("https://twitter.com/yukaii_h"), "yukaii_h");
  assert.equal(xOfLink("https://www.x.com/@ZhouYimingsjtu/"), "ZhouYimingsjtu");
  for (const u of ["https://x.com/intent", "https://twitter.com/home/", "https://x.com/intent/follow?screen_name=a", "https://x.com/i/status/1", "https://x.com/a/status/1", "https://bsky.app/profile/a", "https://notx.com/a", "https://x.com/"]) assert.equal(xOfLink(u), "", u);
  let asked = "";
  const gh = async (_, req) => {
    asked = JSON.parse(req.body).query;
    return { ok: true, json: async () => ({ data: {
      u0: { login: "isdou", twitterUsername: "dou_dev", socialAccounts: { nodes: [{ url: "https://x.com/dou_dev" }] } },
      u1: { login: "dreamfarer-space", twitterUsername: null, socialAccounts: { nodes: [{ url: "https://x.com/ZhouYimingsjtu" }, { url: "https://github.blog" }] } },
      u2: { login: "two", twitterUsername: "first", socialAccounts: { nodes: [{ url: "https://twitter.com/second" }] } },
      u3: { login: "LoveEcho1145", twitterUsername: null, socialAccounts: { nodes: [] } },
    } }) };
  };
  const x = await xAccounts(["isdou", "dreamfarer-space", "two", "LoveEcho1145"], { fetchImpl: gh, tok: "t" });
  assert.match(asked, /u1: user\(login: "dreamfarer-space"\) \{ login twitterUsername socialAccounts\(first: 20\) \{ nodes \{ url \} \} \}/);
  assert.deepEqual(x, { isdou: "dou_dev", "dreamfarer-space": "ZhouYimingsjtu", two: ["first", "second"] });
  // either of two X accounts joins, and both find the row
  const both = [{ subject: "gui: a thing (second on X)", v: "0.1.2" }, { subject: "gui: another (first on X)", v: "0.1.1" }];
  const people = wall(both, { xAccounts: { two: ["first", "second"] } });
  assert.deepEqual(people.map((p) => [p.name, p.github, p.via]), [["two", "two", ["X"]]]);
  assert.match(renderWall({ people, xAccounts: { two: ["first", "second"] } }, "en"), /<tr id="two" data-k="first second">/);
});

// ZhouYimingsjtu on X says GitHub's dreamfarer-space is theirs; no commit
// credits that login, so only contributors-aliases.txt has its profile read
const zhou = [{
  subject: "provider: Antigravity counts Gemini 4 Carbon and Argon at their original price, $4 / $20 / $0.20",
  body: "ZhouYimingsjtu on X: with an Ultra 20x plan, ~$200 of real use gave\nCarbon:Neon:Hydrogen = 1.64:1:0.39. magpie priced them 2:3:1.5 by input",
  v: "0.1.4541",
}];

test("an alias line joins an X account to a login only while that login's profile names it", () => {
  const dir = mkdtempSync(join(tmpdir(), "credits-"));
  const err = process.stderr.write;
  let warned = "";
  process.stderr.write = (m) => { warned += m; return true; };
  try {
    writeFileSync(join(dir, "a.txt"), "# comment\nZhouYimingsjtu dreamfarer-space  # said on X\n@elis_pina LoveEcho1145\nnot an alias line\ndreamfarer_space x_y\n\n");
    const al = aliases(join(dir, "a.txt"));
    assert.deepEqual(al, [{ x: "ZhouYimingsjtu", login: "dreamfarer-space" }, { x: "elis_pina", login: "LoveEcho1145" }]);
    assert.match(warned, /"not an alias line" is not "x-handle github-login"/);
    assert.match(warned, /"dreamfarer_space x_y"/); // an underscore is no GitHub login
    assert.deepEqual(aliases(join(dir, "none.txt")), []);
    // the alias's login is looked up beside the credited ones, once
    assert.deepEqual(toLookUp(["LoveEcho1145", "isdou"], al), ["LoveEcho1145", "isdou", "dreamfarer-space"]);
    assert.deepEqual(toLookUp(["loveecho1145"], al), ["loveecho1145", "dreamfarer-space"]);

    // the profile names nothing yet: nothing changes
    let x = honoured({}, [], al);
    assert.deepEqual(x, {});
    assert.deepEqual(wall(zhou, { xAccounts: x }).map((p) => [p.name, p.github, p.via]), [["ZhouYimingsjtu", undefined, ["X"]]]);
    // it names that X account: the credit goes under the login
    x = honoured({ "dreamfarer-space": "ZhouYimingsjtu" }, [], al);
    assert.deepEqual(x, { "dreamfarer-space": "ZhouYimingsjtu" });
    const joined = wall(zhou, { xAccounts: x });
    assert.deepEqual(joined.map((p) => [p.name, p.github, p.via]), [["dreamfarer-space", "dreamfarer-space", ["X"]]]);
    assert.match(renderWall({ people: joined, xAccounts: x }, "zh"), /<tr id="dreamfarer-space" data-k="zhouyimingsjtu">/);
    // it names another X account: an uncredited login takes only the line's
    assert.deepEqual(honoured({ "dreamfarer-space": "someone_else" }, [], al), {});
    assert.deepEqual(honoured({ "dreamfarer-space": ["someone_else", "zhouyimingsjtu"] }, [], al), { "dreamfarer-space": "zhouyimingsjtu" });
    // a login with no line and no credit takes nothing
    assert.deepEqual(honoured({ stranger: "ZhouYimingsjtu" }, [], al), {});
    // a credited login keeps every X account its profile names, line or not
    assert.deepEqual(honoured({ LoveEcho1145: ["elis_pina", "other"], isdou: "dou_dev" }, ["LoveEcho1145", "isdou"], al), { LoveEcho1145: ["elis_pina", "other"], isdou: "dou_dev" });
  } finally {
    process.stderr.write = err;
    rmSync(dir, { recursive: true, force: true });
  }
  // the checked-in list: each line is an X handle and a GitHub login
  assert.deepEqual(aliases(), [{ x: "ZhouYimingsjtu", login: "dreamfarer-space" }, { x: "elis_pina", login: "LoveEcho1145" }]);
});

// public/wall.js, the page's finder: a classic script that also exports
// its matcher here
const finder = createRequire(import.meta.url)("./public/wall.js");

test("each row has an id a link can land on, kept while the list reorders", () => {
  // lijiho96940561 on X (latest v0.1.3138) couldn't find their row
  assert.equal(slug("lijiho96940561"), "lijiho96940561");
  assert.equal(slug("Benjie Zhou"), "benjie-zhou");
  assert.equal(slug("ylorn."), "ylorn.");
  assert.equal(slug("tjhhhh_84378"), "tjhhhh_84378");
  assert.equal(slug("教父"), "教父");
  assert.equal(slug("辰林鸭~"), "辰林鸭");
  assert.equal(slug("4 v5"), "4-v5");
  assert.equal(slug("ＡＢＣ"), "abc");
  assert.match(slug("~~~"), /^p-[0-9a-z]+$/);
  assert.notEqual(slug("~~~"), slug("!!!"));
  // a GitHub person's id is their login, whatever name the commit gave
  assert.deepEqual(anchors([{ name: "Benjie Zhou", github: "bjz" }, { name: "lijiho96940561" }]), ["bjz", "lijiho96940561"]);
  // the same slug twice, or the page's own id, is told apart in key order,
  // so it doesn't move when the list's order does
  const a = anchors([{ name: "a b" }, { name: "a-b" }, { name: "list" }]);
  const b = anchors([{ name: "list" }, { name: "a-b" }, { name: "a b" }]);
  assert.deepEqual(a, ["a-b-2", "a-b", "list-2"]);
  assert.deepEqual(b, ["list-2", "a-b", "a-b-2"]);
  // charan-rathore (a GitHub login) keeps its own id beside "Charan Rathore"
  assert.deepEqual(anchors([{ name: "charan-rathore", github: "charan-rathore" }, { name: "Charan Rathore" }]), ["charan-rathore", "charan-rathore-2"]);
  assert.deepEqual(anchors([{ name: "x y" }, { name: "x z" }, { name: "x-y" }]), ["x-y-2", "x-z", "x-y"]);
  for (const id of PAGE_IDS) assert.equal(anchors([{ name: id }])[0], `${id}-2`);
  // every id the pages and their scripts use is one rows keep out of
  const used = new Set();
  for (const f of ["docs/contributors.html", "docs/zh/contributors.html", "docs/ja/contributors.html", "docs.js", "docs-search.js"]) {
    const text = readFileSync(new URL(`./public/${f}`, import.meta.url), "utf8");
    for (const m of text.matchAll(/\bid="([^"]+)"|\.id = "([^"]+)"/g)) used.add(m[1] || m[2]);
  }
  used.delete("sx-"); // docs-search.js's results, sx-0, sx-1 …: no key is all that
  for (const id of used) assert.ok(PAGE_IDS.includes(id), `${id} is not in PAGE_IDS`);
});

test("the list carries its finder and each row's id, in each language", () => {
  const data = {
    xAccounts: { Lemonawa: "realLemonawa" },
    people: [
      { name: "lijiho96940561", via: ["X"], releases: [{ v: "0.1.3138", what: ["gateway: Ollama's options reach no Chat upstream"] }] },
      { name: "Lemonawa", via: ["GitHub", "X"], github: "Lemonawa", releases: [{ v: "0.1.5002", what: ["w"] }] },
      { name: "Benjie Zhou", via: ["GitHub"], github: "bjz", releases: [{ v: "0.1.5001", what: ["w"] }] },
    ],
  };
  for (const [lang, ph, many] of [["en", "Find a name", "{n} people"], ["zh", "查找名字", "{n} 人"], ["ja", "名前で探す", "{n} 人"]]) {
    const html = renderWall(data, lang);
    // hidden until the script shows it: with script off the list is whole
    assert.ok(html.startsWith(`<div class="cw-find" hidden data-one="`), lang);
    assert.ok(html.includes(`data-many="${many}"><input id="cw-q" type="search" placeholder="${ph}"`), lang);
    assert.match(html, /<tr id="lijiho96940561"><td><b>lijiho96940561<\/b>/);
    // what else finds a row: its login when that isn't its name, its X account
    assert.match(html, /<tr id="lemonawa" data-k="reallemonawa">/);
    assert.match(html, /<tr id="bjz" data-k="bjz">/);
  }
});

test("the finder matches a name, login or X account, any case, a leading @ left off", () => {
  const { matches, rowKeys, norm } = finder;
  const lijiho = rowKeys("lijiho96940561", "");
  for (const q of ["lijiho", "LIJIHO", "@lijiho96940561", " @Lijiho ", "96940561", "ｌｉｊｉｈｏ", "＠lijiho", ""]) assert.ok(matches(lijiho, q), q);
  for (const q of ["lijiho9694x", "x@lijiho", "lijiho 9"]) assert.equal(matches(lijiho, q), false, q);
  const lemon = rowKeys("Lemonawa", "reallemonawa");
  assert.ok(matches(lemon, "@realLemonawa"));
  assert.ok(matches(lemon, "lemon"));
  const bjz = rowKeys("Benjie Zhou", "bjz");
  assert.ok(matches(bjz, "benjie zh"));
  assert.ok(matches(bjz, "BJZ"));
  assert.ok(matches(rowKeys("教父", ""), "教"));
  assert.equal(norm("  @@Foo "), "foo");
});
