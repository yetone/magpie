// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing pool's account list is the name element's tooltip, not the row's:
// attached to the whole row it covered the Routing and Stays buttons on it,
// so a press landed on the tip's text and nothing was picked (#3785, yetone).
// The buttons carry no title of their own; the element listing the accounts
// behind them still does.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [{ id: "a/sol", name: "sol", providerName: "A Cloud", icon: "generic" }];
const groups = { models, pools: [
  { provider: "a", name: "A Cloud", icon: "generic", kind: "account", who: ["acct-a", "acct-b"], routing: "", affinity: "", sink: false },
], groups: [] };

function serve(lang) {
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a pool's account list is not over its Routing and Stays buttons`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 900 } })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=groups");
      const row = page.locator(".rt-pool");
      await row.waitFor();
      assert.equal(await row.getAttribute("title"), null, "the row itself carries no tooltip");
      const nm = row.locator(".nm");
      assert.equal(await nm.getAttribute("title"), "acct-a, acct-b", "the accounts element does");
      for (const b of await row.locator(".opt").all()) {
        assert.equal(await b.getAttribute("title"), null, "a Routing or Stays button carries no title");
        assert.notEqual(await b.getAttribute("title"), "acct-a, acct-b");
      }
      assert.deepEqual(errors, []);
    });
  }
}
