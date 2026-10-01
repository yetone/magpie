// Run with Node's test runner and Playwright on the module path; see README.md.
// The page `magpie web` serves to a browser (Jorben on Discord: the remote
// web UI had no request archive switch and no usage chart icons, and
// /wails/runtime.js was a 404 in DevTools). In a browser there is no Wails
// runtime, so the page never asks for /wails/runtime.js; the app's window
// still does, for its close and maximise. Neither the Gateway page's request
// archive switch nor the Usage page's chart and its ranking's icons wait on
// it: in magpie web the switch is there and posts settings/archive, and the
// chart draws its columns with each provider's icon beside it. In English and
// Chinese, Chromium and WebKit, with the API faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const midnight = new Date();
midnight.setHours(0, 0, 0, 0);

const WHO = [
  { id: "anthropic", name: "Claude", icon: "claudecode-color", agent: "claude", model: "claude-sonnet-5", tokens: 900000, cost: 0.9 },
  { id: "codex", name: "Codex", icon: "codex-color", agent: "codex", model: "gpt-6-luna", tokens: 400000, cost: 0.4 },
];
const share = (id, name, icon, tokens, cost) => ({ id, name, icon, calls: 2, errors: 0, input: tokens * 0.5, output: tokens * 0.5, cache_write: 0, cache_read: 0, cost });
const SERIES = Array.from({ length: 24 }, (_, h) => {
  const on = h >= 9 && h <= 18;
  const by = { provider: {}, agent: {}, model: {} };
  for (const w of on ? WHO : []) {
    by.provider[w.id] = { calls: 2, tokens: w.tokens, cost: w.cost };
    by.agent[w.agent] = { calls: 2, tokens: w.tokens, cost: w.cost };
    by.model[w.model] = { calls: 2, tokens: w.tokens, cost: w.cost };
  }
  const tokens = on ? WHO.reduce((a, w) => a + w.tokens, 0) : 0;
  return { label: String(h).padStart(2, "0"), time: new Date(midnight.getTime() + h * 3600e3).toISOString(), calls: on ? 4 : 0, errors: 0, input: tokens / 2, output: tokens / 2, cache_write: 0, cache_read: 0, cost: on ? 1.3 : 0, by };
});
const LEDGER = {
  period: "today", rows: [{ t: new Date().toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color", provider: "anthropic", providerName: "Claude", model: "claude-sonnet-5", req: "sonnet", in: 2, out: 600, cache_write: 0, cache_read: 0, ms: 2380, status: 200, cost: 0.087, priced: true }],
  offset: 0, total: 1, calls: 40, errors: 0, input: 6.5e6, output: 6.5e6, cache_write: 0, cache_read: 0, reasoning: 0, cost: 13, unpriced: 0, bucket: "hour", series: SERIES,
  by: {
    provider: WHO.map((w) => share(w.id, w.name, w.icon, w.tokens * 10, w.cost * 10)),
    agent: WHO.map((w) => share(w.agent, w.agent, w.icon, w.tokens * 10, w.cost * 10)),
    model: WHO.map((w) => share(w.model, w.model, "", w.tokens * 10, w.cost * 10)),
  },
  agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }, { id: "codex", name: "Codex", icon: "codex-color" }],
  providers: WHO.map((w) => ({ id: w.id, name: w.name, icon: w.icon })),
};

function serve(lang, web, seen) {
  const gateway = { running: true, window: true, mine: true, url: "http://127.0.0.1:3999", groups: [], calls: [], archive: { on: false, bucket: "bkt/team on https://s3.example.com" } };
  const providers = { providers: [], presets: [], excluded: [], gateway };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    seen.paths.push(url.pathname);
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    // magpie web serves no Wails runtime: a 404, as it answers
    if (url.pathname === "/wails/runtime.js") return web ? route.fulfill({ status: 404, contentType: "text/plain", body: "404 page not found\n" }) : route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/settings/archive") {
      const body = route.request().postDataJSON();
      seen.archive.push(body);
      gateway.archive = { ...gateway.archive, on: body.on };
      return json(gateway.archive);
    }
    if (url.pathname === "/api/usage/requests") return json(LEDGER);
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 1, errors: 0, input: 1, output: 1, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 0, cost: 1, bucket: "day", series: [], agents: [], models: [], path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try {
      await route.fulfill({ body: await fs.readFile(file), contentType });
    } catch {
      await route.fulfill({ status: 404, body: "" });
    }
  };
}

const words = {
  en: { name: "Request archive", off: "Off", on: "On", provider: "Provider" },
  zh: { name: "请求存档", off: "关闭", on: "开启", provider: "供应商" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: magpie web asks for no Wails runtime, and has the archive switch and the usage chart's icons`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const open = async (web) => {
        const page = await (await browser.newContext({ viewport: { width: 1180, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [], failed = [];
        page.on("pageerror", (e) => errors.push(e.message));
        page.on("response", (r) => r.status() >= 400 && failed.push(new URL(r.url()).pathname));
        const seen = { paths: [], archive: [] };
        await page.route("**/*", serve(lang, web, seen));
        await page.goto("http://magpie.test/?view=gateway");
        return { page, errors, failed, seen };
      };

      const { page, errors, failed, seen } = await open(true);
      // the request archive's switch, over the recent calls
      const row = page.locator("#archiveList .row.pref");
      await row.waitFor();
      assert.equal(await row.locator(".name").textContent(), w.name);
      assert.deepEqual(await row.locator(".segs .opt").allTextContents(), [w.off, w.on]);
      const y = () => page.locator("#view-gateway").evaluate((v) => v.scrollTop);
      await row.scrollIntoViewIfNeeded();
      const before = await y();
      await row.locator(".segs .opt").nth(1).click();
      for (let i = 0; i < 50 && !seen.archive.length; i++) await page.waitForTimeout(40);
      assert.deepEqual(seen.archive, [{ on: true }]);
      await page.locator("#archiveList .segs .opt.on").filter({ hasText: w.on }).waitFor();
      assert.equal(await y(), before, "the click moved the page");

      // the Usage page's chart, its ranking by provider with their icons
      await page.locator('[data-view="usage"]').first().click();
      await page.locator("#usageTab .opt").nth(1).click();
      await page.locator("#ledRank .rk").first().waitFor();
      await page.locator("#ledSplit .opt").filter({ hasText: w.provider }).click();
      assert((await page.locator("#ledChart rect.col").count()) > 0, "the chart drew no columns");
      const icons = page.locator("#ledRank .rk .ic img");
      await page.waitForFunction(() => {
        const imgs = [...document.querySelectorAll("#ledRank .rk .ic img")];
        return imgs.length >= 2 && imgs.every((i) => i.complete && i.naturalWidth > 0);
      });
      assert.deepEqual((await icons.evaluateAll((is) => is.map((i) => new URL(i.src).pathname))).sort(), ["/icons/claudecode-color.svg", "/icons/codex-color.svg"]);

      // and never the desktop's runtime, so nothing failed to load
      assert(!seen.paths.includes("/wails/runtime.js"), "magpie web asked for /wails/runtime.js");
      assert.deepEqual(failed, []);
      assert.deepEqual(errors, []);

      // the app's window still takes its runtime, for close and maximise
      const app = await open(false);
      await app.page.locator("#archiveList .row.pref").waitFor();
      for (let i = 0; i < 50 && !app.seen.paths.includes("/wails/runtime.js"); i++) await app.page.waitForTimeout(40);
      assert(app.seen.paths.includes("/wails/runtime.js"), "the app's window no longer loads its runtime");
      assert.deepEqual(app.errors, []);
    });
  }
}
