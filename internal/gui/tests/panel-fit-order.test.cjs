// Run with Node's test runner and Playwright on the module path; see README.md.
// The tray panel keeps the height it asked for last (robot_yz on X, on
// v0.1.3005: the panel had become short, and the Allowances tab's one card,
// Zhipu GLM with its 5h, 7d and MCP rings and its curve, was cut off by the
// foot). Wails serves each of the page's requests on a goroutine of its own,
// so two window/fit requests on their way together reach the panel in either
// order; in a real WKWebView 69 of 400 pairs sent together landed the wrong
// way round. The fake here does what Wails can: the panel's first fit is
// answered, and so applied, only after the page has drawn the card and
// asked for whatever else it would. The panel must end at the latest
// height asked, with the card whole. Chromium and WebKit, every language;
// no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_PANEL_FIT_ORDER_ASSETS || path.join(__dirname, "../assets"));
const H = 3600e3;
const iso = (ms) => new Date(ms).toISOString();

// the reporter's allowance: Zhipu GLM's Max plan, renewed, its 5-hour and
// weekly windows and the MCP month aside (planquota.go's names), and the
// 5-hour window's readings this cycle for the curve
function zhipu(now) {
  const s5 = now - 2 * H, r5 = now + 3 * H;
  const quota = {
    provider: "zhipu", name: "Zhipu GLM", icon: "zhipu-color", plan: "max", until: iso(now + 99 * 24 * H), renew: "auto",
    windows: [
      { name: "5 hours", used: 7, span: 5 * H * 1e6, startsAt: iso(s5), resetsAt: iso(r5) },
      { name: "7 days", used: 18, span: 7 * 24 * H * 1e6, startsAt: iso(now - 2 * 24 * H), resetsAt: iso(now + 5 * 24 * H) },
      { name: "MCP · Month", used: 1, aside: true, resetsAt: iso(now + 20 * 24 * H) },
    ],
  };
  const points = [];
  for (let m = 0; m <= 120; m += 10) points.push({ at: iso(s5 + m * 60e3), left: 100 - (7 * m) / 120, start: iso(s5), resetsAt: iso(r5) });
  return { quotas: [quota], history: [{ provider: "zhipu", user: "", lines: [{ name: "5 hours", points }] }] };
}

// fifteen agents (the reporter's tab says 15), two of them set up
function serve(page, lang, panel) {
  const data = zhipu(Date.now());
  const ids = Array.from({ length: 15 }, (_, i) => "agent" + i);
  const state = {
    agents: ids.map((id, i) => ({ id, name: id, path: "/test/" + id, fields: [], wired: i < 2, added: i < 2 })),
    profiles: [], settings: { lang, theme: "light", tray: "panel", guideHidden: true },
  };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (body) => route.fulfill({ json: body });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light"};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/window/fit") return panel.fit(route, Number(url.searchParams.get("h")));
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings") return json(state.settings);
    if (url.pathname === "/api/usage/quotas") return json(data.quotas);
    if (url.pathname === "/api/usage/quotas/history") return json(data.history);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: iso(Date.now()), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// the panel: it takes a height when its fit request is served, no shorter
// than panelMin (textsize.go), and its first fit is served only on release
function trayPanel(page) {
  const asked = [], applied = [];
  let release, first = true;
  const held = new Promise((r) => { release = r; });
  const take = async (route, h) => {
    applied.push(h);
    await page.setViewportSize({ width: 440, height: Math.min(560, Math.max(220, h)) }).catch(() => {});
    await route.fulfill({ status: 204 }).catch(() => {});
  };
  return {
    asked, applied, release: () => release(),
    async fit(route, h) {
      asked.push(h);
      if (first) { first = false; await held; }
      return take(route, h);
    },
  };
}

// no new height asked for, or served, for a while
async function quiet(page, panel) {
  let n = -1;
  while (n !== panel.asked.length + panel.applied.length) {
    n = panel.asked.length + panel.applied.length;
    await page.waitForTimeout(400);
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: the panel keeps the latest height asked, and the Zhipu GLM card shows whole`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 440, height: 220 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(10000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.addInitScript(() => { try { localStorage.setItem("magpie.panelTab", "usage"); } catch {} });
      const panel = trayPanel(page);
      await page.route("**/*", serve(page, lang, panel));
      await page.goto("http://magpie.test/?mode=panel");

      // the card drawn with its rings and curve, and the page done asking
      // while its first fit is still on its way
      await page.locator("#panelQuota .pq-card .pq-spark").waitFor();
      assert.equal(await page.locator("#panelQuota .pq-card").count(), 1, "one card, Zhipu GLM's");
      await page.locator("#agents .row.agent").first().waitFor({ state: "attached" });
      await quiet(page, panel);
      panel.release();
      await quiet(page, panel);

      const last = panel.asked[panel.asked.length - 1];
      assert.equal(panel.applied[panel.applied.length - 1], last,
        `the panel ended at an older height than the page asked for last (asked ${panel.asked.join(" ")}, applied ${panel.applied.join(" ")})`);
      const seen = await page.evaluate(() => {
        const v = document.querySelector("#view-agents"), card = document.querySelector("#panelQuota .pq-card");
        return { over: v.scrollHeight - v.clientHeight, cardBottom: card.getBoundingClientRect().bottom, viewBottom: v.getBoundingClientRect().bottom, tab: document.body.dataset.ptab };
      });
      assert.equal(seen.tab, "usage");
      assert(seen.cardBottom <= seen.viewBottom + 0.5,
        `the Zhipu GLM card is cut off: its bottom at ${seen.cardBottom}, the view's at ${seen.viewBottom} (asked ${panel.asked.join(" ")}, applied ${panel.applied.join(" ")})`);
      assert(seen.over <= 0, `the Allowances tab overflows the panel by ${seen.over}px`);
      assert.deepEqual(errors, []);
    });
  }
}
