// Run with Node's test runner and Playwright on the module path; see README.md.
// A chat classifier asks a turn's intent and how hard it is in two calls.
// When it tells the intent but not the effort, the turn keeps the intent
// it was told, and the effort's failure is said beside it (@zyh6036 on X:
// Codex, which always asks for reasoning, lost every turn's intent with
// it and stayed on the classifier's model). In every language, and at a
// phone's width.
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
const routeOf = () => ({
  id: 7, seq: 7, time: new Date(now - 2000).toISOString(), agent: "claude", model: "main", provider: "a", done: true, status: 200, ms: 900, tokens: 9000,
  group: { id: "main", name: "Main", members: ["a/fast", "b/opus"] },
  order: [{ id: "a", provider: "a", name: "A", kind: "key", model: "fast", known: true }, { id: "b", provider: "b", name: "B", kind: "key", model: "opus", known: true }],
  tries: [{ id: "a", model: "fast", start: new Date(now - 2000).toISOString(), done: true, ms: 800, status: 200 }],
  rule: { n: 0, turn: 2, tokens: 9000, classified: { by: "c/cls", intents: ["complex"], intent: "complex", said: "1", ms: 300,
    effortError: 'it answered "This is a moderately complex request.", not a level from 1 to 4' } },
});

// the intent told, and the effort that couldn't be, in each language
const words = {
  en: { intent: "c/cls was asked which of “complex” turn 2 is, and said “complex” (",
    effort: "c/cls was to rate how hard turn 2 is, but couldn't — it answered" },
  zh: { intent: "c/cls 判断第 2 轮属于 “complex” 中的“complex”（", effort: "c/cls 未能评估第 2 轮的难度（it answered" },
  "zh-TW": { intent: "c/cls 判斷第 2 輪屬於 “complex” 中的「complex」（", effort: "c/cls 未能評估第 2 輪的難度（it answered" },
  ja: { intent: "c/cls にターン 2 が “complex” のどれかを尋ねたところ、「complex」と答えました（", effort: "c/cls はターン 2 の難しさを評価するはずでしたが、できませんでした（it answered" },
  de: { intent: "c/cls wurde gefragt, welche von “complex” Runde 2 ist, und sagte „complex“ (", effort: "c/cls sollte einschätzen, wie schwer Runde 2 ist, konnte es aber nicht – it answered" },
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

async function open(t, engine, lang, width) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 1400 } })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, routeOf()));
  await page.goto("http://magpie.test/?view=routing");
  await page.locator("#view-routing .rt-stage").waitFor();
  return { page, errors };
}

const text = (page) => page.evaluate(() => document.documentElement.innerText);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    for (const width of [1100, 390]) {
      test(`${engine} ${lang} ${width}px: the intent told holds, and the effort that couldn't be is said`, async (t) => {
        const { page, errors } = await open(t, engine, lang, width);
        await page.waitForFunction((s) => document.documentElement.innerText.includes(s), w.effort).catch(() => {});
        const said = await text(page);
        assert(said.includes(w.intent), `the intent is told: ${said.slice(0, 3000)}`);
        assert(said.includes(w.effort), `the effort's failure is told: ${said.slice(0, 3000)}`);
        const wide = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert(wide <= 0, `no sideways scroll: ${wide}px`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
