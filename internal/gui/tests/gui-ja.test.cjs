// Run with Node's test runner and Playwright on the module path; see README.md.
// Japanese ("日文文档也要有日文截图"): every string the GUI has in Chinese
// it has in Japanese, with the same {placeholders} and tags. Picked in
// Settings, or the system's language when that is Japanese, the window
// reads in Japanese, its <html lang> is ja-JP, and Settings offers 日本語.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

test("ja has every zh string, with its placeholders and tags", async () => {
  const src = await fs.readFile(path.join(assets, "i18n.js"), "utf8");
  const I18N = new Function(src.slice(0, src.indexOf("\n};") + 3) + "; return I18N;")();
  const zh = Object.keys(I18N.zh), ja = I18N.ja;
  assert.deepEqual(zh.filter((k) => !(k in ja)), [], "missing in ja");
  assert.deepEqual(Object.keys(ja).filter((k) => !(k in I18N.zh)), [], "ja only");
  const marks = (s) => [...s.matchAll(/\{\w+\}|<\/?\w+[^>]*>/g)].map((m) => m[0]).sort();
  for (const k of zh) {
    assert.equal(typeof ja[k], "string", k);
    assert.deepEqual(marks(ja[k]), marks(k), `ja for ${JSON.stringify(k)}`);
  }
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
  test(`${name}: the window reads in Japanese when picked or the system's`, async () => {
    const browser = await engine.launch();
    try {
      for (const [lang, sys, want] of [["ja", "en-US", "ja"], ["system", "ja-JP", "ja"], ["system", "en-US", "en"], ["system", "zh-CN", "zh"]]) {
        const page = await (await browser.newContext({ locale: sys, viewport: { width: 1000, height: 760 } })).newPage();
        page.setDefaultTimeout(5000);
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/");
        await page.waitForFunction(() => typeof locale === "string" && document.documentElement.lang);
        const got = await page.evaluate(() => ({
          locale, lang: document.documentElement.lang,
          nav: [...document.querySelectorAll("button[data-view]")].map((b) => b.textContent.trim()).slice(0, 5),
          offered: LOCALES.map(([v, l]) => v + ":" + l),
        }));
        assert.equal(got.locale, want, `${lang} on ${sys}`);
        assert.ok(got.offered.includes("ja:日本語"));
        if (want === "ja") {
          assert.equal(got.lang, "ja-JP");
          assert.deepEqual(got.nav, ["エージェント", "プロバイダ", "ゲートウェイ", "ルーティング", "使用量"]);
        }
        await page.close();
      }
    } finally { await browser.close(); }
  });
}
