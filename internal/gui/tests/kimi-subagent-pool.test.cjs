// Run with Node's test runner and Playwright on the module path; see README.md.
// Kimi Code's subagent pool (XiaoLing_Arisa on X): Kimi Code's own
// [secondary_model] is the model a subagent starts on, the other models its
// lead may start one on, and their effort. In the window they are one
// Subagents line of the opened row, not squares of their own: the model,
// its effort (off until a model is set), and the pool, ticked in one menu
// that stays open. Every language, Chromium and WebKit, wide and narrow.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { value: "magpie/think/deep", label: "Deep", ref: "think/deep" },
  { value: "magpie/think/wide", label: "Wide", ref: "think/wide" },
];
const kimi = () => ({
  id: "kimi", name: "Kimi Code", icon: "kimi", path: "/home/u/.kimi-code/config.toml", wired: true,
  fields: [
    { key: "model", label: "model", value: "magpie/think/deep", options: models },
    { key: "effort", label: "thinking", value: "", options: [{ value: "" }, { value: "off" }, { value: "high" }] },
    { key: "subagent_model", label: "subagent model", value: "", options: [{ value: "" }, ...models] },
    { key: "subagent_pool", label: "subagent pool", value: "", options: models },
    { key: "subagent_effort", label: "subagent effort", value: "", options: [{ value: "" }, { value: "off" }, { value: "high" }] },
  ],
});

function server(lang, sets) {
  let cur = { agents: [kimi()], profiles: [] };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { ...cur, settings: { lang, theme: "light", guideHidden: true } } });
    if (url.pathname === "/api/set") {
      const body = req.postDataJSON();
      sets.push(body);
      cur = JSON.parse(JSON.stringify(cur));
      const fields = cur.agents[0].fields, f = (k) => fields.find((x) => x.key === k);
      f(body.field).value = body.value;
      // as kimi_secondary.go: the subagent model joins the pool, and the
      // pool's first is the subagent model when there was none
      if (body.field === "subagent_model" && body.value && f("subagent_pool").value && !f("subagent_pool").value.split("\n").includes(body.value)) f("subagent_pool").value += "\n" + body.value;
      if (body.field === "subagent_pool" && body.value && !f("subagent_model").value) f("subagent_model").value = body.value.split("\n")[0];
      return route.fulfill({ json: { ...cur, settings: { lang, theme: "light", guideHidden: true } } });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/agents/cli") return route.fulfill({ json: { agents: {}, pending: false } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the words as i18n.js has them
const W = {
  en: { subagents: "Subagents", more: "More models" },
  zh: { subagents: "子代理", more: "更多模型" },
  "zh-TW": { subagents: "子代理", more: "更多模型" },
  ja: { subagents: "サブエージェント", more: "ほかのモデル" },
  de: { subagents: "Subagenten", more: "Weitere Modelle" },
};

const row = '.row.agent[data-id="kimi"]';
const panel = `${row} .ag-exp`;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(W)) {
    for (const width of [1100, 560]) {
      test(`${engine} ${lang} ${width}px: Kimi Code's subagent pool`, async (t) => {
        const w = W[lang];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await browser.newPage({ viewport: { width, height: 800 } });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], sets = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, sets));
        t.after(() => browser.close());
        await page.goto("http://magpie.test/");
        await page.locator(row).waitFor();
        // closed, the row has no square for the pool
        assert.equal(await page.locator(`${row} [data-key="subagent_pool"]`).count(), 0);
        // the sheet is measured as it first appears: the line (kimisub.js)
        // is in it then, not a task later
        await page.evaluate((sel) => new MutationObserver((_, o) => {
          const p = document.querySelector(sel);
          if (p) { window.poolAtFirst = !!p.querySelector(".ag-pool"); o.disconnect(); }
        }).observe(document.body, { childList: true, subtree: true }), panel);
        await page.locator(`${row} .ag-link`).click();
        await page.locator(panel).waitFor();
        assert.equal(await page.evaluate(() => window.poolAtFirst), true, "the Subagents line is there when Details first draws");

        // one Subagents line, in the panel only
        const line = page.locator(`${panel} .ag-kv`, { has: page.locator(".ag-k", { hasText: w.subagents }) });
        assert.equal(await line.count(), 1);
        for (const k of ["subagent_model", "subagent_effort", "subagent_pool"]) {
          assert.equal(await page.locator(`${row} [data-key="${k}"]`).count(), 1, k);
          assert.equal(await line.locator(`[data-key="${k}"]`).count(), 1, k);
        }
        const eff = line.locator('[data-key="subagent_effort"]');
        assert.ok(await eff.isDisabled(), "an effort without a subagent model");
        const pool = line.locator(".ag-pool");
        assert.equal(await pool.locator(".v").textContent(), w.more);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "the Agents view overflows");

        // the pool: two ticks in one menu, which stays open
        const y = await page.evaluate(() => scrollY);
        await pool.click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        assert.equal(await page.evaluate(() => scrollY), y, "the click scrolled the page");
        await page.locator("#pop #list li", { hasText: "Wide" }).first().click();
        await page.waitForFunction(() => document.querySelector('.row.agent[data-id="kimi"] .ag-exp [data-key="subagent_model"] .v')?.textContent === "Wide");
        assert.ok(!(await page.locator("#pop").isHidden()), "the menu closed after one tick");
        await page.locator("#pop #list li", { hasText: "Deep" }).first().click();
        await page.waitForFunction(() => document.querySelector('.row.agent[data-id="kimi"] .ag-pool .v')?.textContent === "Deep");
        assert.deepEqual(sets, [
          { agent: "kimi", field: "subagent_pool", value: "magpie/think/wide" },
          { agent: "kimi", field: "subagent_pool", value: "magpie/think/wide\nmagpie/think/deep" },
        ]);
        // the subagent model's own tick can't be taken out of the pool
        await page.locator("#pop #list li", { hasText: "Wide" }).first().click();
        assert.equal(sets.length, 2);
        assert.equal(await page.locator("#pop #list li.cur").count(), 2);
        await page.keyboard.press("Escape");
        await page.locator("#pop").waitFor({ state: "hidden" });

        // with a subagent model, its effort can be picked
        assert.ok(!(await page.locator(`${panel} [data-key="subagent_effort"]`).isDisabled()));
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "the Agents view overflows");
        assert.deepEqual(errors, []);
      });
    }
  }
}
