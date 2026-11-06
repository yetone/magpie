// Run with Node's test runner and Playwright on the module path; see README.md.
// When smart routing's decision model said which intent a turn is, and a
// rule by that intent put the request's first where it is, that row says
// the intent where an account's allowance goes (joey_xue on Discord); one
// answering or that answered keeps saying so, the intent after it. A
// route a rule put first by no intent, and every other row, still say
// what they have left. In every language, and at a phone's width.
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
// how: "waiting" (not tried yet), "answered", or "tokens" (a rule by size, no intent)
const routeOf = (how) => ({
  id: 7, seq: 7, time: new Date(now - 2000).toISOString(), agent: "claude", model: "main", provider: "b",
  ...(how === "answered" ? { done: true, status: 200, ms: 900 } : {}), tokens: 9000,
  group: { id: "main", name: "Main", members: ["a/fast", "b/opus"] },
  order: [{ id: "b", provider: "b", name: "B", kind: "account", model: "opus", known: true, used: 10 },
    { id: "a", provider: "a", name: "A", kind: "account", model: "fast", known: true, used: 20 }],
  tries: how === "answered" ? [{ id: "b", model: "opus", start: new Date(now - 2000).toISOString(), done: true, ms: 800, status: 200 }] : [],
  rule: how === "tokens"
    ? { n: 1, use: "b/opus", when: ["tokens ≥ 8000"], turn: 3, tokens: 9000 }
    : { n: 1, use: "b/opus", when: ['intent "complex"'], turn: 3, tokens: 9000,
      classified: { by: "c/cls", intents: ["complex"], intent: "complex", ms: 300 } },
});

const words = {
  en: { picked: "picked for “complex”", answered: "answered this request" },
  zh: { picked: "按意图“complex”选中", answered: "回答了这个请求" },
  "zh-TW": { picked: "按意圖「complex」選中", answered: "回答了這個請求" },
  ja: { picked: "インテント「complex」で選択", answered: "このリクエストに応答" },
  de: { picked: "für „complex“ gewählt", answered: "hat diese Anfrage beantwortet" },
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

async function open(t, engine, lang, width, how) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 1400 } })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, routeOf(how)));
  await page.goto("http://magpie.test/?view=routing");
  await page.locator("#view-routing .rt-stage").waitFor();
  return { page, errors };
}

// what each account's row says, by its id (the row's tooltip begins with it)
const said = (page) => page.evaluate(() => Object.fromEntries([...document.querySelectorAll(".rt-accts li")]
  .map((li) => [li.title.split("\n")[0], li.querySelector(":scope > em")?.textContent || ""])));
const until = (page, f) => page.waitForFunction(f).catch(() => {});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    for (const width of [1100, 390]) {
      test(`${engine} ${lang} ${width}px: the row smart routing chose says the intent, not what it has left`, async (t) => {
        const { page, errors } = await open(t, engine, lang, width, "waiting");
        await until(page, () => [...document.querySelectorAll(".rt-accts li")].some((li) => li.title.startsWith("a") && /\d/.test(li.querySelector(":scope > em")?.textContent || "")));
        await until(page, (p) => [...document.querySelectorAll(".rt-accts li em")].some((e) => e.textContent === p), w.picked);
        const s = await said(page);
        assert.equal(s.b, w.picked, `the chosen row: ${JSON.stringify(s)}`);
        assert(/\d/.test(s.a) && !s.a.includes(w.picked), `the other row still says what it has left: ${JSON.stringify(s)}`);
        const wide = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert(wide <= 0, `no sideways scroll: ${wide}px`);
        assert.deepEqual(errors, []);
      });
    }
    test(`${engine} ${lang}: the row that answered says so, then the intent`, async (t) => {
      const { page, errors } = await open(t, engine, lang, 1100, "answered");
      const want = `${w.answered} · ${w.picked}`;
      await until(page, (p) => [...document.querySelectorAll(".rt-accts li em")].some((e) => e.textContent === p), want);
      const s = await said(page);
      assert.equal(s.b, want, JSON.stringify(s));
      assert.deepEqual(errors, []);
    });
    test(`${engine} ${lang}: a rule by no intent leaves the row saying what it has left`, async (t) => {
      const { page, errors } = await open(t, engine, lang, 1100, "tokens");
      await until(page, () => [...document.querySelectorAll(".rt-accts li")].filter((li) => /\d/.test(li.querySelector(":scope > em")?.textContent || "")).length >= 2);
      const s = await said(page);
      assert(/\d/.test(s.b) && !s.b.includes(w.picked), JSON.stringify(s));
      assert.deepEqual(errors, []);
    });
  }
}
