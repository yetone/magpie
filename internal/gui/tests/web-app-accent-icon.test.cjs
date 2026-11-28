// Run with Node's test runner and Playwright on the module path; see README.md.
// Several magpies added to the Dock, each named in its tab's title, all had
// the one black bird (eachg0 on X). An accent of the reader's own is drawn
// in the tab's favicon, the home screen's icon and the standalone window's
// theme colours (webappmark.go): picking one in Settings gives the page the
// icons' new addresses and the window's tinted grounds at once, with no
// reload, and magpie's own colour puts index.html's back. In both engines,
// in every language, with the API faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
// appearance.go's lookCSS for these colours (TestLookFixture keeps it):
// the light block first, the dark one third; --bg is the window's ground
const LOOK = require("./fixtures/look-accents.json");
const bgOf = (a, dark) => LOOK[a].split("\n")[dark ? 2 : 0].match(/--bg:([^;}]+)/)[1];
const OWN = { icon: "icons/favicon.svg", touch: "icons/apple-touch-icon.png", light: "#f4f4f6", dark: "#1a1a1e" };
// webappmark.go's markOf
const markOf = (a) => a ? { icon: OWN.icon + "?accent=" + a.slice(1), touch: OWN.touch + "?accent=" + a.slice(1), light: bgOf(a, false), dark: bgOf(a, true) } : OWN;

function serve(st, log) {
  const settings = () => ({ lang: st.lang, theme: "light", searchVendors: [], searchAPIs: [], ...st });
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang: st.lang, theme: "light", web: true, look: "," })};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings() });
    if (url.pathname === "/api/settings") {
      if (r.request().method() === "POST") Object.assign(st, JSON.parse(r.request().postData()));
      return json(settings());
    }
    if (url.pathname === "/api/appearance/css") return json({ css: st.accent ? LOOK[st.accent] : "", key: (st.accent || "") + ",", mark: markOf(st.accent) });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.pathname.startsWith("/icons/")) log.push(url.pathname + url.search);
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png", ".json": "application/json" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const marks = (page) => page.evaluate(() => {
  const h = document.head, a = (s, n) => h.querySelector(s).getAttribute(n);
  return { icon: a('link[rel="icon"]', "href"), touch: a('link[rel="apple-touch-icon"]', "href"), light: a('meta[name="theme-color"][media*="light"]', "content"), dark: a('meta[name="theme-color"][media*="dark"]', "content") };
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: the tab's icon and the window's colours follow the accent`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const st = { lang }, log = [];
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 800 } })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(st, log));
      await page.goto("http://magpie.test/?view=settings");
      await page.locator("#accentPick .swatch").first().waitFor();
      assert.deepEqual(await marks(page), OWN, "magpie's own look: index.html's icons and colours");

      for (const a of ["#0d9488", "#db2777"]) {
        await page.locator(`#accentPick .swatch[data-accent="${a}"]`).click();
        await page.waitForFunction((h) => document.head.querySelector('link[rel="icon"]').getAttribute("href") === h, markOf(a).icon);
        assert.deepEqual(await marks(page), markOf(a), a);
        // the tab asks for the new picture, which Chromium shows (WebKit's
        // headless tab asks for none)
        if (engine === "chromium") {
          for (let i = 0; i < 50 && !log.includes("/" + markOf(a).icon); i++) await page.waitForTimeout(100);
          assert.ok(log.includes("/" + markOf(a).icon), `the tab asked for ${markOf(a).icon}: ${log}`);
        }
      }

      // back to magpie's own colour: index.html's again
      await page.locator('#accentPick .swatch[data-accent=""]').click();
      await page.waitForFunction((h) => document.head.querySelector('link[rel="icon"]').getAttribute("href") === h, OWN.icon);
      assert.deepEqual(await marks(page), OWN);
      assert.deepEqual(errors, []);
    });
  }
}
