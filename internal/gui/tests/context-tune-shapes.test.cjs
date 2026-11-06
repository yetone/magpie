// Run with Node's test runner and Playwright on the module path; see README.md.
// Usage › Context's Tuned for you draws what /api/tune answers, and the
// magpie answering can be of another version than the page (Linka on
// Discord, a Docker magpie: "undefined is not an object (evaluating
// 'r.cost')"). A row put in with too few calls has no replay (few, no
// compact or ttl), and a row this page doesn't know, a null or one with
// no setting, is left out: the page draws the rest and throws nothing, in
// Chromium and WebKit, in English and Chinese, at 900 and 420 wide.
// MAGPIE_ASSETS runs it against another version's assets.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets"));
const facts = { calls: 4100, streams: 60, compacts: 0, peak: 166000, after: 57000, growth: 1000, rework: 4700, quick: 0.95, pause: 0.045, away: 0.005, hour: 0.9 };
// as magpie sends them since v0.1.3120: put in, too few calls, no replay
const fewRows = { knob: "cache_ttl", setting: { value: "1h", source: "settings", ours: true }, few: true };
const codex = { agent: "codex", calls: 900, advice: [
  { knob: "compact", setting: {}, compact: { current: 200000, best: 200000, cost: 2e8, low: 2e8, curve: [{ value: 100000, cost: 3e8 }, { value: 200000, cost: 2e8 }], facts } },
] };
const cases = {
  // the reply the report's page met
  "too few calls": { days: 7, agents: [{ agent: "claude", calls: 3, advice: [fewRows, { knob: "compact", model: "claude-haiku-4-5", setting: {}, few: true }] }, codex] },
  // rows and agents this page doesn't know: left out
  "a shape not known": { days: 7, agents: [
    { agent: "claude", calls: 3, advice: [
      fewRows,
      { knob: "subagent_cache_ttl", setting: { value: "5m" } },
      { knob: "compact", model: "claude-opus-5-5", compact: null, ttl: null },
      null,
      "cache_ttl",
      // too few calls, its setting not said
      { knob: "compact", model: "claude-haiku-4-5", few: true },
    ] },
    codex,
    null,
  ] },
};

function serve(lang, tune) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }, { id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/tune") return json(tune);
    if (url.pathname === "/api/context") return json({ days: 7, agents: [], sessions: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType, status: 200 });
  };
}

const few = { en: "Not enough calls in this range to tune on", zh: "这段时间的调用太少，还算不出建议" };
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [name, tune] of Object.entries(cases)) test(`${engine}, ${name}: Tuned for you draws the rows it knows`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(few)) for (const [width, height] of [[900, 760], [420, 760]]) {
      await t.test(`${lang}, ${width}x${height}`, async () => {
        const ctx = await browser.newContext({ viewport: { width, height }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => {
          try { localStorage.setItem("magpie.usageTab", "context"); } catch {}
          // every line the status pill says, which an error leaves after 8s
          window.__said = [];
          new MutationObserver(() => { const s = document.querySelector("#status"); if (s?.textContent) window.__said.push(s.textContent); })
            .observe(document, { subtree: true, childList: true, characterData: true });
        });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, tune));
        await page.goto("http://magpie.test/?view=usage");
        await page.locator(".ctx-tunes").waitFor({ timeout: 10000 }).catch(() => {});
        assert.deepEqual(errors, []);
        // the load's failure is said in the status line, as the report shows it
        const said = await page.evaluate(() => [document.body.innerText, ...window.__said].join("\n"));
        assert.doesNotMatch(said, /is not an object|Cannot read properties|TypeError|is not iterable/);
        const cards = page.locator(".ctx-tune");
        assert.equal(await cards.count(), 2);
        const claude = cards.nth(0).locator(".ctx-tune-row");
        assert.equal(await claude.count(), 2, "the two few rows, nothing for the rows not known");
        for (const i of [0, 1]) {
          assert.equal(await claude.nth(i).locator(".ctx-tune-why").innerText(), few[lang]);
          assert.doesNotMatch(await claude.nth(i).innerText(), /NaN|undefined|null/);
        }
        assert.equal(await claude.nth(0).locator("button").count(), 1, "Undo");
        assert.equal(await cards.nth(1).locator(".ctx-tune-row").count(), 1);
        const over = await page.evaluate(() => {
          const view = document.querySelector("#view-usage");
          return view.scrollWidth > view.clientWidth + 1 ? view.scrollWidth + ">" + view.clientWidth : "";
        });
        assert.equal(over, "");
        await ctx.close();
      });
    }
  });
}
