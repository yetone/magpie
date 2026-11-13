// Run with Node's test runner and Playwright on the module path; see README.md.
// The tray panel's Usage tab tells all providers' calls apart by their models
// too (OHHHHHH05 on X): a switch beside the metric turns the ranking and the
// chart from providers to models, the pick is kept for the next time the
// panel opens, and with one provider picked the switch steps aside (its
// models are what is shown then). Every language, a 440px and a 320px panel,
// nothing out of the card; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const midnight = new Date();
midnight.setHours(0, 0, 0, 0);

// each provider's models, and what each made an hour it was used
const WHO = { anthropic: { name: "Claude", models: { "claude-opus-5": 600000, "claude-sonnet-5": 300000 } }, relay: { name: "Relay", models: { "gpt-6-sol": 450000 } } };
const share = (id, name, tokens, icon) => ({ id, name, icon, calls: Math.round(tokens / 1e5), errors: 0, input: tokens * 0.1, output: tokens * 0.05, cache_read: tokens * 0.85, cache_write: 0, cost: tokens / 1e6 });

function page(q) {
  const only = q.get("provider");
  const who = Object.entries(WHO).filter(([id]) => !only || id === only);
  const hours = 10;
  const series = Array.from({ length: 24 }, (_, i) => {
    const by = { provider: {}, model: {}, agent: {} };
    let tokens = 0;
    if (i >= 8 && i < 8 + hours) for (const [id, w] of who) {
      let mine = 0;
      for (const [m, n] of Object.entries(w.models)) { by.model[m] = { calls: 1, tokens: n, cost: n / 1e6 }; mine += n; }
      by.provider[id] = { calls: Object.keys(w.models).length, tokens: mine, cost: mine / 1e6 };
      tokens += mine;
    }
    return { label: String(i), time: new Date(midnight.getTime() + i * 3600e3).toISOString(), calls: tokens ? 3 : 0, errors: 0, input: tokens * 0.1, output: tokens * 0.05, cache_read: tokens * 0.85, cache_write: 0, cost: tokens / 1e6, by };
  });
  const all = series.reduce((a, p) => a + p.input + p.output + p.cache_read, 0);
  const models = who.flatMap(([, w]) => Object.entries(w.models).map(([m, n]) => share(m, m, n * hours))).sort((a, b) => b.cache_read - a.cache_read);
  const providers = Object.entries(WHO).map(([id, w]) => share(id, w.name, Object.values(w.models).reduce((a, n) => a + n, 0) * hours, "generic")).sort((a, b) => b.cache_read - a.cache_read);
  return {
    period: q.get("period"), rows: [], offset: 0, total: 1, calls: 30, errors: 0, input: all * 0.1, output: all * 0.05, cache_read: all * 0.85, cache_write: 0, reasoning: 0, cost: all / 1e6, unpriced: 0,
    bucket: "hour", series, by: { provider: providers, agent: [], model: models },
    agents: [], providers: Object.entries(WHO).map(([id, w]) => ({ id, name: w.name, icon: "generic" })),
  };
}

function serve(lang, asked) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/usage/requests") { asked.push(url.searchParams); return json(page(url.searchParams)); }
    if (url.pathname === "/api/gateway/trace") return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return route.fulfill(url.pathname.startsWith("/api/window/") ? { status: 204 } : { json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const SPLIT = { en: ["Provider", "Model"], zh: ["供应商", "模型"], "zh-TW": ["供應商", "模型"], ja: ["プロバイダ", "モデル"], de: ["Anbieter", "Modell"] };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, names] of Object.entries(SPLIT)) {
    test(`${engine} ${lang}: the panel's Usage tells the models apart`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const ctx = await browser.newContext({ viewport: { width: 440, height: 700 }, reducedMotion: "reduce" });
      const errors = [], asked = [];
      const open = async () => {
        const p = await ctx.newPage();
        p.setDefaultTimeout(5000);
        p.setDefaultNavigationTimeout(60000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.addInitScript(() => { try { localStorage.setItem("magpie.panelTab", "stats"); } catch {} });
        await p.route("**/*", serve(lang, asked));
        await p.goto("http://magpie.test/?mode=panel");
        await p.locator("#panelUsage .pu-card .led-rank .rk").first().waitFor();
        return p;
      };
      const p = await open();
      const split = p.locator("#panelUsage .pu-card .pu-split .opt");
      const ranked = () => p.locator("#panelUsage .led-rank .rk-nm").allTextContents();
      const keys = () => p.locator("#panelUsage rect.col").evaluateAll((r) => [...new Set(r.map((x) => x.dataset.k))].sort().join());

      // providers first, as before; the switch beside the metric
      assert.deepEqual(await split.allTextContents(), names);
      assert.equal(await p.locator("#panelUsage .pu-split .opt.on").textContent(), names[0]);
      assert.deepEqual(await ranked(), ["Claude", "Relay"]);

      // models: every provider's, in the ranking and the chart's colours
      const n = asked.length;
      await split.nth(1).click();
      assert.equal(await p.locator("#panelUsage .pu-split .opt.on").textContent(), names[1]);
      assert.deepEqual(await ranked(), ["claude-opus-5", "gpt-6-sol", "claude-sonnet-5"]);
      assert.equal(await keys(), "claude-opus-5,claude-sonnet-5,gpt-6-sol");
      assert.equal(asked.length, n, "the answer in hand has the models: nothing is read again");

      // the bar keeps to one line inside the card, at 440 and at 320
      for (const width of [440, 320]) {
        await p.setViewportSize({ width, height: 700 });
        await p.waitForTimeout(200);
        const fit = await p.locator("#panelUsage .pu-card .led-bar").evaluate((bar) => {
          const card = bar.closest(".pu-card").getBoundingClientRect();
          return [...bar.querySelectorAll(".opt")].every((b) => { const r = b.getBoundingClientRect(); return r.left >= card.left - 0.5 && r.right <= card.right + 0.5 && b.scrollWidth <= b.clientWidth + 1; });
        });
        assert(fit, `the switches fit the card at ${width}px`);
        assert(await p.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), `no sideways scroll at ${width}px`);
      }
      await p.setViewportSize({ width: 440, height: 700 });

      // kept: the panel opened again shows the models
      const q = await open();
      assert.equal(await q.locator("#panelUsage .pu-split .opt.on").textContent(), names[1]);
      assert.deepEqual(await q.locator("#panelUsage .led-rank .rk-nm").allTextContents(), ["claude-opus-5", "gpt-6-sol", "claude-sonnet-5"]);

      // one provider picked: its models, and the switch steps aside
      await split.nth(0).click();
      await p.locator("#panelUsage .led-rank .rk").first().click();
      for (let i = 0; i < 60 && asked.at(-1)?.get("provider") !== "anthropic"; i++) await p.waitForTimeout(40);
      await p.locator("#panelUsage:not(.pu-loading) .pu-card").waitFor();
      await p.waitForFunction(() => document.querySelector("#panelUsage .led-rank .rk-nm")?.textContent === "claude-opus-5");
      assert.deepEqual(await ranked(), ["claude-opus-5", "claude-sonnet-5"]);
      assert.equal(await p.locator("#panelUsage .pu-split").count(), 0, "no switch over one provider's models");
      assert.deepEqual(errors, []);
    });
  }
}
