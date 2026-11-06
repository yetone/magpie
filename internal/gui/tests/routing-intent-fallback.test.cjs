// Run with Node's test runner and Playwright on the module path; see README.md.
// A group's classifier that couldn't say which kind a turn is (timed out,
// failed, resting after a failure) leaves the turn with the intent the turn
// before had, rather than none (#1826). The trace tells it couldn't, why,
// and the intent the turn kept; a failure with nothing to keep still says
// no rule with an intent matches. In every language, and at a phone's
// width the reason stays on the page.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/fast", name: "fast", providerName: "A", icon: "generic", context: 256000, ready: true },
  { id: "b/opus", name: "opus", providerName: "B", icon: "generic", context: 1000000, ready: true },
];
const groups = () => ({
  models,
  groups: [{ id: "main", name: "Main", members: ["a/fast", "b/opus"], routing: "order", ready: true, classifier: "c/cls",
    memberInfo: [{ id: "a/fast", ready: true, context: 256000 }, { id: "b/opus", ready: true, context: 1000000 }],
    rules: [{ use: "b/opus", intent: "complex" }] }],
  pools: [],
});
const now = Date.now();
const err = "c/cls: no answer in 8s";
const routeOf = (kept) => ({
  id: 7, seq: 7, time: new Date(now - 2000).toISOString(), agent: "claude", model: "main", provider: kept ? "b" : "a", done: true, status: 200, ms: 900, tokens: 9000,
  group: { id: "main", name: "Main", members: ["a/fast", "b/opus"] },
  order: kept
    ? [{ id: "b", provider: "b", name: "B", kind: "key", model: "opus", known: true }, { id: "a", provider: "a", name: "A", kind: "key", model: "fast", known: true }]
    : [{ id: "a", provider: "a", name: "A", kind: "key", model: "fast", known: true }, { id: "b", provider: "b", name: "B", kind: "key", model: "opus", known: true }],
  tries: [{ id: kept ? "b" : "a", model: kept ? "opus" : "fast", start: new Date(now - 2000).toISOString(), done: true, ms: 800, status: 200 }],
  rule: kept
    ? { n: 1, use: "b/opus", when: ['intent "complex"'], turn: 3, tokens: 9000,
      classified: { by: "c/cls", intents: ["complex"], intent: "complex", after: "complex", error: err, fallback: "previous" } }
    : { n: 0, turn: 1, tokens: 9000, classified: { by: "c/cls", intents: ["complex"], error: err } },
});

const words = {
  en: { kept: `c/cls was to tell which of “complex” turn 3 is, but couldn't — ${err} — so the turn keeps “complex”, as turn 2 was.`,
    none: `c/cls was to tell which of “complex” turn 1 is, but couldn't — ${err} — so no rule with an intent matches it.` },
  zh: { kept: `c/cls 未能判断第 3 轮属于 “complex” 中的哪一个（${err}），因此沿用第 2 轮的“complex”。`,
    none: `c/cls 未能判断第 1 轮属于 “complex” 中的哪一个（${err}），带意图的规则均不命中。` },
  "zh-TW": { kept: `c/cls 未能判斷第 3 輪屬於 “complex” 中的哪一個（${err}），因此沿用第 2 輪的「complex」。`,
    none: `c/cls 未能判斷第 1 輪屬於 “complex” 中的哪一個（${err}），帶意圖的規則均不命中。` },
  ja: { kept: `c/cls はターン 3 が “complex” のどれかを判定するはずでしたが、できませんでした（${err}）。そのため、ターン 2 と同じ「complex」のままにします。`,
    none: `c/cls はターン 1 が “complex” のどれかを判定するはずでしたが、できませんでした（${err}）。そのため、インテントを含むルールは一致しません。` },
  de: { kept: `c/cls sollte bestimmen, welche von “complex” Runde 3 ist, konnte es aber nicht – ${err} –, daher bleibt die Runde bei „complex“ wie Runde 2.`,
    none: `c/cls sollte bestimmen, welche von “complex” Runde 1 ist, konnte es aber nicht – ${err} –, daher trifft keine Regel mit Absicht zu.` },
};

function serve(lang, route) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  let first = true;
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      const routes = first ? [route] : [];
      first = false;
      return json({ mine: true, now: new Date().toISOString(), seq: 7, totals: { requests: 1, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(t, engine, lang, width, kept) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 1400 } })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, routeOf(kept)));
  await page.goto("http://magpie.test/?view=routing");
  await page.locator(".rt-group").first().waitFor({ state: "attached" });
  return { page, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    for (const width of [1100, 390]) {
      test(`${engine} ${lang} ${width}px: a classifier that couldn't say keeps the turn before's intent`, async (t) => {
        const { page, errors } = await open(t, engine, lang, width, true);
        await page.waitForFunction((s) => document.documentElement.innerText.includes(s), w.kept).catch(() => {});
        const said = await page.evaluate(() => document.documentElement.innerText);
        assert(said.includes(w.kept), `the kept intent is told: ${said.slice(0, 3000)}`);
        assert(!said.includes(w.none), "not said as no intent");
        const wide = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert(wide <= 0, `no sideways scroll: ${wide}px`);
        assert.deepEqual(errors, []);
      });
    }
    test(`${engine} ${lang}: a classifier that couldn't say, with nothing to keep`, async (t) => {
      const { page, errors } = await open(t, engine, lang, 1100, false);
      await page.waitForFunction((s) => document.documentElement.innerText.includes(s), w.none).catch(() => {});
      const said = await page.evaluate(() => document.documentElement.innerText);
      assert(said.includes(w.none), `no intent is told: ${said.slice(0, 3000)}`);
      assert.deepEqual(errors, []);
    });
  }
}
