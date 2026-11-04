// Run with Node's test runner and Playwright on the module path; see README.md.
// A Claude joined over the network (a Remote magpie provider) shows its
// windows in Routing's Accounts and keys like a local account does, as the
// computer that holds the account read them: the Usage page's cards with
// `from`. Claude's row is told Claude's cards, not Codex's; a remote whose
// read failed says the allowance is unavailable, never 0. In every
// language, at a window's width and at 420px, in both engines.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const iso = (ms) => new Date(ms).toISOString();
const remote = (id, name, model) => ({ id: id + "-k", provider: id, name, preset: "remote-magpie", icon: "magpie", kind: "provider", model, known: false });
const accts = [remote("home", "Home", "claude-opus-5"), remote("den", "Den", "claude-opus-5")];
const route = {
  id: 7, seq: 7, time: iso(now - 2000), agent: "claude", model: "claude-opus-5", provider: "home", done: true, status: 200, ms: 900, tokens: 1500,
  order: accts, tries: [{ id: "home-k", model: "claude-opus-5", start: iso(now - 2000), done: true, ms: 800, status: 200 }],
};
const cards = [
  { provider: "home/claude", name: "Claude · Home", kind: "subscription", user: "work@example.com", from: "Home", windows: [
    { name: "5 hours", used: 40, resetsAt: iso(now + 130 * 60e3) },
    { name: "7 days", used: 12, resetsAt: iso(now + 73 * 3600e3) },
  ] },
  { provider: "home/codex", name: "Codex · Home", kind: "subscription", user: "c@example.com", from: "Home", windows: [{ name: "5 hours", used: 77 }] },
  { provider: "den", name: "Den", icon: "magpie", kind: "subscription", from: "Den", windows: [], error: "couldn't reach the remote magpie: connection refused" },
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
  en: { wins: ["5 hours 40% used · resets in 2h", "7 days 12% used · resets in 3d"], unknown: "Allowance unavailable — hover for why" },
  zh: { wins: ["5 小时 已用 40% · 2 小时后重置", "7 天 已用 12% · 3 天后重置"], unknown: "暂时无法获取额度 — 悬停查看原因" },
  "zh-TW": { wins: ["5 小時 已用 40% · 2 小時後重置", "7 天 已用 12% · 3 天后重置"], unknown: "暫時無法取得額度 — 懸停查看原因" },
  ja: { wins: ["5 時間 使用済み 40% · 2 時間後にリセット", "7 日 使用済み 12% · 3 日後にリセット"], unknown: "利用枠を取得できません — ホバーで理由を表示" },
  de: { wins: ["5 Stunden 40% verbraucht · Reset in 2 Std.", "7 Tage 12% verbraucht · Reset in 3 T."], unknown: "Kontingent nicht verfügbar — für den Grund darüberfahren" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a Claude joined over the network shows its windows in Accounts and keys`, async (t) => {
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
                    await page.locator(".rt-act .wins span").first().waitFor();
            const rows = page.locator(".rt-act");
            const denRow = rows.nth(0), homeRow = rows.nth(1); // listed by name
            // Claude's windows only: Codex's 77% on the same remote is not on Claude's row
            assert.deepEqual(await homeRow.locator(".wins span").allTextContents(), words[lang].wins);
            // a failed read is unknown, with the reason on hover, never a figure
            assert.deepEqual(await denRow.locator(".wins span").allTextContents(), [words[lang].unknown]);
            assert.match(await denRow.locator(".wins span").getAttribute("title"), /connection refused/);
            const over = await homeRow.evaluate((e) => Math.max(e.scrollWidth - e.clientWidth, document.documentElement.scrollWidth - innerWidth));
            assert.ok(over <= 1, `${lang} at ${width}: ${over}px past`);
            assert.ok(asks.every((s) => !s.includes("asked")), "never an asked read: " + asks.join(", "));
            assert.deepEqual(errors, []);
          } finally {
            await ctx.close();
          }
        });
      }
    }
  });
}
