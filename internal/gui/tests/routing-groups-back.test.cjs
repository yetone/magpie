// Run with Node's test runner and Playwright on the module path; see README.md.
// Routing groups opened from Routing lead back to it (unscientific9 on
// Discord, 11-08: a request's group opened the Routing groups page, and
// nothing on it went back). Since the groups got a page of their own
// (80ae904c2) a request's group, and Routing's own "Routing groups ›" where
// the header has no tab for them, open that page; its toolbar now starts
// with "‹ Routing", as a Mac app's back button, and Escape does the same
// while nothing on the page takes it (the filter, an open editor). Back on
// Routing, the request's group has the keyboard again. Beside the sidebar,
// the page opened from its own tab is a page of its own and has none.
// Every language, Chromium and WebKit, wide, a narrow window and a phone's
// width; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ROUTING_ASSETS || path.join(__dirname, "../assets"));
const models = [
  { id: "a/m", name: "m", providerName: "A", icon: "generic" },
  { id: "b/m", name: "m", providerName: "B", icon: "generic" },
  { id: "a/n", name: "n", providerName: "A", icon: "generic" },
];
const info = (ids) => ids.map((id) => ({ id, ready: true }));
const groups = [
  { id: "auto-m", name: "Model M", members: ["a/m", "b/m"], auto: true, ready: true, memberInfo: info(["a/m", "b/m"]) },
  { id: "mine", name: "Mine", members: ["a/n", "b/m"], ready: true, memberInfo: info(["a/n", "b/m"]) },
];
const routing = { en: "Routing", zh: "路由", "zh-TW": "路由", ja: "ルーティング", de: "Weiterleitung" };

const now = Date.now();
const routes = Array.from({ length: 30 }, (_, i) => ({
  id: "r" + i, seq: i + 1, at: new Date(now - i * 60000).toISOString(), agent: "claude", asked: "group/mine",
  model: "a/n", provider: "a", status: 200, ms: 900, ttft: 300, in: 100, out: 20, done: true,
  group: { id: "mine", name: "Mine" },
}));

function serve(lang, web) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: routes.length, totals: { requests: routes.length, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models, pools: [], deciders: [], found: true, groups });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

// the page's boot under the whole suite's load (#3478)
const boot = 15000;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(routing)) {
    for (const { width, web } of [{ width: 1280, web: false }, { width: 640, web: false }, { width: 420, web: true }]) {
      test(`${engine} ${lang} ${width}px${web ? " web" : " window"}: Routing groups opened from Routing go back to it`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}${web ? "-web" : ""}-routing-groups-back.png`) });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, web));
        await page.goto("http://magpie.test/?view=routing");
        await page.locator(".rt-reqs > *").first().waitFor({ state: "attached", timeout: boot });
        await page.locator(".rt-group", { hasText: "Mine" }).waitFor({ state: "attached", timeout: boot });
        const routingView = page.locator("#view-routing"), groupsView = page.locator("#view-groups");
        const back = groupsView.locator(".rt-gback");
        const onRouting = async (why) => {
          await routingView.waitFor({ state: "visible" });
          assert.equal(await groupsView.isHidden(), true, why);
          const on = await page.evaluate(() => [...document.querySelectorAll("#nav button[data-view].on")].map((b) => b.dataset.view));
          assert.deepEqual(on, ["routing"], "Routing's tab is lit again");
        };

        // a request's group opens the groups page on its row
        const chip = routingView.locator('.rt-go[data-go="group:mine"]').first();
        await chip.waitFor({ timeout: boot });
        await chip.click();
        await groupsView.waitFor();
        await groupsView.locator('.rt-group[data-id="mine"]').waitFor();

        // its toolbar starts with the way back, named for Routing
        await back.waitFor({ state: "visible" });
        assert.equal((await back.textContent()).replace("‹", "").trim(), routing[lang]);
        const head = groupsView.locator(".rt-gsec > .row-head").first();
        assert.equal(await head.evaluate((h, b) => [...h.children].find((c) => c.getClientRects().length) === b, await back.elementHandle()), true, "first in the toolbar");
        const bb = await back.boundingBox();
        assert(bb && bb.x >= 0 && bb.x + bb.width <= width && bb.y >= 0 && bb.y + bb.height <= 700, "in sight");
        assert.equal(await back.evaluate((e) => { const c = getComputedStyle(e); return c.borderLeftWidth !== c.borderRightWidth || c.borderLeftColor !== c.borderRightColor; }), false, "no left-border accent");
        const headOver = await head.evaluate((h) => h.scrollWidth - h.clientWidth);
        assert(headOver <= 0, `nothing overflows the toolbar (${headOver}px)`);
        const over = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
        assert(over <= 0, `no sideways scroll (${over}px)`);

        // it goes back to Routing, the request's group with the keyboard
        await back.click();
        await onRouting("back on Routing");
        assert.equal(await page.evaluate(() => !!document.activeElement?.matches('#view-routing .rt-go[data-go="group:mine"]')), true, "the request's group has the keyboard");

        // Escape goes back too, but not out of the filter or an open editor
        await chip.click();
        await back.waitFor({ state: "visible" });
        const filter = groupsView.locator(".rt-gfilter");
        await filter.focus();
        await page.keyboard.press("Escape");
        assert.equal(await groupsView.isVisible(), true, "Escape in the filter stays");
        await groupsView.locator(".rt-gnew").click();
        await groupsView.locator(".rt-gedit").first().waitFor();
        await groupsView.locator(".rt-gedit button").first().focus();
        await page.keyboard.press("Escape");
        assert.equal(await groupsView.isVisible(), true, "Escape with an editor open stays");
        await groupsView.locator(".rt-gedit").first().evaluate(() => window.magpie.call("routing", "discard"));
        await groupsView.locator(".rt-gedit").waitFor({ state: "detached" });
        await page.evaluate(() => document.activeElement?.blur());
        await page.keyboard.press("Escape");
        await onRouting("Escape goes back");

        const side = await page.evaluate(() => document.body.classList.contains("side-nav"));
        if (side) {
          // beside the sidebar, its own tab opens a page of its own: no way
          // back, and Escape leaves it where it is
          await page.locator('#nav button[data-view="groups"]').click();
          await groupsView.waitFor();
          await groupsView.locator('.rt-group[data-id="mine"]').waitFor();
          assert.equal(await back.isVisible(), false, "no back from the sidebar's own tab");
          await page.keyboard.press("Escape");
          assert.equal(await groupsView.isVisible(), true, "Escape stays");
        } else {
          // the header's row has no tab for them: Routing's head opens them,
          // and they lead back
          await page.locator("#rtToGroups").click();
          await back.waitFor({ state: "visible" });
          await back.click();
          await onRouting("back from Routing's own way in");
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
