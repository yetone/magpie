// Run with Node's test runner and Playwright on the module path; see README.md.
// infinitr0us (#2261): a session's subagents sit under it, not beside it.
// A Claude Code session's subagents (s.subagents, as Claude Code, Qoder,
// OpenCode and Kimi Code list them) are folded under its row, "2
// subagents"; unfolded, each is a row of what it was asked and the agent
// it ran as, a subagent another one started set in under it, and a click
// shows what was said in it, read from sessions/transcript with the
// session and the subagent's id (sub=), once. A Codex thread whose
// parent_session is listed is drawn under that session, a session of its
// own still (its details open where it is), and only main sessions are on
// top; a loop of parents, or a parent not listed, leaves a thread on top.
// A Claude session forked from another that went on (role "branch") sits
// under it the same way, its fold naming them branches.
// No click moves the page, nothing has a left-border accent, no <select>,
// nothing wider than the window. In every language, Chromium and WebKit,
// at 1100px and 420px; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const sess = (agent, id, title, min, extra) => ({
  agent, id, cwd: "/work/app", title, start: at(min + 5), last: at(min), models: [], resume: `${agent} --resume ${id}`,
  path: `/x/${id}.jsonl`, size: 4096, messages: 6, files: 1, deletable: true, transcript: true, ...extra,
});

const CLAUDE = [
  sess("claude", "c-1", "Refactor the parser", 5, {
    files: 3,
    subagents: [
      { id: "a1", title: "Inventory the parser's callers", kind: "Explore", start: at(9), last: at(8) },
      { id: "ac1d8e7a80d42178d", title: "Research MCP config", kind: "general-purpose", parent: "a1", start: at(8), last: at(7) },
    ],
  }),
  sess("claude", "c-2", "an older one", 50),
  // Neo (#feedback): forks of a compacted session, each with a task of its
  // own, are its branches, folded under it
  sess("claude", "c-5", "Plan the items", 60),
  sess("claude", "c-6", "Plan the items", 61, { role: "branch", parent_session: "c-5" }),
  sess("claude", "c-7", "Plan the items", 62, { role: "branch", parent_session: "c-5" }),
];
const CODEX = [
  sess("codex", "x-1", "Lead the port", 5),
  sess("codex", "x-2", "Port the lexer", 6, { role: "subagent", parent_session: "x-1", nickname: "Lexer" }),
  sess("codex", "x-3", "Port the tokens", 7, { role: "subagent", parent_session: "x-2" }),
  sess("codex", "x-4", "one of a loop", 20, { parent_session: "x-5" }),
  sess("codex", "x-5", "the other of the loop", 21, { parent_session: "x-4" }),
  sess("codex", "x-6", "its parent is gone", 30, { role: "subagent", parent_session: "x-gone" }),
];
const PARTS = [
  { role: "user", kind: "text", text: "Research task only" },
  { role: "assistant", kind: "text", text: "Kimi reads ~/.kimi/mcp.json" },
];

function serve(lang, calls) {
  const store = { claude: CLAUDE, codex: CODEX };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions/manage") {
      const agent = url.searchParams.get("agent") === "codex" ? "codex" : "claude";
      return json({
        agents: [
          { agent: "claude", count: CLAUDE.length, deletable: true, name: "Claude Code", icon: "claudecode-color" },
          { agent: "codex", count: CODEX.length, deletable: true, name: "Codex", icon: "codex" },
        ],
        agent, sessions: store[agent], terminal: false, trash: [], trashDir: "~/trash",
      });
    }
    if (url.pathname === "/api/sessions/transcript") {
      const c = { agent: url.searchParams.get("agent"), id: url.searchParams.get("id") };
      if (url.searchParams.has("sub")) c.sub = url.searchParams.get("sub");
      calls.push(c);
      return json({ parts: PARTS });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

async function open(engine, lang, width, agent, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
  t.after(async () => {
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${agent}-subagents.png`), fullPage: true });
    }
    await browser.close();
  });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
  const calls = [];
  await page.addInitScript((a) => { localStorage.setItem("magpie.sessionsAgent", a); }, agent);
  await page.route("**/*", serve(lang, calls));
  await page.goto("http://magpie.test/?view=providers");
  await page.locator('#nav button[data-view="sessions"]').click();
  const view = page.locator("#view-sessions");
  const top = (l) => l.evaluate((e) => e.getBoundingClientRect().top);
  const still = async (l, what, click) => {
    const before = await top(l);
    await click();
    await page.waitForTimeout(250);
    assert.equal(await top(l), before, `${what} moved the page`);
  };
  const tr = (k, v) => page.evaluate(([k, v]) => t(k, v), [k, v]);
  const tidy = async () => {
    assert.equal(await page.locator("select").count(), 0, "no native select");
    const border = await page.evaluate(() => [...document.querySelectorAll("#view-sessions, #view-sessions *")]
      .filter((e) => parseFloat(getComputedStyle(e).borderLeftWidth) > 1 && getComputedStyle(e).borderLeftColor !== getComputedStyle(e).borderRightColor).map((e) => e.className));
    assert.deepEqual(border, [], "no left-border accent");
    const wide = await page.evaluate(() => {
      const box = document.querySelector("#view-sessions").getBoundingClientRect();
      return [...document.querySelectorAll("#view-sessions .sm-subs *")].filter((e) => e.getBoundingClientRect().right > box.right + 1).map((e) => e.className);
    });
    assert.deepEqual(wide, [], "nothing under a session is wider than the page");
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), "the page doesn't scroll sideways");
  };
  return { page, view, calls, errors, still, tr, tidy };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: a Claude Code session's subagents are folded under it and open to what was said`, async (t) => {
        const { page, view, calls, errors, still, tr, tidy } = await open(engine, lang, width, "claude", t);
        if (lang !== "en") {
          const missing = await page.evaluate((lang) => ["{n} subagent", "{n} subagents", "{n} branch", "{n} branches"].filter((k) => !I18N[lang]?.[k]), lang);
          assert.deepEqual(missing, [], `every new string has its ${lang}`);
        }
        const row = (id) => view.locator(`.row.sm-sess[data-id="${id}"]`);
        await row("c-1").waitFor();
        const item = row("c-1").locator("xpath=..");
        const fold = item.locator(":scope > .sm-subs > .sm-subs-fold");
        assert.equal(await fold.count(), 1);
        assert.equal((await fold.textContent()).trim(), await tr("{n} subagents", { n: 2 }));
        assert.equal(await fold.getAttribute("aria-expanded"), "false");
        assert.equal(await view.locator(".sm-sub").count(), 0, "folded at first");
        assert.equal(await row("c-2").locator("xpath=..").locator(".sm-subs").count(), 0, "a session with none has no fold");
        const tops = await view.locator(".sm-group > .sm-item > .row.sm-sess").evaluateAll((rs) => rs.map((r) => r.dataset.id));
        assert.deepEqual(tops, ["c-1", "c-2", "c-5"], "a branch is not a row beside its session");
        const branches = row("c-5").locator("xpath=..").locator(":scope > .sm-subs > .sm-subs-fold");
        assert.equal((await branches.textContent()).trim(), await tr("{n} branches", { n: 2 }));
        await still(row("c-5"), "unfolding the branches", () => branches.click());
        await row("c-6").waitFor();
        assert.equal(await row("c-7").isVisible(), true);
        await branches.click();

        await still(row("c-1"), "unfolding the subagents", () => fold.click());
        assert.equal(await fold.getAttribute("aria-expanded"), "true");
        assert.equal(await view.locator(".sess-detail").count(), 0, "the fold doesn't open the session");
        const subs = item.locator(".sm-sub");
        assert.deepEqual(await subs.locator(".sm-sub-row").evaluateAll((rs) => rs.map((r) => r.dataset.sub)), ["a1", "ac1d8e7a80d42178d"], "each under the one that started it");
        assert.equal((await subs.nth(0).locator(".name").textContent()).trim(), "Inventory the parser's callers");
        assert((await subs.nth(0).locator(".sub").textContent()).startsWith("Explore · "));
        const inset = (i) => subs.nth(i).locator(".sm-sub-row .who").evaluate((e) => e.getBoundingClientRect().left);
        assert(await inset(1) > await inset(0), "a subagent's own subagent is set in under it");
        await tidy();

        const a1 = subs.nth(1).locator(".sm-sub-row");
        await still(a1, "opening a subagent", () => a1.click());
        const talk = page.locator(".sess-sheet .sess-talk");
        await talk.locator(".cx-part").first().waitFor();
        assert.deepEqual(calls, [{ agent: "claude", id: "c-1", sub: "ac1d8e7a80d42178d" }]);
        assert.equal(await talk.locator(".cx-part").count(), PARTS.length);
        assert((await talk.textContent()).includes("Kimi reads ~/.kimi/mcp.json"));
        await tidy();
        await page.locator(".sess-sheet .bar .primary").click();
        await page.locator(".sess-sheet").waitFor({ state: "detached" });
        await a1.click();
        await talk.locator(".cx-part").first().waitFor();
        assert.equal(calls.length, 1, "read once");
        await page.locator(".sess-sheet .bar .primary").click();
        await page.locator(".sess-sheet").waitFor({ state: "detached" });

        // the session's own conversation is still its own
        await row("c-1").locator(".who").click();
        await view.locator(".sess-detail .sess-talk-btn").click();
        await page.locator(".sess-sheet .sess-talk .cx-part").first().waitFor();
        assert.deepEqual(calls[1], { agent: "claude", id: "c-1" });
        await page.locator(".sess-sheet .bar .primary").click();
        await page.locator(".sess-sheet").waitFor({ state: "detached" });
        // folded again, it stays folded when the row is drawn again
        await item.locator(".sm-subs-fold").first().click();
        assert.equal(await view.locator(".sm-sub").count(), 0);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: Codex threads sit under the session that started them`, async (t) => {
        const { view, errors, still, tr, tidy } = await open(engine, lang, width, "codex", t);
        const row = (id) => view.locator(`.row.sm-sess[data-id="${id}"]`);
        await row("x-1").waitFor();
        const tops = await view.locator(".sm-group > .sm-item > .row.sm-sess").evaluateAll((rs) => rs.map((r) => r.dataset.id));
        assert.deepEqual(tops, ["x-1", "x-4", "x-5", "x-6"], "only main sessions on top; a loop or a parent not listed stays on top");
        assert.equal((await view.locator(".sm-folder .note").textContent()).trim(), await tr("{n} sessions", { n: 6 }), "the folder counts the ones under others");
        const fold = (id) => row(id).locator("xpath=..").locator(":scope > .sm-subs > .sm-subs-fold");
        assert.equal((await fold("x-1").textContent()).trim(), await tr("{n} subagent", { n: 1 }));
        for (const id of ["x-4", "x-5", "x-6"]) assert.equal(await fold(id).count(), 0, `${id} has none under it`);
        await still(row("x-1"), "unfolding a Codex session", () => fold("x-1").click());
        await row("x-2").waitFor();
        await still(row("x-1"), "unfolding a thread", () => fold("x-2").click());
        await row("x-3").waitFor();
        const left = (id) => row(id).locator(".who").evaluate((e) => e.getBoundingClientRect().left);
        assert(await left("x-2") > await left("x-1") && await left("x-3") > await left("x-2"), "each set in under its parent");
        // a thread is a session of its own: its details open where it is
        await still(row("x-3"), "opening a thread", () => row("x-3").locator(".who").click());
        const d = row("x-3").locator("xpath=..").locator(":scope > .sess-detail");
        await d.waitFor();
        assert((await d.textContent()).includes("x-3"));
        assert.equal(await row("x-3").isVisible(), true, "still under its parent");
        await tidy();
        // opening another closes it, and both stay where they are
        await row("x-2").locator(".who").click();
        await row("x-2").locator("xpath=..").locator(":scope > .sess-detail").waitFor();
        assert.equal(await d.count(), 0);
        assert.equal(await row("x-3").isVisible(), true);
        assert.deepEqual(errors, []);
      });
    }
  }
}
