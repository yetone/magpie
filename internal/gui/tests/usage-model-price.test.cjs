// Run with Node's test runner and Playwright on the module path; see README.md.
// soilzhu80 on X: a model an agent runs is often in no provider's list, so
// no provider's Model settings can price it. A model's row on the Usage
// page (its "…", or a right-click) and its bar on the Sessions tab (a
// right-click) offer "Set price…": a sheet of four prices in $ / 1M tokens,
// the maker's list price greyed in empty boxes, that POSTs the model's
// price from any provider (/api/model-price) and reads the page again. A
// typed comma is a decimal point, a price that is no number is refused in
// the sheet, a model with no list price needs its input and output, and
// Restore default takes a set price away. The Sessions bar asks for the
// model's bare id, the one a session's model is priced at. Narrow window,
// both engines, every language; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const G = (id, name, tokens, extra) => ({ id, name, icon: "generic", calls: 10, input: tokens - 100, output: 100, cache_read: 0, cache_write: 0, cost: 0, ...extra });
const KNOWN = G("relay/sol-7", "sol-7", 900000, { provider: "relay", model: "sol-7", sub: "Relay" });
const AGENT = G("relay/agent-only-9", "agent-only-9", 300000, { provider: "relay", model: "agent-only-9", sub: "Relay" });
const usage = {
  calls: 20, errors: 0, input: 1199800, output: 200, reasoning: 0, cache_read: 0, cache_write: 0, unpriced: 10, cost: 0, bucket: "day",
  series: [], path: "~/.config/magpie/usage.jsonl", agents: [G("pi", "Pi", 1200000)], models: [KNOWN, AGENT],
};
const MAKER = { input: 0.5, output: 2, cache_read: 0.05, cache_write: 0.625 };
const today = new Date();
const iso = (d) => [d.getFullYear(), d.getMonth() + 1, d.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const days = [{ date: iso(today), usage: [{ agent: "claude", cwd: "/work/a", model: "relay/agent-only-9", input: 30000, output: 4000, cache_read: 0, cache_write: 0, cost: 0, priced: false }], active: [] }];

function serve(lang, saved, asks) {
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    const settings = () => ({ lang, theme: "light" });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], clients: [{ id: "pi", name: "Pi", icon: "pi" }], profiles: [], settings: settings() });
    if (url.pathname === "/api/model-price") {
      const model = req.method() === "POST" ? JSON.parse(req.postData()).model : url.searchParams.get("model");
      const maker = model === "sol-7" ? MAKER : undefined;
      if (req.method() === "POST") {
        const body = JSON.parse(req.postData());
        asks.posts.push(body);
        if (body.price) saved[model] = body.price; else delete saved[model];
      } else asks.gets.push(model);
      return json({ model, price: saved[model] || null, maker });
    }
    if (url.pathname === "/api/usage") { asks.usage++; return json(usage); }
    if (url.pathname === "/api/settings") return json(settings());
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: false } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/sessions/progress") return json({ indexing: false });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: ["/test/sessions"] });
    if (url.pathname === "/api/sessions/stats") { asks.stats++; return json({ from: days[0].date, to: days[0].date, days, agents: { claude: "Claude Code" } }); }
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

const L = {
  en: { item: "Set price…", clear: "Clear usage…", title: "Price of sol-7", save: "Save", back: "Restore default", bad: "A price is a number of dollars, 0 or more", none: "agent-only-9 has no list price: give its input and output prices", set: "sol-7 is counted at the price you set", list: "sol-7 is counted at its list price again" },
  zh: { item: "设置价格…", clear: "清除用量…", title: "sol-7 的价格", save: "保存", back: "恢复默认", bad: null, none: null, set: "sol-7 已按你设置的价格计算", list: "sol-7 已恢复按标价计算" },
  "zh-TW": { item: "設定價格…", clear: "清除用量…", title: "sol-7 的價格", save: null, back: null, bad: null, none: null, set: "sol-7 已依你設定的價格計算", list: "sol-7 已恢復依標價計算" },
  ja: { item: "価格を設定…", clear: "使用量を消去…", title: "sol-7 の価格", save: null, back: null, bad: null, none: null, set: "sol-7 は設定した価格で計算されます", list: "sol-7 は再び定価で計算されます" },
  de: { item: "Preis festlegen…", clear: "Nutzung löschen…", title: "Preis von sol-7", save: null, back: null, bad: null, none: null, set: "sol-7 wird zu deinem Preis gerechnet", list: "sol-7 wird wieder zum Listenpreis gerechnet" },
};

// the sheet fits the window: nothing in it wider than the dialog
const fits = (p) => p.evaluate(() => {
  const ed = document.querySelector(".price-ask"), r = ed.getBoundingClientRect();
  const out = [...ed.querySelectorAll("input, button, b, p")].filter((n) => !n.hidden && n.getBoundingClientRect().right > r.right + 1).map((n) => n.outerHTML.slice(0, 80));
  return { out, wide: document.documentElement.scrollWidth <= innerWidth + 1, inside: r.left >= 0 && r.right <= innerWidth };
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a model's price is set from its Usage row and its Sessions bar", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) {
      const w = L[lang];
      await t.test(lang, async () => {
        const errors = [];
        const saved = {}, asks = { posts: [], gets: [], usage: 0, stats: 0 };
        const ctx = await browser.newContext({ viewport: { width: 420, height: 760 }, reducedMotion: "reduce" });
        const p = await ctx.newPage();
        p.setDefaultTimeout(5000);
        p.setDefaultNavigationTimeout(60000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", serve(lang, saved, asks));
        await p.goto("http://magpie.test/?view=usage");
        const row = p.locator(`#usageModels .row.stat[data-id="${KNOWN.id}"]`);
        await row.waitFor();
        await row.scrollIntoViewIfNeeded();

        // the row's "…": Set price… above Clear usage…
        await row.locator(".us-more").click();
        const items = p.locator(".row-menu .rm-item");
        assert.deepEqual((await items.allTextContents()).map((s) => s.trim()), [w.item, w.clear]);
        await items.first().click();
        const sheet = p.locator("#modal .price-ask");
        await sheet.waitFor();
        assert.deepEqual(asks.gets, ["sol-7"]);
        assert.equal((await sheet.locator(".ehead b").textContent()).trim(), w.title);
        const box = (k) => sheet.locator(`input[data-part="${k}"]`);
        // empty, the maker's price greyed in each box; the first one focused
        assert.deepEqual(await sheet.locator("input").evaluateAll((is) => is.map((i) => [i.value, i.placeholder])), [["", "0.5"], ["", "2"], ["", "0.05"], ["", "0.625"]]);
        assert.equal(await box("input").evaluate((i) => i === document.activeElement), true);
        assert.equal(await sheet.locator("button", { hasText: w.back || "Restore default" }).count(), 0, "nothing set: nothing to restore");
        const f = await fits(p);
        assert.deepEqual(f.out, []);
        assert.ok(f.wide && f.inside, JSON.stringify(f));
        if (w.save) assert.equal((await sheet.locator(".bar .primary").textContent()).trim(), w.save);

        // no number: said in the sheet, nothing sent
        await box("input").fill("abc");
        await sheet.locator(".bar .primary").click();
        const bad = sheet.locator(".price-ask-bad");
        await bad.waitFor();
        if (w.bad) assert.equal((await bad.textContent()).trim(), w.bad);
        assert.deepEqual(asks.posts, []);

        // a comma for the decimal point; the boxes left empty take the maker's
        const before = asks.usage;
        await box("input").fill("1,5");
        await box("output").fill("6");
        await box("output").press("Enter");
        await sheet.waitFor({ state: "detached" });
        assert.deepEqual(asks.posts, [{ model: "sol-7", price: { input: 1.5, output: 6, cache_read: 0.05, cache_write: 0.625 } }]);
        await p.locator("#status", { hasText: w.set }).waitFor();
        for (let i = 0; i < 50 && asks.usage === before; i++) await p.waitForTimeout(20);
        assert.ok(asks.usage > before, "the page is read again");

        // opened again: the price set, and Restore default takes it away
        await row.click({ button: "right" });
        await p.locator(".row-menu .rm-item", { hasText: w.item }).click();
        await sheet.waitFor();
        assert.deepEqual(await sheet.locator("input").evaluateAll((is) => is.map((i) => i.value)), ["1.5", "6", "0.05", "0.625"]);
        const back = sheet.locator(".bar button").first();
        if (w.back) assert.equal((await back.textContent()).trim(), w.back);
        await back.click();
        await sheet.waitFor({ state: "detached" });
        assert.deepEqual(asks.posts.at(-1), { model: "sol-7", price: null });
        await p.locator("#status", { hasText: w.list }).waitFor();

        // a model with no list price needs its input and output
        const other = p.locator(`#usageModels .row.stat[data-id="${AGENT.id}"]`);
        await other.click({ button: "right" });
        await p.locator(".row-menu .rm-item", { hasText: w.item }).click();
        await sheet.waitFor();
        assert.deepEqual(await sheet.locator("input").evaluateAll((is) => is.map((i) => i.placeholder)), ["", "", "", ""]);
        await box("input").fill("2");
        await sheet.locator(".bar .primary").click();
        await bad.waitFor();
        if (w.none) assert.equal((await bad.textContent()).trim(), w.none);
        assert.equal(asks.posts.length, 2);
        await box("output").fill("8");
        await sheet.locator(".bar .primary").click();
        await sheet.waitFor({ state: "detached" });
        assert.deepEqual(asks.posts.at(-1), { model: "agent-only-9", price: { input: 2, output: 8, cache_read: 0, cache_write: 0 } });

        // Sessions: a model's bar, right-clicked, prices its bare id
        await p.evaluate(() => { localStorage.setItem("magpie.usageTab", "sessions"); localStorage.setItem("magpie.sessRange", "all"); });
        await p.goto("http://magpie.test/?view=usage");
        const bar = p.locator("#sessModels .sess-bar").first();
        await bar.waitFor();
        await bar.scrollIntoViewIfNeeded();
        await bar.click({ button: "right" });
        const only = p.locator(".row-menu .rm-item");
        assert.deepEqual((await only.allTextContents()).map((s) => s.trim()), [w.item]);
        await only.click();
        await sheet.waitFor();
        assert.equal(asks.gets.at(-1), "agent-only-9");
        assert.deepEqual(await sheet.locator("input").evaluateAll((is) => is.map((i) => i.value)), ["2", "8", "0", "0"]);
        const stats = asks.stats;
        await box("input").fill("3");
        await sheet.locator(".bar .primary").click();
        await sheet.waitFor({ state: "detached" });
        assert.deepEqual(asks.posts.at(-1), { model: "agent-only-9", price: { input: 3, output: 8, cache_read: 0, cache_write: 0 } });
        for (let i = 0; i < 50 && asks.stats === stats; i++) await p.waitForTimeout(20);
        assert.ok(asks.stats > stats, "the Sessions tab is read again");
        // Escape closes it, sending nothing
        await bar.click({ button: "right" });
        await only.click();
        await sheet.waitFor();
        const sent = asks.posts.length;
        await box("input").press("Escape");
        await sheet.waitFor({ state: "detached" });
        assert.equal(asks.posts.length, sent);

        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
