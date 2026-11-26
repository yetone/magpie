// Run with Node's test runner and Playwright on the module path; see README.md.
// A smart routing group keeps a conversation on the model its cache is
// warm on when the classifier only takes a turn to be easier (chet030714
// on X: one Claude Code conversation flipped between two models, and each
// flip read the whole history uncached). The Routing page says which rule
// the turn matched, where that would have sent it, and why it stayed —
// not only that the account was kept for its cache; a group in the group
// says so too. In every language, and at a phone's width.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HARD = "a hard task: designing, debugging, a change across several files, or planning";
const EASY = "an easy task: a quick question, a small edit, a lookup, or running a command";
const models = [
  { id: "a/strong", name: "strong", providerName: "A", icon: "generic", context: 1000000, ready: true },
  { id: "b/light", name: "light", providerName: "B", icon: "generic", context: 1000000, ready: true },
];
const groups = () => ({
  models,
  groups: [
    { id: "smart", name: "Smart", members: ["a/strong", "b/light"], routing: "order", ready: true, classifier: "b/light",
      memberInfo: [{ id: "a/strong", ready: true, context: 1000000 }, { id: "b/light", ready: true, context: 1000000 }],
      rules: [{ use: "a/strong", intent: HARD }, { use: "b/light", intent: EASY }] },
    { id: "main", name: "Main", members: ["group/smart"], routing: "order", ready: true,
      memberInfo: [{ id: "group/smart", ready: true, context: 1000000 }] },
  ],
  pools: [],
});
const now = Date.now();
const rule = () => ({ n: 1, use: "a/strong", when: [`intent "${HARD}"`], turn: 3, tokens: 48000, stay: { n: 2, use: "b/light", tokens: 46000 },
  classified: { by: "b/light", intents: [HARD, EASY], intent: EASY, after: HARD, ms: 300 } });
const base = () => ({
  id: 7, seq: 7, time: new Date(now - 2000).toISOString(), agent: "claude", provider: "a", done: true, status: 200, ms: 900, tokens: 48000,
  order: [{ id: "a", provider: "a", name: "A", kind: "key", model: "strong", known: true }, { id: "b", provider: "b", name: "B", kind: "key", model: "light", known: true }],
  tries: [{ id: "a", model: "strong", start: new Date(now - 2000).toISOString(), done: true, ms: 800, status: 200 }],
  affinity: { why: "cache", last: "a", turn: 3, cacheRead: 45000, cacheWrite: 1000, at: new Date(now - 40000).toISOString() },
});
// the smart group itself, and the smart group as a group in Main
const routes = {
  own: () => ({ ...base(), model: "smart", group: { id: "smart", name: "Smart", members: ["a/strong", "b/light"] }, rule: rule() }),
  nested: () => {
    const r = base();
    r.order = r.order.map((x) => ({ ...x, via: ["smart"] }));
    return { ...r, model: "main", group: { id: "main", name: "Main", members: ["a/strong", "b/light"], via: ["smart", "smart"], subs: [{ id: "smart", name: "Smart" }] },
      rule: { turn: 3, tokens: 48000 }, nested: [{ group: "smart", name: "Smart", rule: rule() }] };
  },
};

// what it says, in each language
const words = {
  own: {
    en: "Turn 3 matches rule 2, which sends it to b/light, but the conversation is cached on a/strong (about ",
    zh: "第 3 轮命中规则 2，本应换到 b/light；但会话在 a/strong 上的缓存还热（约 ",
    "zh-TW": "第 3 輪命中規則 2，本應換到 b/light；但會話在 a/strong 上的快取還熱（約 ",
    ja: "ターン 3 はルール 2 に一致し、b/light に送られるところですが、会話は a/strong にキャッシュされており（約 ",
    de: "Runde 3 trifft Regel 2 zu, die sie an b/light schickt, aber die Unterhaltung ist bei a/strong im Cache (etwa ",
  },
  nested: {
    en: "In Smart, turn 3 matches rule 2, which sends it to b/light, but the conversation is cached on a/strong and b/light is no step up from it, so it stays on a/strong.",
    zh: "在 Smart 中，第 3 轮命中规则 2，本应换到 b/light；但会话在 a/strong 上有缓存，b/light 也不比它强，所以留在 a/strong。",
    "zh-TW": "在 Smart 中，第 3 輪命中規則 2，本應換到 b/light；但會話在 a/strong 上有快取，b/light 也不比它強，所以留在 a/strong。",
    ja: "Smart で、ターン 3 はルール 2 に一致し、b/light に送られるところですが、会話は a/strong にキャッシュされており、b/light もそれより上ではないため、a/strong にとどまります。",
    de: "In Smart trifft Runde 3 Regel 2 zu, die sie an b/light schickt, aber die Unterhaltung ist bei a/strong im Cache und b/light ist keine Stufe höher, daher bleibt sie bei a/strong.",
  },
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

async function open(t, engine, lang, width, route) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 1400 } })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, route));
  await page.goto("http://magpie.test/?view=routing");
  await page.locator("#view-routing .rt-stage").waitFor();
  return { page, errors };
}

const text = (page) => page.evaluate(() => document.documentElement.innerText);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const kind of Object.keys(routes)) {
    for (const lang of Object.keys(words[kind])) {
      for (const width of [1100, 390]) {
        test(`${engine} ${lang} ${width}px: ${kind === "own" ? "a" : "a group in a group's"} turn kept where its cache is says so`, async (t) => {
          const { page, errors } = await open(t, engine, lang, width, routes[kind]());
          const want = words[kind][lang];
          await page.waitForFunction((s) => document.documentElement.innerText.includes(s), want).catch(() => {});
          const said = await text(page);
          assert(said.includes(want), `told: ${said.slice(0, 3000)}`);
          assert(!/\{(turn|m|other|use|tokens|group)\}/.test(said), "no placeholder left");
          const wide = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
          assert(wide <= 0, `no sideways scroll: ${wide}px`);
          assert.deepEqual(errors, []);
        });
      }
    }
  }
}
