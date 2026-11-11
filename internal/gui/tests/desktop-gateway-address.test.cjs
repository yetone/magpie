// Run with Node's test runner and Playwright on the module path; see README.md.
// Claude Desktop's gateway address (#4546): drawn only while magpie offers
// an https address of this computer, picked from the app's own menu. All
// APIs and assets are intercepted; no backend or external network is used.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const LAN = "https://192.168.1.20:3425";
const fresh = (offer) => ({
  agents: [{
    id: "claude-desktop", name: "Claude Desktop", path: "/test/claude_desktop_config.json", icon: "claude-color", wired: true,
    fields: [
      { key: "provider", label: "provider", value: "magpie", options: [{ value: "magpie", label: "magpie" }] },
      { key: "gateway", label: "gateway", value: "", options: offer ? [{ value: "", label: "This computer" }, { value: LAN, label: LAN }] : [] },
    ],
  }],
  profiles: [],
});

function server(lang, fixture) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ ...fixture.state, settings: { lang, theme: "light" } });
    if (url.pathname === "/api/set") {
      const body = req.postDataJSON();
      fixture.sets.push(body);
      fixture.state = JSON.parse(JSON.stringify(fixture.state));
      fixture.state.agents[0].fields.find((f) => f.key === body.field).value = body.value;
      return json({ ...fixture.state, settings: { lang, theme: "light" } });
    }
    if (url.pathname === "/api/gateway/trace") return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const cd = '.row.agent[data-id="claude-desktop"]';
const written = (page) => page.waitForResponse((r) => new URL(r.url()).pathname === "/api/set");
const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];
const langs = process.env.LANGS ? process.env.LANGS.split(",") : ["en", "zh", "zh-TW", "ja", "de"];

async function open(browser, lang, width, fixture, errors) {
  const page = await browser.newPage({ viewport: { width, height: 760 } });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", server(lang, fixture));
  await page.goto("http://magpie.test/");
  await page.locator(cd).waitFor();
  return page;
}

for (const engine of engines) {
  for (const lang of langs) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: Desktop's gateway address is drawn only when offered`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(async () => { await browser.close(); });
        const errors = [];
        const off = await open(browser, lang, width, { state: fresh(false), sets: [] }, errors);
        await off.locator(`${cd} .ag-link`).click();
        await off.locator(`${cd} .ag-exp`).waitFor();
        assert.equal(await off.locator(`${cd} .field[data-key="gateway"]`).count(), 0, "nothing to pick: not drawn");

        const fixture = { state: fresh(true), sets: [] };
        const page = await open(browser, lang, width, fixture, errors);
        await page.locator(`${cd} .ag-link`).click();
        await page.locator(`${cd} .ag-exp`).waitFor();
        const f = page.locator(`${cd} .field[data-key="gateway"]`);
                assert.equal(await f.count(), 1, "drawn while an https address is offered");
        await f.click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        assert.equal(await page.locator("select").count(), 0, "no native select");
        const items = await page.locator("#pop #list li").allInnerTexts();
        assert(items.some((s) => s.includes(LAN)), `the address is offered: ${items}`);
        const wrote = written(page);
        await page.locator("#pop #list li", { hasText: LAN }).first().click();
        await wrote;
        assert.deepEqual(fixture.sets, [{ agent: "claude-desktop", field: "gateway", value: LAN }]);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
        assert.deepEqual(errors, []);
      });
    }
  }
}
