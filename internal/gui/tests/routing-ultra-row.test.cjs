// Run with Node's test runner and Playwright on the module path; see README.md.
// The Routing page's diagram tags an account a group sends in its
// vendor's fast mode: "fast", and "Ultrafast" where the group sends it
// Ultrafast and the account offers it (the trace's Weighed.Ultra).
// paul-tian's #7095: a Codex account sent ultrafast through a group was
// tagged Fast in the diagram while its request row said Ultrafast.
// Chromium and WebKit, in every language, the API faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const seat = (agent, ultra) => ({ id: `codex/${agent}`, provider: "codex", name: "Codex", who: `${agent}@example.com`, kind: "account", agent: "codex", model: "gpt-6.2-astra", routing: "order", fast: true, ...(ultra ? { ultra: true } : {}) });
// one agent's request sent Ultrafast, another's fast alone
const routes = [["codex", true], ["opencode", false]].map(([agent, ultra], i) => ({
  id: 100 - i, seq: 100 - i, time: new Date(now.getTime() - 1000).toISOString(), agent, model: "group/astra", provider: "codex", tier: ultra ? "ultrafast" : "priority",
  order: [seat(agent, ultra)], done: true, status: 200, ms: 500, ttft: 200, tokens: 100,
  tries: [{ id: `codex/${agent}`, model: "gpt-6.2-astra", start: new Date(now.getTime() - 1000).toISOString(), done: true, status: 200, ms: 500, fast: true, tier: ultra ? "ultrafast" : "priority" }],
}));

const W = {
  en: { fast: "fast", ultra: "Ultrafast", g: "Sent in its vendor's fast mode, as the group says", u: "Sent Ultrafast where the account offers it, as the group says" },
  zh: { fast: "快速", ultra: "极速", g: "按分组设置，以服务商的快速模式发送", u: "按分组设置，账号支持极速就以极速发送" },
  "zh-TW": { fast: "快速", ultra: "極速", g: "按分組設定，以服務商的快速模式傳送", u: "按分組設定，帳號支援極速就以極速傳送" },
  ja: { fast: "高速", ultra: "超高速", g: "グループの設定どおり、ベンダーの高速モードで送ります", u: "グループの設定どおり、アカウントが対応していれば超高速で送ります" },
  de: { fast: "schnell", ultra: "Ultrafast", g: "Im Schnellmodus des Anbieters gesendet, gemäß Gruppe", u: "Ultrafast gesendet, wo das Konto es bietet, gemäß Gruppe" },
};

function serve(lang) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }, { id: "opencode", name: "OpenCode", path: "/test/opencode.json", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 100, totals: { requests: routes.length, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(W)) {
    test(`${engine} ${lang}: the diagram tags an account sent Ultrafast as Ultrafast`, async (t) => {
      const w = W[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 900 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=routing");
      await page.locator("li > b small.fb").nth(1).waitFor();
      const tags = (await page.locator("li > b").evaluateAll((bs) => bs.map((b) => {
        const f = b.querySelector("small.fb");
        return f && { who: b.textContent, text: f.textContent.trim(), title: f.title };
      }))).filter(Boolean);
      const of = (agent) => tags.find((x) => x.who.includes(`${agent}@example.com`));
      assert.deepEqual(of("codex") && [of("codex").text, of("codex").title], [w.ultra, w.u], JSON.stringify(tags));
      assert.deepEqual(of("opencode") && [of("opencode").text, of("opencode").title], [w.fast, w.g], JSON.stringify(tags));
      assert.deepEqual(errors, []);
    });
  }
}
