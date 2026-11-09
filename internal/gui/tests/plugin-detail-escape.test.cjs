// Run with Node's test runner and Playwright on the module path; see README.md.
// #3779 (sevtdy): on Plugins › Discover, Escape left a plugin's details
// dialog open and only showed Install's focus ring. The dialog borrows
// #modal outside Providers without being confirmAsk, so Escape reached only
// the Providers page's handler, which closes nothing there. It now closes
// on Escape (and from Close), and its Sign in, which leaves for Providers
// through show(), leaves no confirmAsk behind to swallow the next Escape,
// which closes the add sheet as it always has.
// In Chromium and WebKit, en and zh; the API is faked here.
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
    if (url.pathname === "/api/plugins/page") return json({ readme: "# " + url.searchParams.get("name") + "\n\nHow to use it.", updated: "2026-09-01T00:00:00Z" });
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

const L = { en: { install: "Install", signIn: "Sign in", close: "Close" }, zh: { install: "安装", signIn: "登录", close: "关闭" } };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Escape closes a plugin's details dialog", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = L[lang];
        const ctx = await browser.newContext({ viewport: { width: 980, height: 820 }, reducedMotion: "reduce" });
        t.after(() => ctx.close());
        const page = await ctx.newPage();
        page.setDefaultTimeout(15000); // a busy machine takes seconds over a click
        page.setDefaultNavigationTimeout(60000);
        const errors = [], asked = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, asked));
        await page.goto("http://magpie.test/?view=plugins");
        const view = page.locator("#view-plugins");
        const modal = page.locator("#modal");
        const dlg = page.locator("#modal .pm-detail");
        const gone = () => page.waitForFunction(() => document.querySelector("#modal").hidden && !document.querySelector("#modal .pm-detail"));

        // the reporter's case: a card's details, then Escape
        const zed = view.locator('.pm-card[data-pkg="@magpie-community/opencode-zed-auth"]');
        await zed.locator(".pm-sum").click();
        await dlg.locator(".pm-md").waitFor();
        await page.keyboard.press("Escape");
        await gone();
        assert.equal(await modal.isHidden(), true);
        // and again: nothing was left behind by the first
        await zed.locator(".pm-sum").click();
        await dlg.locator(".pm-md").waitFor();
        await page.keyboard.press("Escape");
        await gone();
        // Close still closes it, and Escape after it is the page's again
        await zed.locator(".pm-sum").click();
        await dlg.locator(".pm-md").waitFor();
        await dlg.locator(".bar button", { hasText: new RegExp("^" + w.close + "$") }).click();
        await gone();

        // Install, then Sign in from the dialog: Providers' add sheet with
        // the sign-in, and Escape there closes the sheet as it always has
        const cp = view.locator('.pm-card[data-pkg="@magpie-community/opencode-copilot-auth"]');
        await cp.locator(".pm-sum").click();
        await dlg.locator(".pm-md").waitFor();
        await dlg.locator(".pm-act", { hasText: w.install }).click();
        await dlg.locator(".pm-act.go", { hasText: w.signIn }).click();
        await page.locator("#view-providers").waitFor({ state: "visible" });
        await page.locator("#addSheet .signing").waitFor();
        await page.waitForFunction(() => document.querySelector("#modal").hidden);
        assert.deepEqual(asked.map(([k]) => k), ["add", "signin"]);
        await page.evaluate(() => document.activeElement?.blur());
        await page.keyboard.press("Escape");
        await page.locator("#addSheet").waitFor({ state: "hidden" });
        await page.waitForFunction(() => !document.querySelector("#addSheet .signing"));
        assert.deepEqual(errors, []);
      });
    }
  });
}
