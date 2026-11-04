// Run with Node's test runner and Playwright on the module path; see README.md.
// A plugin option that says placement "top" is drawn under the accounts,
// above Routing (#5269, mycf_711): WorkBuddy Pro's "panel" switch is about
// the whole plugin, and under the models it took a scroll past the whole
// list each time. The rest stay under the models, where they always were;
// a plugin that says no placement is drawn as before. The note saying the
// plugin reads them goes with the first block. Its switch is saved like any
// other. Both engines, 1100 and 420 wide, every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const panel = { key: "panel", label: "Panel", description: "Draws the plugin's own page.", placement: "top", on: false };
const free = { key: "free_only", label: "Free models only", on: false };
const CASES = {
  mixed: [panel, free],
  top: [panel],
  // an older plugin: no placement, or one magpie doesn't know
  none: [{ ...panel, placement: undefined }, { ...free, placement: "sidebar" }],
};

function serve(lang, options, posts) {
  const plugin = { id: "wbpro", pid: "wbpro", name: "WorkBuddy Pro", icon: "generic", spec: "workbuddy-pro", signedIn: true, models: 2,
    methods: [{ type: "api", label: "API key" }], options };
  const payload = { providers: [{ id: "wbpro", name: "WorkBuddy Pro", icon: "generic", chat: "plugin://wbpro/v1", responses: "", anthropic: "", catalog: "",
    models: [{ id: "m1", name: "M1", on: true }, { id: "m2", name: "M2", on: true }], agents: [], fallback: [], headers: {}, keyList: [], key: {}, proxy: "", maxConcurrency: null,
    // two accounts in use, so Routing is drawn
    account: { agent: "wbpro", agentName: "WorkBuddy Pro", agentIcon: "generic", user: "a@fake",
      logins: [{ user: "a@fake", active: true, on: true }, { user: "b@fake", on: true }] } }],
  presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [plugin] };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(payload);
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/settings/plugin-option") {
      posts.push(route.request().postDataJSON());
      return json({ lang, theme: "light" });
    }
    if (url.pathname.startsWith("/api/provider/") && route.request().method() === "POST") return json(payload);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const W = {
  en: { field: "Plugin options", routing: "Routing", models: "Models", on: "On", save: "Save" },
  zh: { field: "插件选项", routing: "路由", models: "模型", on: "开启", save: "保存" },
  "zh-TW": { field: "外掛選項", routing: "路由", models: "模型", on: "開啟", save: "儲存" },
  ja: { field: "プラグインのオプション", routing: "ルーティング", models: "モデル", on: "オン", save: "保存" },
  de: { field: "Plugin-Optionen", routing: "Weiterleitung", models: "Modelle", on: "An", save: "Speichern" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a plugin option placed at the top", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const width of [1100, 420]) for (const lang of Object.keys(W)) for (const which of Object.keys(CASES)) {
      await t.test(`${width} ${lang} ${which}`, async () => {
        const w = W[lang], posts = [];
        const ctx = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, CASES[which], posts));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "WorkBuddy Pro" }).click();
        const ed = page.locator("#modal .editor");
        await ed.locator(".plugin-options").first().waitFor();
        await page.waitForTimeout(300); // the editor drawn again as what it asked for comes in

        // each block: whether it is the top one, its label, its keys, its
        // note, and where it sits among the accounts, Routing and Models
        const seen = await ed.evaluate((ed, w) => {
          const e = ed.querySelector(":scope > .ebody") || ed, kids = [...e.children];
          const at = (x) => kids.findIndex((k) => k.contains(x));
          const label = (text) => kids.findIndex((k) => k.tagName === "LABEL" && k.textContent === text);
          return {
            accounts: at(e.querySelector(".accts")), routing: label(w.routing), models: label(w.models),
            blocks: [...e.querySelectorAll(".plugin-options")].map((b) => ({
              top: b.classList.contains("top"), at: at(b), label: kids[at(b) - 1].textContent,
              keys: [...b.querySelectorAll(".plugin-option")].map((o) => o.dataset.key),
              note: !!kids[at(b)].querySelector(":scope > .hint"),
            })),
            spill: document.documentElement.scrollWidth - document.documentElement.clientWidth,
          };
        }, w);
        assert.ok(seen.accounts >= 0 && seen.routing > seen.accounts && seen.models > seen.routing, JSON.stringify(seen));
        const shape = seen.blocks.map((b) => [b.top, b.label, b.keys, b.note]);
        if (which === "mixed") {
          assert.deepEqual(shape, [[true, w.field, ["panel"], true], [false, w.field, ["free_only"], false]]);
          assert.ok(seen.blocks[0].at > seen.accounts && seen.blocks[0].at < seen.routing, "the top block sits between the accounts and Routing: " + JSON.stringify(seen));
          assert.ok(seen.blocks[1].at > seen.models, "the rest stay under the models: " + JSON.stringify(seen));
        } else if (which === "top") {
          assert.deepEqual(shape, [[true, w.field, ["panel"], true]]);
          assert.ok(seen.blocks[0].at > seen.accounts && seen.blocks[0].at < seen.routing, JSON.stringify(seen));
        } else {
          assert.deepEqual(shape, [[false, w.field, ["panel", "free_only"], true]]);
          assert.ok(seen.blocks[0].at > seen.models, "as before, under the models: " + JSON.stringify(seen));
        }
        assert.ok(seen.spill <= 0, "the page scrolls sideways by " + seen.spill);
        if (process.env.ARTIFACT_DIR && which === "mixed") await ed.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `plugin-option-placement-${engine}-${width}-${lang}.png`) });

        // the top switch is saved as any other
        await ed.locator('.plugin-option[data-key="panel"] .opt', { hasText: w.on }).click();
        await ed.locator(".bar").getByRole("button", { name: w.save, exact: true }).click();
        for (let i = 0; i < 50 && !posts.length; i++) await page.waitForTimeout(50);
        assert.deepEqual(posts, [{ provider: "wbpro", key: "panel", on: true }]);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
