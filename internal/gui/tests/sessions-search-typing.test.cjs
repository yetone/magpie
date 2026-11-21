// Run with Node's test runner and Playwright on the module path; see README.md.
// A search box over a long history counts its words once typing pauses, at
// Enter, and never while an input method is still composing them
// (suetfungsit on X: the Sessions page filtered and redrew its list on every
// keystroke, which lagged over a large history, and a Chinese input method's
// pinyin was searched before its characters were picked). Checked on the
// Sessions page, Usage › Sessions and Usage › Requests: typing fast redraws
// the list once and asks the server once; a composition asks nothing and
// leaves the list as it was until its characters are picked, then asks once
// for them; Enter and Escape count at once. Chromium composes through its
// real input-method path (CDP), WebKit through the events Safari sends. In
// every language, Chromium and WebKit, wide and narrow. MAGPIE_ASSETS
// selects another assets directory to check the cases against the old code.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets");
const PAUSE = 450; // a reader picking among the input method's candidates
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const gw = (id, title, min) => ({
  agent: "pi", id, cwd: "", title, gateway: true, start: at(min + 5), last: at(min), models: [{ model: "m" }], size: 2048, deletable: true,
});
const said = {
  ses_01: "rename foo to bar in the parser",
  ses_02: "why does the build fail? The Redis client times out",
  ses_03: "write a haiku about Kafka and autumn",
  ses_04: "你好，帮我看看这个报错",
};
// what Usage › Sessions lists
const usageSess = Array.from({ length: 40 }, (_, i) => ({
  agent: "claude", name: "Claude Code", icon: "claude", id: "u" + i, cwd: "/work/alpha",
  title: i === 3 ? "你好 the login page" : i === 7 ? "kafka autumn notes" : "Session " + i,
  start: at(i * 10 + 30), last: at(i * 10 + 1), input: 1000, output: 100, cache_read: 0, cache_write: 0, cost: 0.01, priced: true,
  models: [{ model: "claude-sonnet-5", input: 1000, output: 100, cost: 0.01, priced: true }], resume: "claude --resume u" + i,
}));
const ROW = (req) => ({ t: at(1), agent: "codex", agentName: "Codex", icon: "codex-color", provider: "relay", providerName: "Relay", host: "team", req, model: "gpt-6-sol", in: 100, out: 10, ms: 900, status: 200, cost: 0.01, priced: true });

function serve(lang, log) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const sessions = [gw("ses_04", "看看报错", 0), gw("ses_03", "write a haiku about Kafka and autumn", 1), gw("ses_02", "why does the build fail?", 5), gw("ses_01", "rename foo to bar in the parser", 9)];
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data }).catch(() => {});
    const p = url.pathname;
    if (p === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (p === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (p === "/api/state") return json(state);
    if (p === "/api/plugins") return json({ plugins: [] });
    if (p === "/api/groups") return json({ groups: [], models: [] });
    if (p === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (p === "/api/sessions/manage") {
      return json({
        agents: [{ agent: "pi", count: sessions.length, deletable: true, name: "Pi", icon: "pi" }],
        agent: "pi", sessions, terminal: false, trash: [], trashDir: "~/trash", recording: true, recorded: true,
      });
    }
    if (p === "/api/sessions/search" || p === "/api/sessions/said") {
      const q = url.searchParams.get("q");
      log.push({ kind: p.endsWith("said") ? "said" : "search", q });
      if (p.endsWith("said")) return json({ hits: [], indexed: 4, total: 4 });
      const hits = [];
      for (const [id, text] of Object.entries(said)) {
        const i = text.toLowerCase().indexOf(q);
        if (i >= 0) hits.push({ id, role: "user", kind: "text", before: text.slice(0, i), match: text.slice(i, i + q.length), after: text.slice(i + q.length) });
      }
      return json({ hits, searched: 4, total: 4 });
    }
    if (p === "/api/usage/requests") {
      const q = url.searchParams.get("q") || "";
      log.push({ kind: "requests", q });
      return json({ period: url.searchParams.get("period"), rows: [ROW("asked-" + (q || "none"))], offset: 0, total: 1, calls: 1, errors: 0, input: 100, output: 10, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0.01, unpriced: 0, bucket: "day", series: [], agents: [], providers: [] });
    }
    if (p === "/api/usage/heatmap") return json({ from: "", to: "", days: [] });
    if (p === "/api/usage/quotas") return json([]);
    if (p === "/api/usage/quotas/history") return json({ days: [] });
    if (p === "/api/usage") return json({ calls: 1, errors: 0, input: 100, output: 10, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 0, cost: 0.01, bucket: "day", series: [], agents: [], models: [], path: "~/.config/magpie/usage.jsonl" });
    if (p === "/api/sessions/progress") return json({ indexing: false });
    if (p === "/api/sessions") return json({ sessions: usageSess, dirs: ["/test/sessions"] });
    if (p === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: { claude: "Claude Code" } });
    if (p.startsWith("/api/")) return json({});
    const file = path.join(assets, p === "/" ? "index.html" : p);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

// compose types steps through an input method into box, a pause after each,
// and picks final: Chromium through its own composition (CDP), WebKit by
// Safari's events (the last input after compositionend, not composing)
async function compose(engine, page, box, steps, final) {
  await box.focus();
  if (engine === "chromium") {
    const cdp = await page.context().newCDPSession(page);
    for (const s of steps) {
      await cdp.send("Input.imeSetComposition", { text: s, selectionStart: s.length, selectionEnd: s.length });
      await page.waitForTimeout(PAUSE);
    }
    await cdp.send("Input.insertText", { text: final });
    await cdp.detach();
    return;
  }
  await box.evaluate((el) => el.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true, data: "" })));
  for (const s of steps) {
    await box.evaluate((el, s) => {
      el.value = s;
      el.dispatchEvent(new CompositionEvent("compositionupdate", { bubbles: true, data: s }));
      el.dispatchEvent(new InputEvent("input", { bubbles: true, isComposing: true, inputType: "insertCompositionText", data: s }));
    }, s);
    await page.waitForTimeout(PAUSE);
  }
  await box.evaluate((el, s) => {
    el.value = s;
    el.dispatchEvent(new InputEvent("input", { bubbles: true, isComposing: true, inputType: "insertCompositionText", data: s }));
    el.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true, data: s }));
    el.dispatchEvent(new InputEvent("input", { bubbles: true, isComposing: false, inputType: "insertFromComposition", data: s }));
  }, final);
}

// how many times the list under sel was drawn anew, one a task
// draws counts the list's redraws (one a task), and pauses the gaps between
// keystrokes long enough for the wait to end: on a loaded machine a 30ms
// delay can stretch past it, and the list is then rightly drawn again
const countDraws = (page, sel, box) => page.evaluate(([sel, box]) => {
  window.draws = 0;
  window.keyAt = [];
  document.querySelector(box).addEventListener("input", (e) => window.keyAt.push(e.timeStamp));
  new MutationObserver((ms) => { if (ms.some((m) => m.target.closest?.(sel) || m.target.matches?.(sel))) window.draws++; })
    .observe(document.querySelector(sel).parentElement, { childList: true, subtree: true });
}, [sel, box]);
const draws = (page) => page.evaluate(() => window.draws);
const pauses = (page) => page.evaluate(() => window.keyAt.slice(1).filter((at, i) => at - window.keyAt[i] >= 150).length);
const until = async (page, ok, what) => {
  for (let i = 0; i < 250 && !ok(); i++) await page.waitForTimeout(20);
  assert.ok(ok(), what);
};
const PINYIN = ["n", "ni", "nih", "niha", "nihao"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: the Sessions page searches once typing pauses, not while composing`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const [lang, width] of [["en", 1100], ["zh", 1100], ["zh-TW", 560], ["ja", 560], ["de", 420]]) {
      await t.test(`${lang} at ${width}`, async () => {
        const context = await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const log = [];
        await page.route("**/*", serve(lang, log));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator('#nav [data-view="sessions"]').first().click();
        const view = page.locator("#view-sessions");
        const row = (id) => view.locator(`.row.sm-sess[data-id="${id}"]`);
        await row("ses_03").waitFor();
        const filter = view.locator(".sm-filter");
        const searches = () => log.filter((x) => x.kind === "search").map((x) => x.q);

        // typed fast: the list is drawn once for the words, and once more
        // for what was said, not once a keystroke
        await countDraws(page, "#view-sessions .sm-tree", "#view-sessions .sm-filter");
        await filter.pressSequentially("about kafka", { delay: 30 });
        await row("ses_03").locator(".sm-hit mark").waitFor();
        await page.waitForTimeout(300);
        assert.deepEqual(searches(), ["about kafka"], "one search for the words typed");
        const n = await draws(page), p = await pauses(page);
        assert.ok(n <= 4 + p, `the list was drawn ${n} times for 11 keystrokes with ${p} pauses`);
        assert.equal(await row("ses_01").count(), 0);

        // Escape counts at once: the whole list is back as it returns
        await filter.press("Escape");
        assert.equal(await row("ses_01").count(), 1, "Escape didn't bring the list back at once");

        // pinyin being composed asks nothing and filters nothing, however
        // long the reader lingers over the candidates
        log.length = 0;
        await compose(engine, page, filter, PINYIN, "你好");
        assert.deepEqual(log, [], "a search ran while composing");
        // the characters picked: one search for them
        await row("ses_04").locator(".sm-hit mark").waitFor();
        await page.waitForTimeout(300);
        assert.deepEqual(searches(), ["你好"]);
        assert.equal(await row("ses_04").locator(".sm-hit mark").textContent(), "你好");
        assert.equal(await row("ses_01").count(), 0);

        // Enter counts at once
        await filter.fill("haiku");
        await filter.press("Enter");
        assert.equal(await row("ses_04").count(), 0, "Enter didn't filter at once");
        assert.equal(await row("ses_03").count(), 1);
        await until(page, () => searches().includes("haiku"), "Enter searched nothing");

        assert.deepEqual(errors, []);
        await context.close();
      });
    }
  });

  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: Usage's Sessions and Requests searches wait for the words`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const errors = [], log = [];
      const context = await browser.newContext({ viewport: { width: 1100, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(8000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.addInitScript(() => { localStorage.setItem("magpie.usageTab", "sessions"); localStorage.setItem("magpie.sessRange", "all"); });
      await page.route("**/*", serve(lang, log));
      await page.goto("http://magpie.test/?view=usage");
      await page.locator("#sessGrid:not([hidden])").waitFor();
      const rows = page.locator("#sessList .row.sess");
      await rows.first().waitFor();
      const all = await rows.count();
      const q = page.locator("#sessQ");

      // Usage › Sessions: typed fast, one drawing of the list
      await countDraws(page, "#sessList", "#sessQ");
      await q.pressSequentially("kafka autumn", { delay: 30 });
      await page.waitForFunction(() => document.querySelectorAll("#sessList .row.sess").length === 1);
      await page.waitForTimeout(300);
      const n = await draws(page), p = await pauses(page);
      assert.ok(n <= 2 + p, `the list was drawn ${n} times for 12 keystrokes with ${p} pauses`);
      await q.press("Escape");
      assert.equal(await rows.count(), all, "Escape didn't bring the list back at once");

      // composing filters nothing until the characters are picked
      await compose(engine, page, q, PINYIN, "你好");
      assert.equal(await rows.count(), all, "the list was filtered by pinyin being composed");
      await page.waitForFunction(() => document.querySelectorAll("#sessList .row.sess").length === 1);
      assert.match(await rows.first().innerText(), /你好/);
      await q.fill("Session 1");
      await q.press("Enter");
      assert.equal(await rows.filter({ hasText: "你好" }).count(), 0, "Enter didn't filter at once");

      // Usage › Requests: composing reads nothing; the words picked, one read
      await page.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
      await page.locator("#ledWrap .led tbody tr").first().waitFor();
      const box = page.locator("#ledQ");
      await box.scrollIntoViewIfNeeded();
      log.length = 0;
      await compose(engine, page, box, PINYIN, "你好");
      // (the page's own refresh may read it again meanwhile, with the words
      // it had)
      const reads = () => log.filter((x) => x.kind === "requests").map((x) => x.q);
      assert.deepEqual(reads().filter((q) => q !== ""), [], "Requests was read while composing");
      await page.waitForFunction(() => /asked-你好/.test(document.querySelector("#ledWrap")?.textContent || ""));
      assert.deepEqual(reads().filter((q) => q !== ""), ["你好"]);
      // Enter reads at once
      log.length = 0;
      await box.fill("gpt");
      await box.press("Enter");
      await until(page, () => log.some((x) => x.kind === "requests" && x.q === "gpt"), "Enter read nothing");
      assert.deepEqual(errors, []);
    });
  }
}
