// Run with Node's test runner and Playwright on the module path; see README.md.
// lantian_x on X: of the Library's tabs they use a few, and the rest sit
// there as clutter, with only the whole Library to switch off. Plugins ›
// Built-in › Library's row has Sections now, one link (no switch per tab on
// the list): its menu ticks each tab the page shows, and unticking one
// hides it from the page's tabs and the sidebar's list at once, saved as
// the plugin's hide setting. The link says how many are hidden. The last
// tab shown can't be hidden. A hidden tab the reader had open gives way to
// the first one shown. Hiding is the page's alone: nothing but the
// setting is posted, so agents are given what the tabs hold as before.
// Every language, both engines, 1100, 800 and 420 points. No backend: the
// API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/tester";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const view = {
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code")],
  instructions: { agents: [], sets: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], skills: [], problems: [],
  hooks: [{ name: "lint", event: "turn-end", command: "echo", agents: ["claude"] }],
};

// as plugins/library's manifest says it, and pluginrt.SchemaOf its settings
const SCHEMA = {
  type: "object",
  properties: { hide: { type: "string", title: "Hidden sections", description: "The Library's tabs left out of its page; agents are given what they hold as before", default: "" } },
  "x-order": ["hide"],
};
const TABS = ["instructions", "memory", "mcp", "skills", "hooks", "env", "subagents", "commands", "rules", "plugins", "rtk", "bili"];

const words = {
  en: { sections: "Sections", hidden2: "Sections · 2 hidden", hooks: "Hooks", env: "Environment variables", instructions: "Instructions", last: "One section always shows" },
  zh: { sections: "分区", hidden2: "分区 · 已隐藏 2 个", hooks: "Hooks", env: "环境变量", instructions: "指令", last: "至少保留一个分区" },
  "zh-TW": { sections: "分區", hidden2: "分區 · 已隱藏 2 個", hooks: "Hooks", env: "環境變數", instructions: "指令", last: "至少保留一個分區" },
  ja: { sections: "セクション", hidden2: "セクション · 2 個を非表示", hooks: "フック", env: "環境変数", instructions: "指示", last: "少なくとも 1 つのセクションは表示されます" },
  de: { sections: "Bereiche", hidden2: "Bereiche · 2 ausgeblendet", hooks: "Hooks", env: "Umgebungsvariablen", instructions: "Anweisungen", last: "Ein Bereich bleibt immer sichtbar" },
};

function serve(lang, config, posts) {
  const list = () => [{ id: "library", name: "Library", sub: "Instructions, MCP servers, skills and hooks, kept once and given to every agent", on: true, chosen: true, source: "preset", state: "active", inUse: 0, page: "library", schema: SCHEMA, config: { ...config } }];
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    // what the page asks to change: the built-ins and the library (what agents are given)
    if (req.method() === "POST" && /^\/api\/(plugins\/builtin|library)/.test(url.pathname)) posts.push([url.pathname, req.postDataJSON()]);
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",featuresOff:[],pluginConfig:{library:${JSON.stringify(config)}}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", builtins: list() } });
    if (url.pathname === "/api/settings") return json({ lang, theme: "light", builtins: list() });
    if (url.pathname === "/api/plugins/builtin") {
      if (req.method() === "POST") Object.assign(config, req.postDataJSON().config);
      return json(list());
    }
    if (url.pathname === "/api/plugins") return json({ bun: true, bunVersion: "1.4.2", plugins: [], picker: false, movable: [] });
    if (url.pathname === "/api/plugins/listings") return json({ listings: [] });
    if (url.pathname === "/api/plugins/github") return json({ repos: [] });
    if (url.pathname === "/api/library") return json(view);
    if (url.pathname === "/api/library/rtk") return json({ path: "", agents: [] });
    if (url.pathname === "/api/library/market/servers" || url.pathname === "/api/library/market/skills") return json({ items: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

const sideTabs = (page) => page.evaluate(() => [...document.querySelectorAll(".nav-sub:not(.leaving) .nav-s")].map((b) => b.textContent.trim()));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: the Library's sections are hidden one by one from Plugins › Built-in`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const width of [1100, 800, 420]) {
        const where = `${width}px`;
        const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
        // the reader had Hooks open last
        await context.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "hooks"); } catch {} });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const config = { hide: "" }, posts = [];
        await page.route("**/*", serve(lang, config, posts));
        await page.goto("http://magpie.test/?view=plugins");
        const builtinTab = () => page.locator(width >= 1024 ? ".nav-sub:not(.leaving) .nav-s" : "#view-plugins .lib-tabs .opt").first().click();
        await builtinTab();
        const row = page.locator('#builtinList .feat-row[data-id="library"]');
        await row.waitFor();

        // one link on the row, not a switch per tab
        const link = row.locator(".lib-sections");
        assert.equal(await link.textContent(), w.sections, where);
        assert.equal(await page.locator("#builtinList .feat-set").count(), 0, `${where}: a form under the row`);
        assert.equal(await row.locator('[role="switch"]').count(), 1, `${where}: switches on the row`);

        // its menu: every tab, ticked
        await link.click();
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        const items = menu.locator('[role="menuitemcheckbox"]');
        assert.equal(await items.count(), TABS.length, where);
        assert.equal(await menu.locator('[aria-checked="true"]').count(), TABS.length, `${where}: all ticked`);
        const box = await menu.boundingBox();
        assert.ok(box.x >= 0 && box.x + box.width <= width + 0.5, `${where}: the menu runs out of the window`);

        // Hooks and Environment variables unticked: saved at once, the menu stays
        await Promise.all([page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === "/api/plugins/builtin"), items.filter({ hasText: w.hooks }).first().click()]);
        await Promise.all([page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === "/api/plugins/builtin"), items.filter({ hasText: w.env }).first().click()]);
        assert.deepEqual(posts.map(([p, b]) => [p, b.config]), [["/api/plugins/builtin", { hide: "hooks" }], ["/api/plugins/builtin", { hide: "hooks,env" }]], where);
        assert.ok(await menu.isVisible(), `${where}: the menu closed`);
        await page.waitForFunction((s) => document.querySelector('#builtinList .feat-row[data-id="library"] .lib-sections')?.textContent === s, w.hidden2);
        await page.keyboard.press("Escape");
        await menu.waitFor({ state: "detached" });

        // the Library page: Hooks and Environment variables gone from its
        // tabs and the sidebar's list, and Hooks, open last, gives way to
        // the first tab
        await page.evaluate(() => document.querySelector('#nav button[data-view="library"]').click());
        // (the tabs are drawn at every width; from 1024 points the sidebar shows them instead)
        await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-tabs .opt").length === 10);
        const ids = await page.evaluate(() => [...document.querySelectorAll("#view-library .lib-tabs .opt")].map((b) => b.dataset.seg));
        assert.deepEqual(ids, TABS.filter((x) => x !== "hooks" && x !== "env"), where);
        assert.equal(await page.locator("#view-library .lib-tabs .opt.on").getAttribute("data-seg"), "instructions", where);
        if (width >= 1024) {
          await page.waitForFunction(() => document.querySelectorAll(".nav-sub:not(.leaving) .nav-s").length === 10);
          const side = await sideTabs(page);
          assert.ok(!side.some((n) => n.startsWith(w.hooks) || n.startsWith(w.env)), `${where}: the sidebar lists a hidden tab: ${side}`);
        }
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollWidth - innerWidth <= 1), true, `${where}: the page scrolls sideways`);

        // back on the row: the last tab shown can't be hidden
        config.hide = TABS.filter((x) => x !== "instructions").join(",");
        await page.goto("http://magpie.test/?view=plugins");
        await builtinTab();
        await page.locator('#builtinList .feat-row[data-id="library"] .lib-sections').click();
        await menu.waitFor();
        assert.equal(await menu.locator('[aria-checked="true"]').count(), 1, `${where}: one ticked`);
        const before = posts.length;
        await menu.locator('[role="menuitemcheckbox"]').filter({ hasText: w.instructions }).first().click();
        await menu.waitFor({ state: "detached" });
        assert.equal(await page.locator("#status").textContent(), w.last, where);
        assert.equal(posts.length, before, `${where}: hiding the last tab was saved`);

        // nothing but the setting was posted: what agents are given is as it was
        assert.deepEqual([...new Set(posts.map(([p]) => p))], ["/api/plugins/builtin"], where);
        assert.deepEqual(errors, [], where);
        await context.close();
      }
    });
  }
}
