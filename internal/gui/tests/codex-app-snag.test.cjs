// Run with Node's test runner and Playwright on the module path; see README.md.
// ysm119 on Discord (#4550): after magpie restarted the Codex app's
// app-server, a signed-in ChatGPT app showed "ChatGPT hit a snag" till it
// was quit and opened again. magpie reopens it itself when nothing runs;
// where it couldn't (Linux, an app that didn't quit), the Agents row says
// to (appSnag), over what it would say of its provider. Every language,
// both engines, wide and narrow. No backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const codex = (appSnag) => ({
  id: "codex", name: "Codex", path: "/fixture/config.toml", wired: true,
  fields: [{ key: "model", label: "model", value: "gpt-6.1-sol", options: [{ value: "gpt-6.1-sol", label: "gpt-6.1-sol", ref: "gpt-6.1-sol" }] }],
  appSnag,
});

const words = {
  en: "The Codex app hit an error · quit it and open it again",
  zh: "Codex 应用出错了 · 请退出后重新打开",
  "zh-TW": "Codex 應用程式出錯了 · 請結束後重新開啟",
  ja: "Codex アプリでエラーが発生しました · 終了してもう一度開いてください",
  de: "Die Codex-App hat einen Fehler · beende sie und öffne sie erneut",
};

function serve(lang, snag) {
  const state = { agents: [codex(snag)], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: state });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/agents/cli") return route.fulfill({ json: { agents: {}, pending: false } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], presets: [], excluded: [], gateway: { running: true, window: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const row = '.row.agent[data-id="codex"]';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [980, 440]) {
      test(`${engine} ${lang} ${width}px: the Codex row says to quit and reopen the app after its error page`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await browser.newPage({ viewport: { width, height: 800 } });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, true));
        await page.goto("http://magpie.test/?view=agents");
        const st = page.locator(`${row} .ag-st-t`);
        await st.waitFor();
        assert.equal((await st.textContent()).trim(), words[lang]);
        assert.ok(await page.locator(`${row} .ag-st.wait, ${row} .wait .ag-st-t, ${row} .wait`).count() > 0, "said as a wait");
        const box = await st.boundingBox();
        assert.ok(box && box.x >= 0 && box.x + box.width <= width + 0.5, "the line is on the page: " + JSON.stringify(box));
        const side = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert.ok(side <= 1, "scrolls sideways: " + side);
        assert.deepEqual(errors, []);
      });
    }
  }
  test(`${engine}: without appSnag the row says nothing of it`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage({ viewport: { width: 980, height: 800 } });
    await page.route("**/*", serve("en", undefined));
    await page.goto("http://magpie.test/?view=agents");
    await page.locator(`${row} .ag-st-t`).waitFor();
    assert.doesNotMatch(await page.locator(`${row} .ag-st-t`).textContent(), /hit an error/);
  });
}
