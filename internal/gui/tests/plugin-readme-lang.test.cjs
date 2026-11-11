// Run with Node's test runner and Playwright on the module path; see README.md.
// ahwulv on X: a plugin that ships a Chinese README still showed the English
// one on its details page with the UI in Chinese. The page asks for the UI's
// language (?lang=), and a README read in one language is not shown again
// in another. In Chromium and WebKit; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const listings = [
  { package: "@magpie-community/opencode-zed-auth", name: "Zed", icon: "zed", providers: ["zed"], community: true,
    summary: { en: "Claude, GPT, Gemini and Grok hosted by Zed, on your Zed plan.", zh: "用 Zed 订阅使用 Zed 托管的 Claude、GPT、Gemini 和 Grok。" },
    npm: { version: "0.1.0", weekly: 120, license: "MIT", repository: "https://github.com/magpie-community/plugins" } },
  { package: "@magpie-community/opencode-copilot-auth", name: "GitHub Copilot", icon: "githubcopilot", providers: ["github-copilot"], community: true,
    summary: { en: "Your GitHub Copilot plan.", zh: "你的 GitHub Copilot 套餐。" },
    npm: { version: "0.0.9", weekly: 48213, license: "MIT", repository: "https://github.com/magpie-community/plugins" } },
];

function server(lang, asked) {
  const installed = [];
  const subs = [];
  const market = () => ({ listings, state: { bun: true, bunVersion: "1.3.0", plugins: installed } });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    const body = () => route.request().postDataJSON();
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") {
      return json({ providers: [{ id: "openai", name: "OpenAI", icon: "openai", preset: "openai", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }],
        presets: [], excluded: [], gateway: { running: true, window: true }, plugins: subs });
    }
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/plugins/market" || url.pathname === "/api/plugins" || url.pathname === "/api/plugins/listings") { const m = market(); return json(url.pathname === "/api/plugins" ? m.state : url.pathname === "/api/plugins/listings" ? { listings: m.listings } : m); }
    if (url.pathname === "/api/plugins/page") { asked.push(url.searchParams.get("lang")); return json({ readme: url.searchParams.get("lang") === "zh-CN" ? "# 中文说明" : "# English readme", updated: "2026-09-01T00:00:00Z" }); }
    if (url.pathname === "/api/plugins/add") {
      const b = body();
      asked.push(["add", b]);
      installed.push({ spec: b.spec, providers: ["GitHub Copilot"], version: "0.0.9" });
      subs.push({ id: "github-copilot-plugin", pid: "github-copilot", name: "GitHub Copilot", icon: "githubcopilot", spec: b.spec, signedIn: false, models: 34,
        methods: [{ type: "oauth", label: "Login with GitHub" }] });
      return json({});
    }
    if (url.pathname === "/api/plugin-signin") { asked.push(["signin", body()]); return json({ id: "p1", agent: "github-copilot-plugin", state: "waiting", url: "https://github.test/login/device" }); }
    if (url.pathname === "/api/signin/p1") return json({ id: "p1", agent: "github-copilot-plugin", state: "waiting", url: "https://github.test/login/device" });
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}


for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a plugin's page asks for the README in the UI's language", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const [lang, tag, want] of [["en", "en", "English readme"], ["zh", "zh-CN", "中文说明"]]) {
      await t.test(lang, async () => {
        const ctx = await browser.newContext({ viewport: { width: 980, height: 820 }, reducedMotion: "reduce" });
        t.after(() => ctx.close());
        const page = await ctx.newPage();
        page.setDefaultTimeout(15000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], asked = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, asked));
        await page.goto("http://magpie.test/?view=plugins");
        const zed = page.locator('#view-plugins .pm-card[data-pkg="@magpie-community/opencode-zed-auth"]');
        await zed.locator(".pm-sum").click();
        await page.locator("#modal .pm-detail .pm-md").waitFor();
        assert.match(await page.locator("#modal .pm-detail .pm-readme").innerText(), new RegExp(want));
        assert.deepEqual(asked, [tag]);
        assert.deepEqual(errors, []);
      });
    }
  });
}
