// Run with Node's test runner and Playwright on the module path; see README.md.
// X (@iamforu_): "antigravity-bridge在路由页面不显示额度". The plugin's usage
// read failed for the account, so its Usage card held an error and no
// windows, and Routing said "what's left not known yet" with no word of why.
// A row whose card says its read failed now says the allowance is
// unavailable, with the card's reason on hover, in the accounts list and on
// the stage; an account with a card read keeps its windows, and one with no
// card at all stays unknown. Every language, 1100 and 420px, both engines.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const iso = (ms) => new Date(ms).toISOString();
const why = "agy usage: timed out after 18s";
const accts = [
  { id: "antigravity-bridge", provider: "antigravity-bridge", name: "antigravity-bridge", who: "saasortsfed", kind: "account", agent: "antigravity-bridge", model: "gemini-3.8-flash", known: false },
  { id: "antigravity-bridge#profile-aa", provider: "antigravity-bridge", name: "antigravity-bridge", who: "minhaco", kind: "account", agent: "antigravity-bridge", model: "gemini-3.8-flash", known: false, shared: true },
  { id: "antigravity-bridge#profile-bb", provider: "antigravity-bridge", name: "antigravity-bridge", who: "nocard", kind: "account", agent: "antigravity-bridge", model: "gemini-3.8-flash", known: false },
];
const route = {
  id: 7, seq: 7, time: iso(now - 2000), agent: "claude", model: "gemini-flash-latest", provider: "group/gemini-flash-latest", done: true, status: 200, ms: 900, tokens: 1500,
  order: accts, tries: [{ id: "antigravity-bridge", model: "gemini-3.8-flash", start: iso(now - 2000), done: true, ms: 800, status: 200 }],
};
const cards = [
  { provider: "antigravity-bridge", name: "antigravity-bridge", kind: "subscription", user: "saasortsfed", windows: [
    { name: "Gemini · 5 hours", used: 40, aside: true, resetsAt: iso(now + 130 * 60e3) },
    { name: "Gemini · 7 days", used: 12, aside: true },
  ] },
  { provider: "antigravity-bridge", name: "antigravity-bridge", kind: "subscription", user: "minhaco", windows: [], error: why },
];

function serve(lang, asks) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light", quotaLeft: false } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 7, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: [route] });
    }
    if (url.pathname === "/api/usage/quotas") { asks.push(url.search); return json(cards); }
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}


const words = {
  en: ["Allowance unavailable — hover for why", "what's left not known yet"],
  zh: ["暂时无法获取额度 — 悬停查看原因", "剩余额度未知"],
  "zh-TW": ["暫時無法取得額度 — 懸停查看原因", "剩餘額度未知"],
  ja: ["利用枠を取得できません — ホバーで理由を表示", "残りはまだ不明"],
  de: ["Kontingent nicht verfügbar — für den Grund darüberfahren", "Rest noch unbekannt"],
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a plugin account whose usage read failed says why, not unknown`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(words)) {
      for (const width of [1100, 420]) {
        await t.test(`${lang} at ${width}px`, async () => {
          const ctx = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
          try {
            const page = await ctx.newPage();
            page.setDefaultTimeout(5000);
            page.setDefaultNavigationTimeout(60000);
            const errors = [], asks = [];
            page.on("pageerror", (e) => errors.push(e.message));
            await page.route("**/*", serve(lang, asks));
            await page.goto("http://magpie.test/?view=routing");
            const [bad, unknown] = words[lang];
            // the accounts list: the failed read says so, its reason on hover
            const act = page.locator(".rt-act", { hasText: "minhaco" }).first();
            await act.waitFor();
            await page.waitForFunction((bad) => [...document.querySelectorAll(".rt-act")].some((e) => e.textContent.includes("minhaco") && e.textContent.includes(bad)), bad);
            const st = act.locator(".st");
            assert.equal((await st.textContent()).trim(), bad);
            assert.equal(await st.getAttribute("title"), why);
            // a read card keeps its windows; no card at all stays unknown
            const ok = page.locator(".rt-act", { hasText: "saasortsfed" }).first();
            assert.ok((await ok.textContent()).includes("40%"), "the read card's windows");
            const none = page.locator(".rt-act", { hasText: "nocard" }).first();
            assert.ok(!(await none.textContent()).includes(bad), "no card is not a failed read");
            if (unknown) assert.ok((await none.textContent()).includes(unknown), "no card stays unknown");
            // the stage: the row says it too, the reason in its tooltip
            const row = page.locator(".rt-stage li", { hasText: "minhaco" }).first();
            await row.waitFor();
            assert.ok((await row.textContent()).includes(bad), "stage: " + (await row.textContent()));
            assert.ok((await row.getAttribute("title")).split("\n").includes(why));
            const over = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
            assert.ok(over <= 1, `${lang} at ${width}: ${over}px past`);
            assert.deepEqual(errors, []);
          } finally {
            await ctx.close();
          }
        });
      }
    }
  });
}
