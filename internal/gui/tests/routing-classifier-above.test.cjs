// Run with Node's test runner and Playwright on the module path; see README.md.
// A group in a group, effort=auto of its own, isn't asked how hard the
// turn is once the group above picked its reasoning (#4358): the Routing
// page says it wasn't asked and what the group above picked, not that it
// "couldn't" and the turn "reasons as the agent asked", which the turn
// doesn't. In every language, and at a phone's width.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/fast", name: "fast", providerName: "A", icon: "generic", context: 256000, ready: true },
];
const groups = () => ({
  models,
  groups: [
    { id: "quality", name: "Quality", members: ["a/fast"], routing: "order", ready: true, classifier: "cc/cls", effort: "auto",
      memberInfo: [{ id: "a/fast", ready: true, context: 256000 }] },
    { id: "main", name: "Main", members: ["group/quality"], routing: "order", ready: true, classifier: "cp/cls", effort: "auto",
      memberInfo: [{ id: "group/quality", ready: true, context: 256000 }] },
  ],
  pools: [],
});
const now = Date.now();
const routeOf = () => ({
  id: 7, seq: 7, time: new Date(now - 2000).toISOString(), agent: "claude", model: "main", provider: "a", done: true, status: 200, ms: 900, tokens: 9000,
  effort: "medium",
  group: { id: "main", name: "Main", members: ["group/quality"] },
  order: [{ id: "a", provider: "a", name: "A", kind: "key", model: "fast", known: true, via: ["quality"] }],
  tries: [{ id: "a", model: "fast", start: new Date(now - 2000).toISOString(), done: true, ms: 800, status: 200, effort: "high" }],
  rule: { turn: 2, tokens: 9000, pick: "high", classified: { by: "cp/cls", intents: null, effort: "high", score: 2.2, ms: 300 } },
  nested: [{ group: "quality", name: "Quality", rule: { turn: 2, tokens: 9000, classified: { by: "cc/cls", intents: null, above: "high" } } }],
});

// what the group in it says, in each language
const words = {
  en: "cc/cls wasn't asked how hard turn 2 is: the group above picked high reasoning for it.",
  zh: "cc/cls 未被要求评估第 2 轮的难度：上一层分组已为这一轮选了 high 推理强度。",
  "zh-TW": "cc/cls 未被要求評估第 2 輪的難度：上一層群組已為這一輪選了 high 推理強度。",
  ja: "cc/cls にはターン 2 の難しさを尋ねていません。上位のグループがこのターンの推論に high を選んだためです。",
  de: "cc/cls wurde nicht gefragt, wie schwer Runde 2 ist: Die Gruppe darüber hat für sie high als Reasoning gewählt.",
};
// what it said before: that it couldn't, and the turn reasons as asked
const before = {
  en: "reasons as the agent asked", zh: "按 agent 要求的推理强度", "zh-TW": "按 agent 要求的推理強度",
  ja: "エージェントの指定どおりに推論します", de: "vom Agenten verlangte Reasoning",
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
    for (const width of [1100, 390]) {
      test(`${engine} ${lang} ${width}px: a group in a group says the group above picked its turn's reasoning`, async (t) => {
        const { page, errors } = await open(t, engine, lang, width);
        await page.waitForFunction((s) => document.documentElement.innerText.includes(s), words[lang]).catch(() => {});
        const said = await text(page);
        assert(said.includes(words[lang]), `told: ${said.slice(0, 3000)}`);
        assert(!said.includes(before[lang]), "not told it reasons as the agent asked");
        assert(!/\{(by|turn|level)\}/.test(said), "no placeholder left");
        const wide = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert(wide <= 0, `no sideways scroll: ${wide}px`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
