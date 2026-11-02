// Run with Node's test runner and Playwright on the module path; see README.md.
// Snow CLI's light model and thinking (#1457) are set on magpie's profile
// only: there, the row has a small pill beside the model and a thinking
// pill whose default says Snow takes off; on a profile of the user's own
// neither has anything to offer, and neither is drawn. In every language,
// in the window and the tray panel's narrow row. No backend: the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const ref = (value) => ({ value, label: value, ref: value, note: "via magpie" });
const options = [ref("magpie/relay/glm-4.6"), ref("magpie/relay/glm-4.5v")];
const levels = [{ value: "", takes: "off" }, ...["low", "medium", "high", "max"].map((value) => ({ value }))];
// as magpie reads a Snow on its own profile, and on one of the user's
const snow = (ours) => ({
  id: "snow", name: "Snow CLI", icon: "snow", path: "/fixture/.snow/profiles/magpie.json", wired: ours,
  fields: ours ? [
    { key: "model", label: "model", value: "magpie/relay/glm-4.6", options },
    { key: "small", label: "small", value: "", options },
    { key: "effort", label: "thinking", value: "", options: levels },
  ] : [
    { key: "model", label: "model", value: "glm-5", options: [{ value: "glm-5", group: "Snow CLI" }, ...options] },
    { key: "small", label: "small", value: "", options: [] },
    { key: "effort", label: "thinking", value: "", options: [] },
  ],
});

function server(lang, ours) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [snow(ours)], profiles: [], settings: { lang, theme: "light", guideHidden: true } } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true, window: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const row = '.row.agent[data-id="snow"]';
// the thinking pill's default: Snow's own, off
const off = { en: "default (off)", zh: "默认（关闭）", "zh-TW": "預設（關閉）", ja: "デフォルト（オフ）", de: "Standard (aus)" };
// the settings drawn: pills, and the panel's thinking slider as "effort"
const keys = (scope) => [...document.querySelectorAll(scope + " .field")].map((b) => b.dataset.key)
  .concat(document.querySelector(scope + " .effort-control") ? ["effort"] : []);
const value = ([scope, key]) => (document.querySelector(`${scope} .field[data-key="${key}"] > .v`) || document.querySelector(`${scope} .effort-control .effort-head b`))?.textContent ?? null;
// nothing on the row runs past its right edge
const over = (scope) => {
  const r = document.querySelector(scope).getBoundingClientRect();
  return [...document.querySelectorAll(scope + " .field")].filter((b) => b.getBoundingClientRect().right > r.right + 1).map((b) => b.dataset.key);
};

// the row opened: the window's lists the settings beside the model, the
// panel's every one
async function open(page, mode) {
  if (mode === "panel") {
    await page.locator(`${row} .ag-sum`).click();
    await page.locator(`${row} .ag-open .field`).first().waitFor();
  } else if (await page.locator(`${row} .ag-link`).count()) {
    // a row not on magpie has its settings on the row itself
    await page.locator(`${row} .ag-link`).click();
    await page.locator(`${row} .ag-exp`).waitFor();
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(off)) {
    for (const [mode, width] of [["window", 1240], ["panel", 440], ["window", 720]]) {
      test(`${engine} ${lang} ${mode} ${width}: Snow's small and thinking on magpie's profile`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, true));
        await page.goto("http://magpie.test/" + (mode === "panel" ? "?mode=panel" : ""));
        await page.locator(row).waitFor();
        await open(page, mode);
        const got = await page.evaluate(keys, row);
        assert.ok(got.includes("small") && got.includes("effort"), JSON.stringify(got));
        assert.equal(await page.evaluate(value, [row, "effort"]), off[lang]);
        assert.deepEqual(await page.evaluate(over, row), [], "a pill past the row's edge");
        assert.deepEqual(errors, []);
        await page.close();
      });

      test(`${engine} ${lang} ${mode} ${width}: on the user's own profile Snow has no small or thinking pill`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, false));
        await page.goto("http://magpie.test/" + (mode === "panel" ? "?mode=panel" : ""));
        await page.locator(row).waitFor();
        await open(page, mode);
        const got = await page.evaluate(keys, row);
        assert.ok(got.includes("model"), JSON.stringify(got));
        assert.ok(!got.includes("small") && !got.includes("effort"), JSON.stringify(got));
        assert.deepEqual(errors, []);
        await page.close();
      });
    }
  }
}
