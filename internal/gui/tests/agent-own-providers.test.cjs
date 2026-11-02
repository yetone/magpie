// Run with Node's test runner and Playwright on the module path; see README.md.
// Renzexi01 on X: an agent with one provider of its own read "now has 1
// providers of its own". One is said in the singular, more than one as
// before. In every language, at a narrow width too, in Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const options = [{ value: "m1", label: "m1" }, { value: "relay/m1", label: "m1", ref: "relay/m1", note: "Relay · via magpie" }];
const state = () => ({
  agents: [
    { id: "mimo", name: "MiMo Code", icon: "generic", path: "/fixture/mimo", source: "providers:1", fields: [{ key: "model", label: "model", value: "m1", options }] },
    { id: "opencode", name: "OpenCode", icon: "generic", path: "/fixture/opencode", source: "providers:3", fields: [{ key: "model", label: "model", value: "m1", options }] },
  ],
  profiles: [],
});

function server(lang) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ ...state(), settings: { lang, theme: "light", guideHidden: true } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const one = "Not connected · now has 1 provider of its own";
const many = "Not connected · now has {n} providers of its own";
const row = (id) => `.row.agent[data-id="${id}"]`;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [980, 420]) {
      test(`${engine} ${lang} ${width}px: one provider of its own is said in the singular`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 640 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/?view=agents");
        await page.locator(`${row("mimo")} .ag-st`).waitFor();
        const tr = (k) => page.evaluate(([k, lang]) => (lang === "en" ? k : I18N[lang]?.[k]), [k, lang]);
        for (const k of [one, many]) assert.ok(await tr(k), `${lang} has ${k}`);
        assert.equal((await page.locator(`${row("mimo")} .ag-st`).textContent()).trim(), await tr(one));
        assert.equal((await page.locator(`${row("opencode")} .ag-st`).textContent()).trim(), (await tr(many)).replace("{n}", "3"));
        if (lang === "en") assert.doesNotMatch(await page.locator(`${row("mimo")} .ag-st`).textContent(), /1 providers/);
        const wide = await page.evaluate(() => document.scrollingElement.scrollWidth - document.scrollingElement.clientWidth);
        assert.ok(wide <= 0, `the page scrolls sideways by ${wide}px`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
