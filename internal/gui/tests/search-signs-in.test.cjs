// Run with Node's test runner and Playwright on the module path; see README.md.
// A plugin's search API that signs in as the plugin's provider (axelburks on
// Discord) takes no key: Settings' add row says whose sign-in it uses in
// place of the key field, Add sends it with none, and each added one's row
// says how many of the provider's accounts are on, or, as an error, that
// none is signed in or none is on. In all five languages, Chromium and
// WebKit, 980px and 420px wide, with the API faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const vendors = [
  { id: "seek-web", name: "Seek", auth: { name: "SeekCo", signedIn: true, on: 2 } },
  { id: "out-web", name: "Out", auth: { name: "OutCo", signedIn: false, on: 0 } },
  { id: "none-web", name: "None On", auth: { name: "NoneCo", signedIn: true, on: 0 } },
];
const words = {
  en: { add: "Add", signs: "Signs in with SeekCo", on: "Signed in to SeekCo · accounts on: 2", out: "Not signed in to OutCo: sign in to it on Providers", none: "No account of NoneCo is on" },
  zh: { add: "添加", signs: "以 SeekCo 登录", on: "已登录 SeekCo · 开启的账号：2", out: "未登录 OutCo：请在“供应商”中登录", none: "NoneCo 没有开启的账号" },
  "zh-TW": { add: "新增", signs: "以 SeekCo 登入", on: "已登入 SeekCo · 開啟的帳號：2", out: "未登入 OutCo：請在「供應商」中登入", none: "NoneCo 沒有開啟的帳號" },
  ja: { add: "追加", signs: "SeekCo でサインイン", on: "SeekCo にサインイン済み · オンのアカウント：2", out: "OutCo にサインインしていません：「プロバイダ」でサインインしてください", none: "NoneCo のオンのアカウントがありません" },
  de: { add: "Hinzufügen", signs: "Meldet sich mit SeekCo an", on: "Bei SeekCo angemeldet · Konten an: 2", out: "Nicht bei OutCo angemeldet: unter Anbieter anmelden", none: "Kein Konto von NoneCo ist an" },
};

function serve(lang, posted) {
  let apis = [
    { vendor: "out-web", name: "Out", ready: true },
    { vendor: "none-web", name: "None On", ready: true },
  ];
  const settings = () => ({ lang, theme: "light", searchVendors: vendors, searchAPIs: apis });
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data, status = 200) => r.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings() });
    if (url.pathname === "/api/settings") return json(settings());
    if (url.pathname === "/api/settings/search-api") {
      const b = JSON.parse(r.request().postData());
      posted.push(b);
      apis = [...apis, { vendor: b.vendor, name: vendors.find((v) => v.id === b.vendor).name, ready: true }];
      return json(settings());
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) for (const width of [980, 420]) {
    const w = words[lang];
    test(`${engine} ${lang} ${width}px: a search API that signs in shows its sign-in, not a key`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 1400 } })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posted = [];
      await page.route("**/*", serve(lang, posted));
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/?view=settings&tab=models");
      await settingsDrawn(page);
      const list = page.locator("#searchList");
      const head = list.locator(".row.search-add");
      await head.waitFor();

      // the first vendor is picked: Seek, which signs in as SeekCo
      assert.equal(await head.locator("button.search-vendor").innerText(), "Seek");
      assert.equal(await head.locator("input.search-key").isVisible(), false, "no key field");
      assert.equal(await head.locator("button.link").isVisible(), false, "no Get a key");
      assert.equal(await head.locator(".search-signs").isVisible(), true);
      assert.equal(await head.locator(".search-signs").innerText(), w.signs);

      // the stored ones: not signed in, and none on, are errors
      const rows = list.locator(".row.search-api");
      assert.equal(await rows.nth(0).locator(".sub").innerText(), w.out);
      assert.equal(await rows.nth(0).locator(".sub").evaluate((e) => e.classList.contains("err")), true);
      assert.equal(await rows.nth(1).locator(".sub").innerText(), w.none);
      assert.equal(await rows.nth(1).locator(".sub").evaluate((e) => e.classList.contains("err")), true);
      assert.equal(await rows.nth(0).locator("button.search-eye").count(), 0, "nothing to show");
      assert.equal(await list.locator(".row.search-keys").count(), 0, "no keys to list");

      // Add sends it with no key, and its row says how many accounts are on
      await head.locator("button.text", { hasText: w.add }).evaluate((b) => b.click());
      await page.waitForFunction(() => document.querySelectorAll("#searchList .row.search-api").length === 3);
      assert.deepEqual(posted, [{ vendor: "seek-web", key: "", url: "" }]);
      assert.equal(await rows.nth(2).locator(".sub").innerText(), w.on);
      assert.equal(await rows.nth(2).locator(".sub").evaluate((e) => e.classList.contains("err")), false);
      assert(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth), "nothing wider than the window");
      assert.deepEqual(errors, []);
    });
  }
}
