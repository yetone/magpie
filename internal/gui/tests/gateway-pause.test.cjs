// Run with Node's test runner and Playwright on the module path; see README.md.
// Pausing the gateway (me2seeks on X: the gateway could be stopped only by
// quitting magpie with Disconnect and quit). Settings → General has a
// Gateway row, On or Paused: Paused asks the backend to pause (agents
// disconnected, the listener stopped), and the row reads Paused once it
// says so. An agent that couldn't be disconnected is named in the status
// line. The Gateway page's card then reads paused, with Resume gateway,
// which asks to resume and leaves it running. In every language, both
// engines, wide and narrow; what is clicked stays put on screen, nothing runs off
// it sideways. The API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

const words = {
  en: { on: "On", paused: "Paused", state: "paused", resume: "Resume gateway", restart: "Restart gateway", failed: "Codex couldn't be disconnected: config is read-only" },
  zh: { on: "开启", paused: "已暂停", state: "已暂停", resume: "恢复网关", restart: "重启网关", failed: "Codex 断开失败：config is read-only" },
  "zh-TW": { on: "開啟", paused: "已暫停", state: "已暫停", resume: "恢復閘道", restart: "重啟閘道", failed: "Codex 中斷連線失敗：config is read-only" },
  ja: { on: "オン", paused: "一時停止中", state: "一時停止中", resume: "ゲートウェイを再開", restart: "ゲートウェイを再起動", failed: "Codex の接続を解除できませんでした: config is read-only" },
  de: { on: "An", paused: "Pausiert", state: "pausiert", resume: "Zugangspunkt fortsetzen", restart: "Zugangspunkt neu starten", failed: "Codex konnte nicht getrennt werden: config is read-only" },
};

function serve(lang, world) {
  const settings = () => ({ lang, theme: "light", searchVendors: [], searchAPIs: [], gatewayPaused: world.paused });
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/gateway/pause") {
      const { on } = JSON.parse(r.request().postData());
      world.posts.push(on);
      if (on && world.refuse) return json({ paused: false, agent: "Codex", error: "config is read-only" });
      world.paused = on;
      return json({ paused: on, agents: ["codex"] });
    }
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings() });
    if (url.pathname === "/api/settings") return json(settings());
    if (url.pathname === "/api/providers") return json({
      providers: [], presets: [], excluded: [],
      gateway: world.paused
        ? { running: false, mine: false, paused: true, window: true, url: "http://127.0.0.1:3999", calls: [], groups: [], models: 0 }
        : { running: true, mine: true, window: true, url: "http://127.0.0.1:3999", calls: [], groups: [], models: 3 },
    });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/gateway/trace") return json({ mine: false, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) for (const width of [1000, 420]) {
    const w = words[lang];
    test(`${engine} ${lang} ${width}px: the gateway pauses from Settings and resumes from its page`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch());
      t.after(() => browser.close());
      const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); }); // a goto in WebKit can cut off a fetch (#1350)
      const world = { paused: false, posts: [], refuse: true };
      await page.route("**/*", serve(lang, world));
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/?view=settings&tab=general");
      await settingsDrawn(page);
      const row = page.locator("#gatewayPauseRow");
      await row.locator(".opt.on").waitFor();
      assert.equal(await row.isVisible(), true, "the row is hidden in the app");
      assert.deepEqual(await row.locator(".opt").allInnerTexts(), [w.on, w.paused]);
      assert.equal(await row.locator(".opt.on").innerText(), w.on);
      // what the reader clicks stays put on screen (gui-shell.md, "A click
      // never moves"); the view's scrollTop may change around it
      const top = (l) => l.evaluate((e) => Math.round(e.getBoundingClientRect().top));

      // an agent that can't be disconnected: named, and the row stays On
      await row.locator(".opt", { hasText: w.paused }).click();
      await page.waitForFunction((m) => document.querySelector("#status")?.textContent === m, w.failed);
      assert.deepEqual(world.posts, [true]);
      await page.waitForFunction((on) => document.querySelector("#gatewayPauseRow .opt.on")?.textContent === on, w.on);

      // paused: the row, in sight since the click, stays put
      const before = await top(row);
      world.refuse = false;
      await row.locator(".opt", { hasText: w.paused }).click();
      await page.waitForFunction((p) => document.querySelector("#gatewayPauseRow .opt.on")?.textContent === p, w.paused);
      assert.deepEqual(world.posts, [true, true]);
      assert.equal(await top(row), before, "the click moved the row");
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "the page scrolls sideways");

      // the Gateway page says so, and resumes it
      await page.goto("http://magpie.test/?view=gateway");
      const fix = page.locator("#gateway .gw-fix");
      await fix.waitFor();
      assert.equal((await page.locator("#gateway .state").textContent()).trim(), w.state);
      assert.equal((await fix.textContent()).trim(), w.resume);
      assert.equal(await page.locator("#gateway .sub.err").count(), 0);
      // the card's own text changes length, so the page's scroll is what
      // holds here
      const scrolls = () => page.evaluate(() => [...document.querySelectorAll(".view, html, body")].map((e) => e.scrollTop).join(","));
      const gwBefore = await scrolls();
      await fix.click();
      await page.waitForFunction((r) => document.querySelector("#gateway .gw-fix")?.textContent.trim() === r, w.restart);
      assert.deepEqual(world.posts, [true, true, false]);
      assert.notEqual((await page.locator("#gateway .state").textContent()).trim(), w.state);
      assert.equal(await scrolls(), gwBefore, "the click scrolled the page");
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "the page scrolls sideways");
      assert.deepEqual(errors, []);
    });
  }
}
