// Run with Node's test runner and Playwright on the module path; see README.md.
// A market of Claude plugins, as the Pi packages tab has Pi's: under the
// Claude plugins tab, Discover lists Claude Code's official marketplace
// (anthropics/claude-plugins-official), under the empty state too; Install
// sends the library's own plugin install with the marketplace and the
// plugin's name, for every agent shown; one in the library shows
// Installed; the search narrows the installed plugins as well as the
// market's. In every language, both engines, at a phone's width too. No
// backend: the API is faked here, its cards are entries of the real
// marketplace.json as the market reads them.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/ada";
const MARKET = "anthropics/claude-plugins-official";
const lib = (plugins) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [{ id: "claude", name: "Claude Code", icon: "", skills: `${HOME}/.claude/skills` }],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], servers: [],
  plugins,
});
// as MarketClaudePlugins answers for them
const entry = (name, description, author, category, repo, extra) => ({
  name, description, author, category, marketplace: "claude-plugins-official", source: MARKET, repo,
  icon: "https://github.com/" + repo.split("/")[3] + ".png?size=96", ...extra,
});
const listed = [
  entry("agent-sdk-dev", "Development kit for working with the Claude Agent SDK", "Anthropic", "development", "https://github.com/anthropics/claude-plugins-official/tree/main/plugins/agent-sdk-dev"),
  entry("figma", "Figma design platform integration. Access design files, extract component information, read design tokens, and translate designs into code. Bridge the gap between design and development workflows.", "", "design", "https://github.com/figma/mcp-server-guide"),
  entry("airwallex-agentos", "Airwallex's AgentOS", "Airwallex", "productivity", "https://github.com/airwallex/agentos", { title: "Airwallex" }),
];
const installed = (name) => ({ name, description: listed.find((x) => x.name === name).description, skills: ["one"], mcp: [], hooks: [], subagents: [], extensions: [], agents: ["claude"] });

function server(lang, calls) {
  const plugins = [];
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib(plugins) });
    if (url.pathname === "/api/library/market/claude") {
      const q = (url.searchParams.get("q") || "").toLowerCase();
      calls.push(["market", q]);
      return route.fulfill({ json: { items: listed.filter((x) => (x.name + " " + x.description + " " + x.category).toLowerCase().includes(q)) } });
    }
    if (url.pathname === "/api/library/plugins/install") {
      const b = req.postDataJSON();
      calls.push(["install", b]);
      plugins.push(...b.names.map(installed));
      return route.fulfill({ json: { ...lib(plugins), result: { changed: b.agents } } });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/library/icon")) return route.fulfill({ status: 404, body: "" });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: {
    tab: "Claude plugins", none: "No Claude plugins in the library yet", note: "Claude Code's official marketplace",
    find: "Search Claude plugins — GitHub, Figma, testing…", install: "Install", installed: "Installed", done: "1 plugin installed",
    nothing: "Nothing matches “zzz”.",
  },
  zh: {
    tab: "Claude 插件", none: "资源库里还没有 Claude 插件", note: "Claude Code 官方市场",
    find: "在 Claude Code 官方市场搜索插件 — GitHub、Figma、测试…", install: "安装", installed: "已安装", done: "已安装 1 个插件",
    nothing: "没有与“zzz”匹配的结果。",
  },
  "zh-TW": {
    tab: "Claude 外掛", none: "資源庫裡還沒有 Claude 外掛", note: "Claude Code 官方市場",
    find: "在 Claude Code 官方市場搜尋外掛 — GitHub、Figma、測試…", install: "安裝", installed: "已安裝", done: "已安裝 1 個外掛",
    nothing: "沒有與「zzz」匹配的結果。",
  },
  ja: {
    tab: "Claude プラグイン", none: "ライブラリにはまだ Claude プラグインがありません", note: "Claude Code 公式マーケットプレイス",
    find: "Claude Code 公式マーケットプレイスでプラグインを検索 — GitHub、Figma、テスト…", install: "インストール", installed: "インストール済み", done: "プラグインを 1 件インストールしました",
    nothing: "「zzz」に一致するものはありません。",
  },
  de: {
    tab: "Claude-Plugins", none: "Noch keine Claude-Plugins in der Bibliothek", note: "Offizieller Marktplatz von Claude Code",
    find: "Claude-Plugins suchen – GitHub, Figma, Tests…", install: "Installieren", installed: "Installiert", done: "1 Plugin installiert",
    nothing: "Nichts passt zu „zzz“.",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Claude plugins market lists the official marketplace, installs from it and searches the installed ones too", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of Object.keys(words)) for (const width of [980, 380]) {
      await t.test(`${lang} at ${width}px`, async () => {
        const w = words[lang], calls = [];
        const ctx = await browser.newContext({ viewport: { width, height: 900 } });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, calls));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        await v.locator(".lib-tabs").getByText(w.tab, { exact: true }).click();

        // nothing installed: the empty state, and the market under it
        await v.getByText(w.none, { exact: true }).waitFor();
        const mk = v.locator('.mk[data-market="claude"]');
        const cardOf = (name) => mk.locator(`.mk-card[data-id="${name}"]`);
        await cardOf("figma").waitFor();
        assert.equal(await mk.locator(".mk-note").textContent(), w.note);
        assert.equal(await mk.locator('input[data-lib="market-claude"]').getAttribute("placeholder"), w.find);
        // the name it shows, its author and category
        assert.deepEqual(await mk.locator(".mk-title").allTextContents(), ["agent-sdk-dev", "figma", "Airwallex"]);
        assert.deepEqual(await cardOf("agent-sdk-dev").locator(".mk-meta span").allTextContents(), ["Anthropic", "development"]);
        assert.equal(await mk.locator(".mk-have").count(), 0);

        // Install: the library's plugin install, from the marketplace
        for (const name of ["figma", "agent-sdk-dev"]) {
          assert.equal(await cardOf(name).locator(".mk-add").textContent(), w.install);
          await cardOf(name).locator(".mk-add").click();
          await cardOf(name).locator(".mk-have").waitFor();
          assert.equal(await cardOf(name).locator(".mk-have").textContent(), w.installed);
          assert.equal(await page.locator("#status").textContent(), w.done);
        }
        const rows = v.locator(".lib-plugins > .lib-plugin");
        await rows.nth(1).waitFor();
        assert.equal(await v.getByText(w.none, { exact: true }).count(), 0);
        assert.equal(await cardOf("airwallex-agentos").locator(".mk-add").count(), 1);

        // the search narrows the installed ones and the market's alike
        const find = mk.locator('input[data-lib="market-claude"]');
        await find.fill("fig");
        await page.waitForFunction(() => document.querySelectorAll('.mk[data-market="claude"] .mk-card').length === 1);
        assert.deepEqual(await mk.locator(".mk-title").allTextContents(), ["figma"]);
        assert.deepEqual(await rows.evaluateAll((rs) => rs.filter((r) => !r.hidden).map((r) => r.querySelector(".name").textContent)), ["figma"]);
        await find.fill("zzz");
        await mk.locator(".mk-msg").waitFor();
        assert.equal(await mk.locator(".mk-msg").textContent(), w.nothing);
        assert.equal(await v.locator(".lib-plugins").isHidden(), true);
        await find.fill("");
        await page.waitForFunction(() => document.querySelectorAll('.mk[data-market="claude"] .mk-card').length === 3);
        assert.equal(await rows.evaluateAll((rs) => rs.filter((r) => !r.hidden).length), 2);

        assert.deepEqual(calls.filter((c) => c[0] === "install"), [
          ["install", { source: MARKET, names: ["figma"], agents: ["claude"] }],
          ["install", { source: MARKET, names: ["agent-sdk-dev"], agents: ["claude"] }],
        ]);
        // nothing runs off the side at a phone's width
        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert(over <= 0, `the page scrolls sideways by ${over}px`);
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
