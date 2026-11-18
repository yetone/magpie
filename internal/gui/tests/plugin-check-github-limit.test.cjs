// Run with Node's test runner and Playwright on the module path; see README.md.
// bingqilinweimaotai (#1942): Check for updates with a plugin from GitHub
// that GitHub's rate limit kept from being checked. The summary no longer
// says every plugin is up to date (only the ones checked are known to be),
// and the row and the summary say it is GitHub's rate limit, with when it
// lifts and how a token in Settings raises it. English, Chinese, Japanese
// and German; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const NPM = "opencode-filler-auth", GIT = "github:me/opencode-x-auth";

function server(lang, limited) {
  const installed = [
    { spec: NPM, providers: ["F"], version: "1.0.0", latest: "1.0.0", moved: [] },
    { spec: GIT, package: "opencode-x-auth", providers: ["X"], version: "1.0.0", commit: "abc1234", moved: [] },
  ];
  const state = () => ({ bun: true, bunVersion: "1.3.0", plugins: installed, movable: [] });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugins/updates") return json({ checked: "2026-10-01T00:00:00Z", waiting: [], updated: [] });
    if (url.pathname === "/api/plugins/npm") return json({ npm: {} });
    if (url.pathname === "/api/plugins") return json(state());
    if (url.pathname === "/api/plugins/listings") return json({ listings: [] });
    if (url.pathname === "/api/plugins/check") {
      return json({ at: "2026-10-02T09:30:00Z", state: state(), plugins: [
        { spec: NPM, package: NPM, version: "1.0.0", latest: "1.0.0", status: "current", auto: false, off: false },
        { spec: GIT, package: "opencode-x-auth", version: "1.0.0", commit: "abc1234", status: "unknown", why: "limited",
          error: "GitHub's rate limit for requests without a token (60 an hour) is used up", limited, auto: false, off: false },
      ] });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { installed: "Installed", check: "Check for updates", head: "No updates for the plugins checked · 1 couldn't be checked: ", none: /^Couldn't ask github:me\/opencode-x-auth for its newest commit: GitHub allows 60 requests an hour without a token, used up until \S+.* Add a GitHub token in Settings › Network and sharing/,
    token: /GitHub's rate limit for your GitHub token is used up for now$/, every: "Every plugin is up to date" },
  zh: { installed: "已安装", check: "检查更新", head: "已检查的插件都没有更新 · 1 个无法检查：", none: /^没能向 github:me\/opencode-x-auth 查询最新提交：GitHub 不带令牌每小时仅允许 60 次请求，已用完，\S+ 恢复。在 设置 › 网络与共享 添加 GitHub 令牌/,
    token: /你的 GitHub 令牌的请求额度暂时用完了$/, every: "所有插件都是最新的" },
  ja: { installed: "インストール済み", check: null, head: "確認できたプラグインに更新はありません · 1 個を確認できませんでした：", none: /^github:me\/opencode-x-auth の最新コミットを確認できませんでした：GitHub はトークンなしでは/,
    token: /GitHub トークンのレート制限は現在使い切られています$/, every: "すべてのプラグインが最新です" },
  de: { installed: "Installiert", check: null, head: "Keine Updates für die geprüften Erweiterungen · 1 konnten nicht geprüft werden: ", none: /^github:me\/opencode-x-auth konnte nicht nach dem neuesten Commit gefragt werden: GitHub erlaubt ohne Token 60 Anfragen/,
    token: /Das Rate-Limit von GitHub für Ihren GitHub-Token ist vorerst aufgebraucht$/, every: "Alle Erweiterungen sind aktuell" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a plugin GitHub's rate limit kept from being checked", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) {
      for (const width of [980, 420]) {
        await t.test(lang + " " + width, async () => {
          const w = L[lang];
          for (const [limited, want] of [[{ until: "2030-01-02T03:04:00Z" }, w.none], [{ token: true }, w.token]]) {
            const page = await (await browser.newContext({ viewport: { width, height: 640 } })).newPage();
            page.setDefaultTimeout(5000);
            page.setDefaultNavigationTimeout(60000);
            const errors = [];
            page.on("pageerror", (e) => errors.push(e.message));
            await page.route("**/*", server(lang, limited));
            await page.goto("http://magpie.test/?view=plugins");
            const view = page.locator("#view-plugins");
            // the Installed tab, by its name: Built-in (P0) came before it
            const tabs = page.locator(":is(.lib-tabs .opt, .nav-s):visible");
            await tabs.filter({ hasText: w.installed }).first().click();
            const row = view.locator(".pm-row").filter({ has: page.locator(".name span", { hasText: /^opencode-x-auth$/ }) });
            await row.waitFor();
            await view.locator(".pm-foot .pm-check").evaluate((b) => b.click());
            const warn = row.locator(".pm-chip.warn");
            await warn.waitFor();
            assert.match(await warn.getAttribute("title"), want);
            const said = await page.locator("#status").textContent();
            assert(said.startsWith(w.head), "the summary: " + said);
            assert.match(said.slice(w.head.length), want);
            assert(!said.startsWith(w.every), "the summary says every plugin is up to date: " + said);
            assert.deepEqual(errors, []);
            await page.context().close();
          }
        });
      }
    }
  });
}
