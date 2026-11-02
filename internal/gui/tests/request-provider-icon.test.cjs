// Run with Node's test runner and Playwright on the module path; see README.md.
// The request lists name who answered with the provider's logo before its
// name (Jiabubu on Discord): Routing's Requests, from the try's member as
// the request's story draws it, and Usage → Requests' "Provider · account"
// cell, from the ledger's providers. A row a session file was read for names
// no provider for sure and has no logo; a request that found no one has none
// either. In every language, Chromium and WebKit, wide and narrow; no
// backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const codex = [{ id: "codex:1", provider: "codex", name: "Codex", who: "ann@example.com", kind: "account", model: "gpt-5.6-terra", known: true, icon: "codex-color", routing: "order" }];
const ds = { id: "deepseek", provider: "deepseek", name: "DeepSeek", kind: "provider", model: "deepseek-flash", icon: "deepseek-color", routing: "order" };
const mk = (i, w, model, status) => ({
  id: 100 - i, seq: 100 - i, time: at(i), agent: "codex", model, provider: w.provider,
  order: w.provider === "codex" ? codex : [w], done: true, status, ms: 4000, tokens: 9000,
  ...(status === 200 ? { tries: [{ id: w.id, model: w.model, start: at(i), done: true, status, ms: 4000 }] } : { tries: [], error: "no provider serves it" }),
});
const routes = [mk(0, codex[0], "gpt-5.6-terra", 200), mk(1, ds, "deepseek-flash", 200), mk(2, ds, "deepseek-flash", 503)];

const ROW = { t: now.toISOString(), agent: "codex", agentName: "Codex", icon: "codex-color", provider: "openai", providerName: "OpenAI", req: "gpt-5.5", model: "gpt-5.5", in: 2, out: 600, cache_write: 0, cache_read: 3900, ms: 2380, status: 200, cost: 0.08, priced: true };
const ROWS = [
  { ...ROW, route_id: "r-1" },
  { ...ROW, t: new Date(now - 60000).toISOString(), provider: "zhipu", providerName: "Zhipu", req: "glm-5", model: "glm-5" },
  // read from Codex's own session file: no provider for sure, no logo
  { ...ROW, t: new Date(now - 120000).toISOString(), source: "log", provider: "", providerName: "" },
];

function routingServer(lang) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/t", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  const assets = path.resolve(__dirname, "../assets");
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      // a long poll left waiting when the test goes on to Usage: its page
      // is gone by then, so its reply has nowhere to go
      if (url.searchParams.get("wait")) return new Promise((r) => setTimeout(r, 20e3)).then(() => json({ mine: true, now: now.toISOString(), seq: 100, routes: [] })).catch(() => {});
      return json({ mine: true, now: now.toISOString(), seq: 100, totals: { requests: routes.length, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

function usageServer(lang, theme) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme }, fx: { rate: 7.2, at: new Date().toISOString() } });
    if (url.pathname === "/api/usage/requests") return json({
      period: "today", rows: ROWS, offset: 0, total: ROWS.length, calls: 3, errors: 0, input: 6, output: 1800, cache_write: 0, cache_read: 11700, cost: 0.24, reasoning: 0, unpriced: 0,
      bucket: "hour", series: [], by: { provider: [], agent: [], model: [] },
      agents: [{ id: "codex", name: "Codex", icon: "codex-color" }], providers: [{ id: "openai", name: "OpenAI", icon: "openai" }, { id: "zhipu", name: "Zhipu", icon: "zhipu-color" }],
    });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 1, errors: 0, input: 1, output: 1, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 0, cost: 1, bucket: "day", series: [], agents: [], models: [], path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a request names who answered with its logo", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 420]) {
        await t.test(`${lang} at ${width}`, async () => {
          const errors = [];
          const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
          t.after(() => context.close());
          const p = await context.newPage();
          p.setDefaultTimeout(5000);
          p.setDefaultNavigationTimeout(60000);
          p.on("pageerror", (e) => errors.push(e.message));

          // Routing's Requests
          await p.route("**/*", routingServer(lang));
          await p.goto("http://magpie.test/?view=routing");
          await p.locator(".rt-reqs .rt-req.bad").waitFor();
          const reqs = await p.locator(".rt-reqs .rt-req").evaluateAll((bs) => bs.map((b) => {
            const to = b.querySelector(".to"), ic = to.querySelector(":scope > .ic"), said = to.querySelector(".said");
            const box = (n) => n.getBoundingClientRect();
            return { bad: b.classList.contains("bad"), icon: ic?.dataset.icon || null, before: !!ic && !!(ic.compareDocumentPosition(said) & Node.DOCUMENT_POSITION_FOLLOWING),
              w: ic ? box(ic).width : 0, level: ic ? Math.abs((box(ic).top + box(ic).bottom) / 2 - (box(said).top + box(said).bottom) / 2) : 0 };
          }));
          assert.deepEqual(reqs.map((r) => r.icon), ["codex-color", "deepseek-color", null], "Routing: each answered row has its provider's logo");
          for (const r of reqs.filter((r) => r.icon)) {
            assert(r.before, "Routing: the logo comes before the name");
            assert(Math.abs(r.w - 14) <= 0.5 && r.level <= 1.5, `Routing: a 14px logo level with the name: ${JSON.stringify(r)}`);
          }
          assert.equal(await p.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), true, "Routing doesn't scroll sideways");

          // Usage → Requests
          await p.unroute("**/*");
          await p.route("**/*", usageServer(lang, "light"));
          await p.goto("http://magpie.test/?view=usage&tab=requests");
          await p.locator("table.led tbody tr.led-row").nth(2).waitFor();
          // the table drawn and laid out (a first draw can be swapped for the next)
          await p.waitForFunction(() => [...document.querySelectorAll("table.led td.where .ic")].some((e) => e.getBoundingClientRect().width > 0));
          const cells = await p.locator("table.led tbody tr.led-row td.where").evaluateAll((tds) => tds.map((td) => {
            const ic = td.querySelector(".where-line > .ic"), name = td.querySelector(".where-name");
            const box = (n) => n.getBoundingClientRect();
            return { icon: ic?.dataset.icon || null, name: name.textContent, before: !!ic && ic.nextElementSibling === name,
              w: ic ? box(ic).width : 0, inside: box(name).right <= box(td).right + 1, level: ic ? Math.abs((box(ic).top + box(ic).bottom) / 2 - (box(name).top + box(name).bottom) / 2) : 0 };
          }));
          assert.deepEqual(cells.map((c) => c.icon), ["openai", "zhipu-color", null], "Usage: each provider's logo, none for a session file's row");
          assert.equal(cells[0].name.startsWith("OpenAI"), true, "Usage: the name's text is the name alone");
          for (const c of cells.filter((c) => c.icon)) assert(c.before && Math.abs(c.w - 14) <= 0.5 && c.level <= 1.5 && c.inside, `Usage: a 14px logo before the name, level, inside the cell: ${JSON.stringify(c)}`);
          assert.equal(await p.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), true, "Usage doesn't scroll sideways");
          assert.deepEqual(errors, []);
        });
      }
    }
  });
}
