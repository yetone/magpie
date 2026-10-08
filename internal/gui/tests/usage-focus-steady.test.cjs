// Run with Node's test runner and Playwright on the module path; see README.md.
// The window focused again, or shown again, reads what the page shows anew,
// and must not blank or rebuild a chart that didn't change. Under a window
// manager whose focus follows the mouse (Hyprland on omarchy; Zhenzhen on
// Discord) the pointer crossing from a terminal into magpie focuses it every
// time, and the Usage page's bars flashed: the Overview put its skeleton up,
// hid the chart and drew it again from nothing. Now the Overview, the
// Requests tab and the tray panel's Usage tab keep their bars (the same
// elements, never hidden, no skeleton, the panel not dimmed) when nothing
// changed, and a change is drawn in place, still with no skeleton between.
// The Routing page's groups, read again on focus, aren't drawn again either.
// English and Chinese; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const midnight = new Date();
midnight.setHours(0, 0, 0, 0);
const DAYS = 7;

// the Overview's answer; v moves the newest day's tokens, as a request does
function overview(v) {
  const series = Array.from({ length: DAYS }, (_, i) => {
    const tokens = (i + 1) * 1000 + (i === DAYS - 1 ? v * 5000 : 0);
    return { label: "D" + i, time: new Date(midnight.getTime() - (DAYS - 1 - i) * 864e5).toISOString(), calls: i + 1, errors: 0, input: tokens * 0.8, output: tokens * 0.2, cost: 0.01 * (i + 1) };
  });
  const input = series.reduce((a, p) => a + p.input, 0), output = series.reduce((a, p) => a + p.output, 0);
  const calls = series.reduce((a, p) => a + p.calls, 0);
  return {
    calls, errors: 0, input, output, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 0, cost: 0.28, bucket: "day", series,
    agents: [{ name: "Codex", icon: "codex-color", calls, errors: 0, input, output, cache_read: 0, cost: 0.28 }],
    models: [{ name: "gpt-6-sol", calls, errors: 0, input, output, cache_read: 0, cost: 0.28 }],
    path: "~/.config/magpie/usage.jsonl",
  };
}

// the Requests tab's (and the panel's) answer: a page of rows and the chart
const share = (calls, tokens, cost) => ({ id: "relay", name: "Relay", icon: "generic", calls, errors: 0, input: tokens * 0.8, output: tokens * 0.2, cache_read: 0, cache_write: 0, cost });
function requests() {
  const series = Array.from({ length: DAYS }, (_, i) => {
    const tokens = (i + 1) * 1000;
    return { label: String(i), time: new Date(midnight.getTime() - (DAYS - 1 - i) * 864e5).toISOString(), calls: i + 1, errors: 0, input: tokens * 0.8, output: tokens * 0.2, cache_read: 0, cache_write: 0, cost: 0.01 * (i + 1),
      by: { provider: { relay: { calls: i + 1, tokens, cost: 0.01 * (i + 1) } }, agent: {}, model: { "gpt-6-sol": { calls: i + 1, tokens, cost: 0.01 * (i + 1) } } } };
  });
  return {
    period: "7d", offset: 0, total: 1, calls: 28, errors: 0, input: 22400, output: 5600, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0.28, unpriced: 0, bucket: "day", series,
    rows: [{ t: new Date(midnight.getTime() + 3600e3).toISOString(), agent: "codex", agentName: "Codex", icon: "codex-color", provider: "relay", providerName: "Relay", req: "sol", model: "gpt-6-sol", in: 100, out: 10, ms: 900, status: 200, cost: 0.01, priced: true }],
    by: { provider: [share(28, 28000, 0.28)], agent: [], model: [{ ...share(28, 28000, 0.28), id: "gpt-6-sol", name: "gpt-6-sol" }] },
    agents: [{ id: "codex", name: "Codex", icon: "codex-color" }], providers: [{ id: "relay", name: "Relay", icon: "generic" }],
  };
}

// a routing group, for the Routing page's list
const GROUPS = { models: [{ id: "rl/glm-5", provider: "rl", providerName: "Relay", model: "glm-5", name: "GLM-5" }], pools: [],
  groups: [{ id: "sol", name: "Sol", members: ["rl/glm-5"], routing: "order", ready: true, memberInfo: [{ id: "rl/glm-5", ready: true }] }] };

function serve(lang, calls, cur) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") { calls.state++; return json({ agents: [], profiles: [], settings: { lang, theme: "light" } }); }
    if (url.pathname === "/api/usage") { calls.overview++; return json(overview(cur.v)); }
    if (url.pathname === "/api/usage/requests") { calls.requests++; return json(requests()); }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/gateway/trace") return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") { calls.groups = (calls.groups || 0) + 1; return json(GROUPS); }
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// watch: from now on, note every blanking of the chart — bars or columns
// taken off the page, the chart hidden, a skeleton or the dimming put up —
// and tag the bars drawn now, to tell them from new ones
function watch(p, bars) {
  return p.evaluate((sel) => {
    window.__flash = [];
    let n = 0;
    for (const b of document.querySelectorAll(sel)) b.__keep = ++n;
    const note = (what) => window.__flash.push(what);
    const hit = (node) => node.nodeType === 1 && (node.matches(sel) || node.querySelector(sel));
    new MutationObserver((ms) => {
      for (const m of ms) {
        if (m.type === "childList") for (const r of m.removedNodes) if (hit(r)) { note("bars removed"); break; }
        if (m.type === "attributes" && m.attributeName === "hidden" && m.target.id === "chart" && m.target.hidden) note("chart hidden");
        if (m.type === "attributes" && m.attributeName === "class" && m.target.classList.contains("loading")) note("skeleton");
        if (m.type === "attributes" && m.attributeName === "class" && m.target.classList.contains("pu-loading")) note("dimmed");
      }
    }).observe(document.body, { subtree: true, childList: true, attributes: true, attributeFilter: ["hidden", "class"] });
  }, bars);
}
const kept = (p, bars) => p.evaluate((sel) => [...document.querySelectorAll(sel)].map((b) => b.__keep || 0), bars);
const flashes = (p) => p.evaluate(() => window.__flash);

// back: the pointer crossing into the window (focus), and the window shown
// again (visibilitychange); then what that asked for is let in
async function back(p, calls, key) {
  const before = calls[key];
  await p.evaluate(() => { window.dispatchEvent(new Event("focus")); document.dispatchEvent(new Event("visibilitychange")); });
  for (let i = 0; i < 100 && calls[key] === before; i++) await p.waitForTimeout(20);
  assert(calls[key] > before, `coming back read ${key} again`);
  await p.waitForTimeout(300);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: focusing the window keeps the Usage charts and the Routing groups`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const errors = [];
      const open = async (url, viewport, calls, cur) => {
        const p = await (await browser.newContext({ viewport, reducedMotion: "no-preference" })).newPage();
        p.setDefaultTimeout(5000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", serve(lang, calls, cur));
        await p.goto(url);
        return p;
      };

      // the window, on the Overview
      const calls = { state: 0, overview: 0, requests: 0 }, cur = { v: 0 };
      const p = await open("http://magpie.test/?view=usage", { width: 1180, height: 760 }, calls, cur);
      if (await p.locator("#usageTab .opt.on").evaluate((b) => b !== b.parentElement.firstElementChild)) await p.locator("#usageTab .opt").first().click();
      const BAR = "#chart .bars .bar";
      await p.locator(BAR).first().waitFor();
      assert.equal(await p.locator(BAR).count(), DAYS);
      await watch(p, BAR);
      await back(p, calls, "overview");
      assert.deepEqual(await flashes(p), [], "nothing changed: the chart isn't blanked");
      assert.deepEqual(await kept(p, BAR), [1, 2, 3, 4, 5, 6, 7], "the same bars stay");

      // a request came meanwhile: the newest day grows past the rest, which the
      // first bar, measured against it, shows; with no skeleton between
      const tall = () => p.locator(BAR).first().evaluate((b) => b.querySelector("i.in").style.height);
      const was = await tall();
      cur.v = 1;
      await back(p, calls, "overview");
      assert.notEqual(await tall(), was, "the change is drawn");
      const seen = await flashes(p);
      assert(!seen.includes("skeleton") && !seen.includes("chart hidden"), "no skeleton over the change: " + seen.join(", "));
      assert.equal(await p.locator("#chart").isVisible(), true);

      // the Requests tab: its columns stay
      await p.locator("#usageTab .opt").nth(1).click();
      const COL = "#ledgerPane .led-chart rect.col";
      await p.locator(COL).first().waitFor();
      await p.waitForTimeout(300);
      await watch(p, COL);
      const cols = await p.locator(COL).count();
      await back(p, calls, "requests");
      assert.deepEqual(await flashes(p), [], "the Requests chart isn't blanked");
      assert.deepEqual(await kept(p, COL), Array.from({ length: cols }, (_, i) => i + 1), "the same columns stay");
      await p.locator("#usageTab .opt").first().click(); // the tab is remembered: leave it on the Overview

      // the tray panel's Usage tab: not dimmed, not drawn again
      const pc = { state: 0, overview: 0, requests: 0 };
      const panel = await open("http://magpie.test/?mode=panel", { width: 440, height: 560 }, pc, { v: 0 });
      await panel.locator('[data-ptab="stats"]').click();
      const PCOL = "#panelUsage .led-chart rect.col";
      await panel.locator(PCOL).first().waitFor();
      await panel.waitForTimeout(2300); // the panel reads again on focus once 2 s have passed
      await watch(panel, PCOL);
      const pcols = await panel.locator(PCOL).count();
      await back(panel, pc, "requests");
      assert.deepEqual(await flashes(panel), [], "the panel's chart isn't dimmed or blanked");
      assert.deepEqual(await kept(panel, PCOL), Array.from({ length: pcols }, (_, i) => i + 1), "the same columns stay");

      // the Routing page's groups: the same cards stay
      await p.locator('[data-view="routing"]').first().click();
      const GRP = "#view-routing .rt-group";
      await p.locator(GRP).first().waitFor();
      await p.waitForTimeout(300);
      await watch(p, GRP);
      await back(p, calls, "groups");
      assert.deepEqual(await kept(p, GRP), [1], "the same group card stays");

      assert.deepEqual(errors, []);
    });
  }
}
