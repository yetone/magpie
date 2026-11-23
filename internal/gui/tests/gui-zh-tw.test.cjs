// Run with Node's test runner and Playwright on the module path; see README.md.
// Traditional Chinese (zh-TW): every string the GUI has in Chinese it has
// in Traditional, with the same {placeholders} and tags and Taiwan's words
// (設定, 檔案, 帳號, 伺服器, 預設). Picked in Settings, or the system's
// language when that is zh-TW, zh-HK or any zh-Hant, the window reads in
// Traditional Chinese, its <html lang> is zh-TW, and Settings offers
// 繁體中文 beside 简体中文.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { strings } = require("./strings.cjs");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

test("zh-TW has every zh string, with its placeholders and tags, in Traditional", async () => {
  const { I18N } = strings(); // i18n.js's and the plugins'
  const zh = Object.keys(I18N.zh), tw = I18N["zh-TW"];
  assert.deepEqual(zh.filter((k) => !(k in tw)), [], "missing in zh-TW");
  assert.deepEqual(Object.keys(tw).filter((k) => !(k in I18N.zh)), [], "zh-TW only");
  const marks = (s) => [...s.matchAll(/\{\w+\}|<\/?\w+[^>]*>/g)].map((m) => m[0]).sort();
  // characters only Simplified writes; a value with one wasn't converted
  const simplified = /[设账号务认户录络软数据择错误删储图标页时间们这说过还应关开语简设议]/;
  for (const k of zh) {
    assert.equal(typeof tw[k], "string", k);
    assert.deepEqual(marks(tw[k]), marks(I18N.zh[k]), `zh-TW for ${JSON.stringify(k)}`);
    assert.doesNotMatch(tw[k].replace(/\{\w+\}|<[^>]*>|简体中文/g, ""), simplified, `zh-TW for ${JSON.stringify(k)}: ${tw[k]}`);
  }
  // Taiwan's words where they differ from the Mainland's
  for (const [k, want] of [["Settings", "設定"], ["File", "檔案"], ["Accounts", "帳號"], ["Default", "預設"], ["Sign in", "登入"], ["Save", "儲存"], ["Gateway", "閘道"]]) {
    if (k in tw) assert.equal(tw[k], want, k);
  }
  assert.ok(Object.values(tw).some((v) => v.includes("伺服器")), "伺服器");
});

function serve(lang) {
  const settings = { lang, theme: "light" };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/gateway/trace") return url.searchParams.get("wait") ? new Promise(() => {}) : json({ mine: true, now: new Date().toISOString(), seq: 0, totals: {}, routes: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

for (const [name, engine] of [["webkit", webkit], ["chromium", chromium]]) {
  test(`${name}: the window reads in Traditional Chinese when picked or the system's`, async () => {
    const browser = await engine.launch();
    try {
      for (const [lang, sys, want] of [
        ["zh-TW", "en-US", "zh-TW"], ["system", "zh-TW", "zh-TW"], ["system", "zh-HK", "zh-TW"],
        ["system", "zh-Hant-TW", "zh-TW"], ["system", "zh-CN", "zh"], ["system", "zh-SG", "zh"],
        ["zh", "zh-TW", "zh"], ["system", "en-US", "en"],
      ]) {
        const page = await (await browser.newContext({ locale: sys, viewport: { width: 1000, height: 760 } })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/");
        await page.waitForFunction(() => typeof locale === "string" && document.documentElement.lang);
        const got = await page.evaluate(() => ({
          locale, lang: document.documentElement.lang, sys: navigator.language,
          nav: [...document.querySelectorAll("button[data-view]")].map((b) => b.textContent.trim()).slice(0, 5),
          offered: LOCALES.map(([v, l]) => v + ":" + l),
          units: (chineseUnits = true, [fmtN(133000000), fmtN(68100)]),
        }));
        assert.equal(got.locale, want, `${lang} on ${sys} (navigator.language ${got.sys})`);
        assert.ok(got.offered.includes("zh-TW:繁體中文") && got.offered.includes("zh:简体中文"), got.offered.join());
        if (want === "zh-TW") {
          assert.equal(got.lang, "zh-TW");
          assert.deepEqual(got.nav, ["Agent", "會話", "資源庫", "Artifacts", "供應商"]);
          assert.deepEqual(got.units, ["1.33 億", "6.8 萬"]);
        }
        if (want === "zh") assert.deepEqual(got.units, ["1.33 亿", "6.8 万"]);
        await page.close();
      }
      // Settings → Language is one menu button showing the language picked
      // (1ooogle on X): it fits its row on a wide and a narrow window, and
      // its menu lists all six, none cut short
      for (const width of [1000, 440]) {
        const page = await (await browser.newContext({ locale: "zh-TW", viewport: { width, height: 760 } })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        await page.route("**/*", serve("zh-TW"));
        await page.goto("http://magpie.test/");
        await page.locator("#prefs").click();
        await page.locator("#langPick span").first().waitFor();
        const fit = await page.evaluate(() => {
          const b = document.querySelector("#langPick"), row = b.closest(".row").getBoundingClientRect(), box = b.getBoundingClientRect();
          return {
            inside: box.left >= row.left && box.right <= row.right + 0.5,
            sideways: document.documentElement.scrollWidth > innerWidth,
            shown: b.textContent,
            cut: b.querySelector("span").scrollWidth > b.querySelector("span").clientWidth + 1,
            units: !document.querySelector("#unitsRow").hidden,
          };
        });
        assert.equal(fit.shown, "繁體中文");
        assert.ok(fit.inside && !fit.sideways && !fit.cut, `${width}px: ${JSON.stringify(fit)}`);
        assert.ok(fit.units, "Number units (萬 / 億) is offered in Traditional Chinese too");
        await page.locator("#langPick").click();
        const items = page.locator(".proto-menu .pm-item");
        await items.first().waitFor();
        const menu = await items.evaluateAll((bs) => bs.map((b) => { const n = b.querySelector(".pm-name"); return { name: n.textContent, cut: n.scrollWidth > n.clientWidth + 1, inside: b.getBoundingClientRect().right <= innerWidth }; }));
        assert.deepEqual(menu.map((m) => m.name), ["跟隨系統", "English", "简体中文", "繁體中文", "日本語", "Deutsch"]);
        assert.ok(menu.every((m) => !m.cut && m.inside), `${width}px menu: ${JSON.stringify(menu)}`);
        await page.close();
      }
    } finally { await browser.close(); }
  });
}
