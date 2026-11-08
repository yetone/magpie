const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

// #5655 (jerrychoi; iamnil on Discord): a session's call to another model
// than its chat's (Claude Desktop's own Sonnet beside an Opus chat) goes
// to the account the chat is on (withSession, why "lead"), and the
// Routing page says so, naming the chat's model.
const words = {
  en: ["where this session's", "claude-opus-5-5", "one account"],
  zh: ["这个会话的", "claude-opus-5-5", "一个账号"],
  "zh-TW": ["這個會話的", "claude-opus-5-5", "一個帳號"],
  ja: ["このセッションの", "claude-opus-5-5", "1 つのアカウント"],
  de: ["dieser Sitzung", "claude-opus-5-5", "einem Konto"],
};

function routes(now) {
  const time = new Date(now.getTime() - 5000).toISOString();
  const at = new Date(now.getTime() - 65000).toISOString();
  const a = { id: "claude#a", provider: "claude", name: "Claude", kind: "account", who: "a@example.com", model: "claude-sonnet-5" };
  const b = { id: "claude#b", provider: "claude", name: "Claude", kind: "account", who: "b@example.com", model: "claude-sonnet-5" };
  return [{ id: 1, seq: 1, time, agent: "claude-desktop", model: "claude/claude-sonnet-5", provider: "claude", order: [a, b], done: true, status: 200, ms: 20,
    affinity: { mode: "", turn: 3, last: "claude#b", lastTurn: 2, at, cacheRead: 68925, cacheWrite: 110000, why: "lead", leadModel: "claude-opus-5-5" },
    tries: [{ id: a.id, model: "claude-sonnet-5", start: time, done: true, status: 200, ms: 20 }] }];
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
      test(`${engine} ${width} ${lang}: affinity says a side call went with its session's account`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await browser.newPage({ viewport: { width, height: 900 } });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const now = new Date();
        await page.route("**/*", serve(lang, () => routes(now), now));
        await page.goto("http://magpie.test/?view=routing");
        await page.waitForFunction(() => /\d/.test(document.body.innerText) && document.querySelector(".rt-cap")?.textContent);
        const text = await page.evaluate(() => document.body.innerText + "\n" + document.querySelector(".rt-cap").textContent);
        for (const w of words[lang]) assert.ok(text.includes(w), `${w} not in\n${text}`);
        assert.doesNotMatch(text, /\{(first|model)\}/);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "no sideways scroll");
        assert.deepEqual(errors, []);
      });
    }
  }
}
