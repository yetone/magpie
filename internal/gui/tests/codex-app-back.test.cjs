// Run with Node's test runner and Playwright on the module path; see README.md.
// A bug report by mail (11-04): after its ChatGPT allowance reset, Codex
// was back on its account, but the Codex app still showed no account and
// no usage. Its app-server was the one started while magpie served Codex,
// and it reads the account only as it starts; with "restart the Codex app
// when magpie switches its account back" off, nothing restarted it, and
// nothing said so. The Agents row now says it (appBack), on hover and in
// the row opened, with a Restart that posts codex/daemon/restart-app.
// Every language, both engines, wide and narrow. No backend, the API is
// faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const codex = (appBack) => ({
  id: "codex", name: "Codex", path: "/fixture/config.toml", wired: true,
  fields: [{ key: "model", label: "model", value: "gpt-6.1-sol", options: [{ value: "gpt-6.1-sol", label: "gpt-6.1-sol", ref: "gpt-6.1-sol" }] }],
  appBack,
});

const words = {
  en: { line: "Back on ChatGPT", why: "no usage", restart: "Restart", done: "Codex app-server restarted" },
  zh: { line: "已回到 ChatGPT", why: "用量", restart: "重启", done: "Codex 的 app-server 已重启" },
  "zh-TW": { line: "已回到 ChatGPT", why: "用量", restart: "重啟", done: "Codex 的 app-server 已重新啟動" },
  ja: { line: "ChatGPT に戻りました", why: "使用量", restart: "再起動", done: "Codex の app-server を再起動しました" },
  de: { line: "Wieder bei ChatGPT", why: "keine Nutzung", restart: "Neu starten", done: "App-Server von Codex neu gestartet" },
};

function serve(lang, posts) {
  let back = true;
  const state = () => ({ agents: [codex(back || undefined)], profiles: [], settings: { lang, theme: "light" } });
  const providers = { providers: [], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/codex/daemon/restart-app") {
      posts.push(route.request().method());
      back = false;
      return route.fulfill({ json: providers });
    }
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: state() });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/agents/cli") return route.fulfill({ json: { agents: {}, pending: false } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: providers });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const row = '.row.agent[data-id="codex"]';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    for (const width of [980, 440]) {
      test(`${engine} ${lang} ${width}px: back on ChatGPT, the Codex row says to restart the app and does it`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        for (const where of ["line", "opened"]) {
          await t.test(where, async () => {
            const page = await browser.newPage({ viewport: { width, height: 800 } });
            page.setDefaultTimeout(5000);
            page.setDefaultNavigationTimeout(60000);
            const errors = [];
            page.on("pageerror", (e) => errors.push(e.message));
            const posts = [];
            await page.route("**/*", serve(lang, posts));
            await page.goto("http://magpie.test/?view=agents");
            const st = page.locator(`${row} .ag-st-t`);
            await st.waitFor();
            const text = await st.textContent();
            assert.ok(text.includes(w.line), `${JSON.stringify(text)} lacks ${JSON.stringify(w.line)}`);
            assert.doesNotMatch(text, /\{\w+\}/);
            const title = await st.getAttribute("title");
            assert.ok(title && title.includes(w.why), "the hover says why: " + title);
            assert.doesNotMatch(title, /\{\w+\}/);
            let go;
            if (where === "line") {
              go = page.locator(`${row} .ag-st button`, { hasText: w.restart });
            } else {
              await page.locator(`${row} .ag-link`).click();
              const said = page.locator(`${row} .ag-app-back`);
              await said.waitFor();
              const s = await said.textContent();
              assert.ok(s.includes(w.why), "the row opened says why: " + s);
              assert.doesNotMatch(s, /\{\w+\}/);
              go = page.locator(`${row} .ag-app-back + .ag-vl button`);
              await page.waitForFunction(() => {
                const b = document.querySelector('.row.agent[data-id="codex"] .ag-app-back + .ag-vl button'), r = b.getBoundingClientRect();
                return document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2) === b;
              });
            }
            assert.equal((await go.textContent()).trim(), w.restart);
            const box = await go.boundingBox(), vw = page.viewportSize().width;
            assert.ok(box && box.x >= 0 && box.x + box.width <= vw + 0.5, "Restart is on the page: " + JSON.stringify(box));
            const side = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
            assert.ok(side <= 1, "scrolls sideways: " + side);
            await go.click();
            await page.locator("dialog.action-confirm .primary").click();
            await page.waitForFunction(() => !document.querySelector('.row.agent[data-id="codex"] .ag-app-back'));
            await page.waitForFunction((l) => !document.querySelector('.row.agent[data-id="codex"] .ag-st-t')?.textContent.includes(l), w.line);
            assert.deepEqual(posts, ["POST"]);
            assert.deepEqual(errors, []);
            await page.close();
          });
        }
      });
    }
  }
  test(`${engine}: without appBack the row says nothing of it`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage({ viewport: { width: 980, height: 800 } });
    const posts = [];
    const handler = serve("en", posts);
    await page.route("**/*", async (route) => {
      if (new URL(route.request().url()).pathname === "/api/state") return route.fulfill({ json: { agents: [codex(undefined)], profiles: [], settings: { lang: "en", theme: "light" } } });
      return handler(route);
    });
    await page.goto("http://magpie.test/?view=agents");
    await page.locator(`${row} .ag-st-t`).waitFor();
    assert.doesNotMatch(await page.locator(`${row} .ag-st-t`).textContent(), /Back on ChatGPT/);
    await page.locator(`${row} .ag-link`).click();
    await page.locator(`${row} .ag-exp`).waitFor();
    assert.equal(await page.locator(`${row} .ag-app-back`).count(), 0);
  });
}
