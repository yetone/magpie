const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

// A conversation a later member of a group routed In order answered goes
// back to the first member at its next turn, once that one is ready
// (yyhhyyyyyy on Discord: an API key first, a Claude subscription second,
// 8 of 10 requests on the subscription). The Routing page says why it
// moved, as it does for a provider's fallback giving one back ("main"),
// which said nothing before. A conversation Stays: Strict keeps on the
// fallback, the provider ready again, says that too ("strict" with main;
// 01huadalang on Discord, 10-31). A member of a group routed any other
// way that stood in for one listed ahead of it gives the conversation back
// too, and says so ("back"; 01huadalang, 11-01: a MiMo backup kept a Codex
// session by its cache).
const words = {
  en: { back: ["listed ahead of it in the group", "turn 2 goes back"], order: ["the group goes in order", "turn 2 goes back"], accounts: ["the provider goes in order", "turn 2 goes back"], main: ["a fallback", "turn 2 goes back"], strict: ["a fallback", "Strict", "new conversations go to"] },
  zh: { back: ["排在它前面", "第 2 轮回到它"], order: ["该组按顺序路由", "第 2 轮回到"], accounts: ["该提供商按顺序路由", "第 2 轮回到"], main: ["备用", "第 2 轮回到它"], strict: ["备用", "严格", "新会话会走"] },
  "zh-TW": { back: ["排在它前面", "第 2 輪回到它"], order: ["該群組按順序路由", "第 2 輪回到"], accounts: ["該供應商按順序路由", "第 2 輪回到"], main: ["備用", "第 2 輪回到它"], strict: ["備用", "嚴格", "新會話會走"] },
  ja: { back: ["受けられなかった", "ターン 2 は"], order: ["順番どおり", "ターン 2 は"], accounts: ["このプロバイダーは順番どおり", "ターン 2 は"], main: ["代替", "ターン 2 は"], strict: ["代替", "厳格", "新しい会話は"] },
  de: { back: ["in der Gruppe davor", "Runde 2 dorthin zurück"], order: ["der Reihe nach", "Runde 2 zurück"], accounts: ["der Anbieter geht der Reihe nach", "Runde 2 zurück"], main: ["ein Ersatz", "Runde 2 dorthin zurück"], strict: ["ein Ersatz", "Strikt", "neue Unterhaltungen gehen an"] },
};

function routes(now, as) {
  // "accounts": a provider routing its own keys In order, no group (CryptoSarom on X)
  const why = as === "accounts" ? "order" : as;
  const time = new Date(now.getTime() - 5000).toISOString();
  const at = new Date(now.getTime() - 65000).toISOString();
  const k1 = { id: "cw#k1", provider: "cw", name: "CW", kind: "key", who: "k1", model: "m" };
  const k2 = { id: "cw#k2", provider: "cw", name: "CW", kind: "key", who: "k2", model: "m", fallback: why !== "order" && why !== "back" };
  // Strict leaves the plan the one it keeps, and names the main beside it
  const order = why === "strict" ? [k2] : [k1, k2];
  return [{ id: 1, seq: 1, time, agent: "claude", model: "cw/m", provider: "cw", order, done: true, status: 200, ms: 20,
    group: as === "order" ? { id: "g", name: "G", routing: "order" } : why === "back" ? { id: "g", name: "G", routing: "" } : undefined,
    affinity: { mode: why === "strict" ? "strict" : "", turn: 2, last: "cw#k2", lastTurn: 1, at, cacheRead: 20000, cacheWrite: 3000, why, main: why === "strict" ? k1 : undefined },
    tries: [{ id: order[0].id, model: "m", start: time, done: true, status: 200, ms: 20 }] }];
}

function serve(lang, getRoutes, now) {
  const assets = path.resolve(__dirname, "../assets");
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs={lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window={};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.has("wait")) return;
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: getRoutes() });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"]) {
  for (const width of [1100, 420]) {
    for (const lang of Object.keys(words)) {
      test(`${engine} ${width} ${lang}: affinity says a conversation went back to the first`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await browser.newPage({ viewport: { width, height: 900 } });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const now = new Date();
        let rs;
        await page.route("**/*", serve(lang, () => rs, now));
        const story = async () => {
          await page.goto("http://magpie.test/?view=routing");
          await page.waitForFunction(() => /\d/.test(document.body.innerText) && document.querySelector(".rt-cap")?.textContent);
          return page.evaluate(() => document.body.innerText + "\n" + document.querySelector(".rt-cap").textContent);
        };
        for (const why of ["back", "order", "accounts", "main", "strict"]) {
          rs = routes(now, why);
          const text = await story();
          for (const w of words[lang][why]) assert.ok(text.includes(w), `${why}: ${w} not in\n${text}`);
          assert.doesNotMatch(text, /\{(who|first|n)\}/);
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "no sideways scroll");
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
