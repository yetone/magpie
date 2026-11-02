// Run with Node's test runner and Playwright on the module path; see README.md.
// An OpenCode Go key whose windows can't be read says why on its card, in
// every language, rather than "Allowance unavailable": a phone can't hover
// for the reason (planWindows, planquota.go).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: true })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the errors planWindows gives, as built from OpenCode's real replies
const errs = [
  "this key has no OpenCode Go subscription: Go belongs to the member who subscribed, in that workspace, so use a key that member made there (opencode.ai, the workspace's API Keys)",
  "OpenCode didn't take this key (401 Unauthorized: Unauthorized): it was deleted, or is mistyped",
  "OpenCode Go's reply has no rolling, weekly or monthly window: fields plan, usage.fiveHour",
];
const keys = [
  "No OpenCode Go on this key — use a key made by the member who subscribed, in that workspace",
  "OpenCode didn't take this key — paste a current one in the provider's settings",
  "OpenCode Go answered in a form magpie doesn't read — hover for what it sent",
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine}: an OpenCode Go key's card says why it has no windows (${lang})`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 440, height: 760 } })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang));
      await page.goto("http://magpie.test/?view=providers");
      await page.waitForFunction(() => typeof quotaError === "function");
      const said = await page.evaluate((errs) => errs.map((e) => quotaError(e)), errs);
      const want = await page.evaluate((keys) => keys.map((k) => t(k)), keys);
      const generic = await page.evaluate(() => t("Allowance unavailable — hover for why"));
      for (let i = 0; i < errs.length; i++) {
        assert.equal(said[i], want[i], errs[i]);
        assert.notEqual(said[i], generic);
        if (lang !== "en") assert.notEqual(said[i], keys[i], `${lang} has its own words for: ${keys[i]}`);
      }
      // a 503 or anything else is still the vendor's words on hover only
      assert.equal(await page.evaluate(() => quotaError("503 Service Unavailable: Inference routing is unavailable. Please retry later.")), generic);
      assert.deepEqual(errors, []);
    });
  }
}
