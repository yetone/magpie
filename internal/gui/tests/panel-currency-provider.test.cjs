// Run with Node's test runner and Playwright on the module path; see README.md.
// Currency "By provider" in the tray panel's Usage tab and the window's
// Requests tab (onkashiko on X: "在菜单栏迷你窗中还是只会显示为一种货币").
// The ranking's rows and their tooltips are one provider's costs, in what it
// sells in: DeepSeek's and SiliconFlow's in yuan, OpenAI's in dollars; a
// model in that provider's (deepseek-v4 at DeepSeek and SiliconFlow, both
// yuan), a model served in both currencies, the "Other" row, the chart's axis
// and the total in the "Totals in" currency, never added up as one. With one
// provider picked the whole page is its calls: the total and the axis in its
// currency. Every language, a 440px and a 320px panel, a 1100px and a 420px
// window; no backend, the API is faked as the Go side answers it
// (usageapi.Ledger's provider, fx.yuan). MAGPIE_ASSETS runs it on other assets.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets");
const midnight = new Date();
midnight.setHours(0, 0, 0, 0);

// what each provider's calls of each model cost, in dollars as stored
const COST = { deepseek: { "deepseek-v4": 1 }, siliconflow: { "deepseek-v4": 0.25, "kimi-k3": 0.25 }, openai: { "kimi-k3": 0.5, "gpt-5": 1.5 } };
const NAMES = { deepseek: "DeepSeek", siliconflow: "SiliconFlow", openai: "OpenAI" };
const share = (id, name, cost, icon) => ({ id, name, icon, calls: 2, errors: 0, input: cost * 1e5, output: cost * 5e4, cache_read: 0, cache_write: 0, cost });
const sum = (xs) => xs.reduce((a, b) => a + b, 0);

function ledger(q) {
  const only = q.get("provider");
  const who = Object.entries(COST).filter(([id]) => !only || id === only);
  const models = {};
  for (const [, ms] of who) for (const [m, c] of Object.entries(ms)) models[m] = (models[m] || 0) + c;
  const total = sum(Object.values(models));
  const byModel = Object.entries(models).map(([m, c]) => share(m, m, c)).sort((a, b) => b.cost - a.cost);
  const byProvider = Object.entries(COST).map(([id, ms]) => share(id, NAMES[id], sum(Object.values(ms)), "generic")).sort((a, b) => b.cost - a.cost);
  const modelAt = Object.entries(COST).flatMap(([id, ms]) => Object.entries(ms).map(([m, c]) => share(id + "/" + m, m + " · " + NAMES[id], c, "generic")));
  const by = { provider: {}, model: {}, modelAt: {} };
  for (const [id, ms] of who) {
    by.provider[id] = { calls: 2, tokens: 1000, cost: sum(Object.values(ms)) };
    for (const [m, c] of Object.entries(ms)) by.modelAt[id + "/" + m] = { calls: 1, tokens: 500, cost: c };
  }
  for (const [m, c] of Object.entries(models)) by.model[m] = { calls: 1, tokens: 500, cost: c };
  const series = Array.from({ length: 24 }, (_, i) => {
    const on = i === 10;
    return { label: String(i), time: new Date(midnight.getTime() + i * 3600e3).toISOString(), calls: on ? 6 : 0, errors: 0, input: on ? 1000 : 0, output: on ? 500 : 0, cache_read: 0, cache_write: 0, cost: on ? total : 0, by: on ? by : { provider: {}, model: {}, modelAt: {} } };
  });
  return {
    period: q.get("period") || "today", rows: [], offset: 0, total: 1, calls: 6, errors: 0, input: total * 1e5, output: total * 5e4, cache_read: 0, cache_write: 0, reasoning: 0, cost: total, unpriced: 0,
    ...(only ? { provider: only } : {}),
    bucket: "hour", series, by: { provider: byProvider, agent: [], model: byModel, modelAt },
    agents: [], providers: Object.entries(NAMES).map(([id, name]) => ({ id, name, icon: "generic" })),
  };
}

const fx = { rate: 7.2, at: new Date().toISOString(), stale: false, yuan: ["deepseek", "siliconflow"] };

function serve(lang, asked, totalCurrency) {
  const settings = { lang, theme: "light", currency: "provider", totalCurrency };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings, fx });
    if (url.pathname === "/api/settings") return json({ ...settings, fx });
    if (url.pathname === "/api/usage/requests") { asked.push(url.searchParams); return json(ledger(url.searchParams)); }
    if (url.pathname === "/api/usage/heatmap") return json({ days: [] });
    if (url.pathname === "/api/gateway/trace") return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 0, errors: 0, input: 0, output: 0, reasoning: 0, unpriced: 0, cost: 0, bucket: "day", series: [], agents: [], models: [], path: "" });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname.startsWith("/api/")) return route.fulfill(url.pathname.startsWith("/api/window/") ? { status: 204 } : { json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const BROWSERS = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];

// each ranked row's name and value
const rankOf = (p, box) => p.locator(box + " button.rk").evaluateAll((rs) => Object.fromEntries(rs.map((r) => [r.querySelector(".rk-nm").textContent, r.querySelector(".rk-val").textContent])));
// each ranked row's name and the cost in its second line (by tokens)
const subOf = (p, box) => p.locator(box + " button.rk").evaluateAll((rs) => Object.fromEntries(rs.map((r) => [r.querySelector(".rk-nm").textContent, (r.querySelector(".rk-b").textContent.match(/≈([$¥][\d.]+)/) || [])[1]])));
const axisOf = (p, box) => p.locator(box + " svg text.axis").first().textContent();
// the chart's tooltip over the one busy hour: each row's name and value, the total's last
async function tipOf(p, box) {
  const g = p.locator(box + " svg").first();
  const r = await g.boundingBox();
  await p.mouse.move(r.x + r.width - 2, r.y + r.height / 2); // off the busy hour first
  const at = await p.locator(box + " rect.col").first().boundingBox();
  await p.mouse.move(at.x + at.width / 2, at.y + Math.max(1, at.height / 2));
  const tip = p.locator(box + " .tip");
  await tip.waitFor();
  return tip.evaluate((t) => [...t.querySelectorAll("div")].map((d) => [d.querySelector("span")?.textContent, d.querySelector("em")?.textContent?.split(" · ")[0]]));
}

for (const engine of BROWSERS) {
  for (const lang of LANGS) {
    test(`${engine} ${lang}: the panel's Usage shows each provider's cost in its own currency`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const width of [440, 320]) {
        const ctx = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
        const errors = [], asked = [];
        const p = await ctx.newPage();
        p.setDefaultTimeout(5000);
        p.setDefaultNavigationTimeout(60000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.addInitScript(() => { try { localStorage.setItem("magpie.panelTab", "stats"); localStorage.setItem("magpie.panelUseSplit", "provider"); localStorage.setItem("magpie.panelUseMetric", "tokens"); } catch {} });
        await p.route("**/*", serve(lang, asked, "usd"));
        await p.goto("http://magpie.test/?mode=panel");
        const box = "#panelUsage";
        await p.locator(box + " .pu-card .led-rank .rk").first().waitFor();

        // by tokens: each provider's cost under it, in what it sells in
        assert.deepEqual(await subOf(p, box), { DeepSeek: "¥7.20", OpenAI: "$2.00", SiliconFlow: "¥3.60" }, `${width}px`);
        // the total across them in the Totals in currency, dollars
        assert.equal(await p.locator(box + " .pu-tot .v.cost").textContent(), "≈$3.50");

        // by cost: the ranking's figures too; the axis is a sum, in dollars
        await p.locator(box + " .pu-card .led-bar .segs:not(.pu-split) .opt").nth(1).click();
        await p.waitForFunction(() => [...document.querySelectorAll("#panelUsage .rk-val")].some((v) => v.textContent.startsWith("¥")));
        assert.deepEqual(await rankOf(p, box), { DeepSeek: "¥7.20", OpenAI: "$2.00", SiliconFlow: "¥3.60" });
        assert.match(await axisOf(p, box), /^\$/);
        const tip = await tipOf(p, box);
        assert.deepEqual(tip.slice(0, -1).sort(), [["DeepSeek", "¥7.20"], ["OpenAI", "$2.00"], ["SiliconFlow", "¥3.60"]]);
        assert.equal(tip.at(-1)[1], "$3.50", "the hour's total in dollars");

        // by model: one served only in yuan is in yuan; one served in yuan and
        // dollars is a sum of both, in the totals' dollars
        await p.locator(box + " .pu-split .opt").nth(1).click();
        await p.waitForFunction(() => document.querySelectorAll("#panelUsage .rk").length === 3 && [...document.querySelectorAll("#panelUsage .rk-nm")].some((n) => n.textContent === "gpt-5"));
        assert.deepEqual(await rankOf(p, box), { "deepseek-v4": "¥9.00", "gpt-5": "$1.50", "kimi-k3": "$0.750" });

        // one provider picked: every figure is DeepSeek's, the total and axis in yuan
        await p.locator(box + " .pu-split .opt").nth(0).click();
        await p.locator(box + " .led-rank .rk", { hasText: "DeepSeek" }).click();
        await p.waitForFunction(() => document.querySelector("#panelUsage .rk-nm")?.textContent === "deepseek-v4" && !document.querySelector("#panelUsage.pu-loading"));
        assert.equal(asked.at(-1).get("provider"), "deepseek");
        assert.equal(await p.locator(box + " .pu-tot .v.cost").textContent(), "≈¥7.20");
        assert.deepEqual(await rankOf(p, box), { "deepseek-v4": "¥7.20" });
        assert.match(await axisOf(p, box), /^¥/);

        assert(await p.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), `no sideways scroll at ${width}px`);
        const out = await p.locator(box + " .rk-val").evaluateAll((vs) => vs.filter((v) => { const r = v.getBoundingClientRect(), c = v.closest(".pu-card").getBoundingClientRect(); return r.right > c.right + 0.5; }).length);
        assert.equal(out, 0, `every figure inside the card at ${width}px`);
        assert.deepEqual(errors, []);
        await ctx.close();
      }
    });

    test(`${engine} ${lang}: the window's Requests shows each provider's cost in its own currency`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const width of [1100, 420]) {
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        const errors = [], asked = [];
        const p = await ctx.newPage();
        p.setDefaultTimeout(5000);
        p.setDefaultNavigationTimeout(60000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.addInitScript(() => { try { localStorage.setItem("magpie.ledMetric", "cost"); } catch {} });
        // totals in yuan this time: OpenAI's stays in dollars
        await p.route("**/*", serve(lang, asked, "cny"));
        await p.goto("http://magpie.test/");
        await p.locator('[data-view="usage"]').first().click();
        await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
        await p.locator("#ledRank button.rk").first().waitFor();
        await p.waitForTimeout(200);
        const box = "#ledRank";
        // Provider: LED_SPLITS' third
        await p.locator("#ledSplit .opt").nth(2).click();
        await p.waitForFunction(() => [...document.querySelectorAll("#ledRank .rk-nm")].some((n) => n.textContent === "OpenAI"));
        assert.deepEqual(await rankOf(p, box), { DeepSeek: "¥7.20", OpenAI: "$2.00", SiliconFlow: "¥3.60" }, `${width}px`);
        assert.match(await axisOf(p, "#ledChart"), /^¥/, "the axis, a sum, in the totals' yuan");
        // Model: a model served in both currencies is a sum, in yuan
        await p.locator("#ledSplit .opt").nth(0).click();
        await p.waitForFunction(() => [...document.querySelectorAll("#ledRank .rk-nm")].some((n) => n.textContent === "gpt-5"));
        assert.deepEqual(await rankOf(p, box), { "deepseek-v4": "¥9.00", "gpt-5": "$1.50", "kimi-k3": "¥5.40" });
        // Model · provider: each at its own provider
        await p.locator("#ledSplit .opt").nth(1).click();
        await p.waitForFunction(() => document.querySelectorAll("#ledRank .rk").length >= 5);
        const at = await rankOf(p, box);
        assert.equal(at["kimi-k3 · OpenAI"], "$0.500");
        assert.equal(at["kimi-k3 · SiliconFlow"], "¥1.80");

        // OpenAI picked: the whole page is its calls, in dollars
        await p.locator("#ledSplit .opt").nth(2).click();
        await p.locator(box + " button.rk", { hasText: "OpenAI" }).click();
        await p.waitForFunction(() => document.querySelector("#usageCost b")?.textContent === "≈$2.00");
        assert.equal(asked.at(-1).get("provider"), "openai");
        await p.waitForFunction(() => /^\$/.test(document.querySelector("#ledChart svg text.axis")?.textContent || ""));
        assert.deepEqual(errors, []);
        await ctx.close();
      }
    });
  }
}
