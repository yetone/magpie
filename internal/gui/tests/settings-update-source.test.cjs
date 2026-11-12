// Run with Node's test runner and Playwright on the module path; see README.md.
// Update source (RootSir and Sakugar_ on X): Settings › Network has a menu
// button under App updates' proxy. Automatic, usemagpie.ai only and GitHub
// only each save updateSource alone (""/"site"/"github"), in the app's menu
// and in every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
function server(lang, posts) {
  let cur = {
    theme: "light", lang, tray: "panel", currency: "usd", textSize: 100,
    proxy: "direct", proxyNow: "", proxySource: "off", pluginProxy: "", updateProxy: "",
    trayUsageEvery: 3, version: "test", dir: "/test", lanURLs: [],
    visionModels: [], imageGenModels: [], workbuddyCheckins: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: true })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        posts.push(body);
        // as settings.Save: the custom copy is the server's, set from the value
        cur = { ...cur, ...body };
      }
      return json(cur);
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const labels = {
  en: { row: "Update source", auto: "Automatic", site: "usemagpie.ai only", github: "GitHub only", saved: "Saved" },
  zh: { row: "更新源", auto: "自动", site: "仅 usemagpie.ai", github: "仅 GitHub", saved: "已保存" },
  "zh-TW": { row: "更新來源", auto: "自動", site: "僅 usemagpie.ai", github: "僅 GitHub", saved: "已儲存" },
  ja: { row: "アップデート元", auto: "自動", site: "usemagpie.ai のみ", github: "GitHub のみ", saved: "保存しました" },
  de: { row: "Update-Quelle", auto: "Automatisch", site: "Nur usemagpie.ai", github: "Nur GitHub", saved: "Gespeichert" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(labels)) {
    const l = labels[lang];
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: the update source is picked from the app's menu`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(4000);
        page.setDefaultNavigationTimeout(60000);
        const posts = [], errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posts));
        await page.goto("http://magpie.test/?view=settings&tab=network");
        const row = page.locator("#updateSourceRow");
        await row.getByText(l.row, { exact: true }).waitFor();
        assert.equal(await row.locator("select").count(), 0, "no native select");
        const pick = page.locator("#updateSourcePick");
        assert.equal(await pick.textContent(), l.auto);
        const choose = async (name, want) => {
          const count = posts.length;
          const response = page.waitForResponse((r) => r.url().endsWith("/api/settings") && r.request().method() === "POST");
          await pick.click();
          await page.locator(".proto-menu").waitFor();
          assert.equal(await page.locator(".proto-menu .pm-item").count(), 3);
          await page.locator(".proto-menu").getByRole("menuitemradio", { name, exact: true }).click();
          await response;
          await page.waitForFunction((word) => document.querySelector("#status")?.textContent.includes(word), l.saved);
          assert.equal(posts.length, count + 1, "save once");
          assert.equal(posts.at(-1).updateSource, want);
          assert.equal(posts.at(-1).updateProxy, "", "the update proxy stays as it was");
          assert.equal(posts.at(-1).proxy, "direct", "the global proxy stays as it was");
          await page.waitForFunction((word) => document.querySelector("#updateSourcePick")?.textContent === word, name);
        };
        await choose(l.github, "github");
        await choose(l.site, "site");
        await choose(l.auto, "");
        // the menu ticks the one chosen
        await pick.click();
        assert.equal(await page.locator(".proto-menu .pm-item.on").textContent(), l.auto);
        await page.keyboard.press("Escape");
        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert(over <= 0, `the page scrolls sideways by ${over}px`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
