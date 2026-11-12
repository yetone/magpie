// Run with Node's test runner and Playwright on the module path; see README.md.
// Where Codex compacts is one value (7eleven on Discord): the Usage page's
// Tuned for you card and Long conversations in Codex's settings on the
// Agents page. The card says it is Long conversations, shared by Codex,
// Claude Code and Grok Build, and keeps saying so after Use, with Undo;
// after Use, the Agents page's sheet shows the window Use put in, with no
// reload, in its one Long conversations row (the owner, 11-12: not a row for
// every agent above one for Codex). A window typed there is Codex's own, the
// one the card shows as Now when the Usage page comes back, and the card
// then says it is Codex's. Chromium and WebKit, in every language; the API is faked here and
// keeps the one value as the gateway does (settings.CompactAt).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, agentRow, openAgentSettings } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const curve = (lo, hi, best) => {
  const pts = [];
  for (let v = lo; v <= hi; v += 10000) pts.push({ value: v, cost: 1e9 + Math.abs(v - best) * 3000, compacts: Math.round(9e6 / v) });
  return pts;
};
const facts = { calls: 900, streams: 20, compacts: 40, peak: 380000, after: 57000, growth: 4000, rework: 4700 };

function serve(lang, posts) {
  let settings = { theme: "light", lang, tray: "panel", currency: "usd", version: "0.1.900", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425", proxyNow: "none", proxySource: "none", visionModels: [], imageGenModels: [], titleModels: [], workbuddyCheckins: [], lanURLs: [], redactWords: [], trayUsageEvery: 3, compactAt: 0, fullContext: false };
  let ours = 0; // the window tune put in, as the gateway's stash keeps it
  // Codex's own Long conversations comes first (settings.ForAgent)
  const window = () => {
    const own = (settings.agentCompacts || {}).codex;
    if (own) return own.full ? 0 : own.at || 272000;
    return settings.fullContext ? 0 : settings.compactAt || 272000;
  };
  const tune = () => {
    const now = window();
    return {
      days: 7,
      agents: [{ agent: "codex", calls: 900, advice: [{
        knob: "compact",
        setting: { value: now ? String(now) : "", source: "long", ours: ours > 0 && ours === now },
        compact: { current: now || 1000000, best: 400000, cost: 1.9e9, low: 1.5e9, window: 1000000, curve: curve(200000, 600000, 400000), facts },
      }] }],
    };
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [agentRow("codex", "Codex")], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/tune") return json(tune());
    if (url.pathname === "/api/tune/apply" || url.pathname === "/api/tune/undo") {
      const b = req.postDataJSON();
      posts.push({ path: url.pathname, body: b });
      if (url.pathname.endsWith("apply")) {
        ours = +b.value;
        // a Use puts the advice where Codex reads it (tune.longKey)
        settings = (settings.agentCompacts || {}).codex
          ? { ...settings, agentCompacts: { ...settings.agentCompacts, codex: { at: ours } } }
          : { ...settings, compactAt: ours, fullContext: false };
      }
      return json({});
    }
    if (url.pathname === "/api/settings/full-context") {
      const body = req.postDataJSON();
      posts.push({ path: url.pathname, body });
      settings = { ...settings, fullContext: body.on, ...(body.at !== undefined ? { compactAt: body.at === 272000 ? 0 : body.at } : {}) };
      return json(settings);
    }
    if (url.pathname === "/api/settings/agent-compact") {
      const body = req.postDataJSON();
      posts.push({ path: url.pathname, body });
      const ac = { ...(settings.agentCompacts || {}) };
      if (body.own) ac[body.agent] = { ...(body.full ? { full: true } : {}), ...(body.at && body.at !== 272000 ? { at: body.at } : {}) };
      else delete ac[body.agent];
      settings = { ...settings, agentCompacts: ac };
      return json(settings);
    }
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/context") return json({ days: 7, agents: [], sessions: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    try {
      await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
    } catch {
      await route.fulfill({ status: 404, body: "" });
    }
  };
}

const words = {
  en: { long: "Long conversations, shared by Codex, Claude Code and Grok Build", own: "Long conversations in Codex", undo: "Undo" },
  zh: { long: "「长对话」，Codex、Claude Code 和 Grok Build 共用", own: "Codex 的长对话", undo: "撤销" },
  "zh-TW": { long: "「長對話」，Codex、Claude Code 與 Grok Build 共用", own: "Codex 的長對話", undo: "復原" },
  ja: { long: "「長い会話」。Codex、Claude Code、Grok Build で共通", own: "Codex の長い会話", undo: "取り消す" },
  de: { long: "„Lange Unterhaltungen“, die Codex, Claude Code und Grok Build teilen", own: "Lange Unterhaltungen in Codex", undo: "Rückgängig" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: Tuned for you and Long conversations are one window`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const [lang, w] of Object.entries(words)) {
      await t.test(lang, async () => {
        const ctx = await browser.newContext({ viewport: { width: 1100, height: 760 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.usageTab", "context"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await watchSettingsReads(page);
        await page.goto("http://magpie.test/?view=usage");
        const row = page.locator(".ctx-tune-row").first();
        await row.waitFor();
        // the card says where its window is kept
        assert.equal(await row.locator(".ctx-tune-src").innerText(), w.long);
        await row.locator("button.primary").click();
        // put in by magpie: Undo, and still the one value
        await row.getByRole("button", { name: w.undo, exact: true }).waitFor();
        assert.equal(await row.locator(".ctx-tune-src").innerText(), w.long);
        assert.deepEqual(posts.map((p) => [p.path, p.body.agent, p.body.value]), [["/api/tune/apply", "codex", "400000"]]);

        // the Agents page's sheet shows the window Use put in, in one row
        await page.locator('#nav button[data-view="agents"]').click();
        await openAgentSettings(page, "codex");
        const num = page.locator(".agent-compact input.compact-num");
        await num.waitFor();
        assert.equal(await num.inputValue(), "400k", "Long conversations after Use on the Usage page");
        assert.equal(await page.locator("#fullContextRow").filter({ visible: true }).count(), 0, "every agent's row stands beside Codex's own");

        // a window typed there is Codex's own, the card's Now when Usage is back
        await num.fill("300k");
        await num.press("Enter");
        await page.waitForFunction(() => document.querySelector(".agent-compact input.compact-num")?.value === "300k");
        assert.deepEqual(posts.at(-1), { path: "/api/settings/agent-compact", body: { agent: "codex", own: true, full: false, at: 300000 } });
        await page.locator('#nav button[data-view="usage"]').click();
        await page.locator(".ctx-tune-row .ctx-tune-val.now b").filter({ hasText: "300K" }).waitFor();
        // no longer the window tune put in: no Undo, and the card says whose it is
        assert.equal(await page.locator(".ctx-tune-row").first().getByRole("button", { name: w.undo, exact: true }).count(), 0);
        assert.equal(await page.locator(".ctx-tune-row .ctx-tune-src").first().innerText(), w.own);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
