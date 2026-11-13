// Run with Node's test runner and Playwright on the module path; see README.md.
// The Claude subscription's editor (baksili_ on X: a Claude account past
// its plan goes on at Anthropic's extra usage, billed): Spend extra usage
// is On, as it always was, says what Off does, and Settings' search finds
// it; Off saves claudeNoExtraUsage with the rest of the settings kept, the
// page not moved by the click. In English, Simplified and Traditional
// Chinese, Japanese and German, 1100 and 420 wide.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { provPrefsProviders, openProvPrefs, inEditor, watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { name: "Spend extra usage", off: "Off", on: "On", sub: "bills further requests as extra usage", risk: "other accounts take the requests" },
  zh: { name: "花费额外用量", off: "关闭", on: "开启", sub: "按额外用量计费", risk: "由你的其他账号接手" },
  "zh-TW": { name: "花費額外用量", off: "關閉", on: "開啟", sub: "按額外用量計費", risk: "由你的其他帳號接手" },
  ja: { name: "追加使用量を使う", off: "オフ", on: "オン", sub: "追加使用量として請求", risk: "ほかのアカウントがリクエストを受けます" },
  de: { name: "Zusätzliche Nutzung verbrauchen", off: "Aus", on: "An", sub: "als zusätzliche Nutzung ab", risk: "anderen Konten übernehmen die Anfragen" },
};

function serve(lang, saved) {
  const settings = { lang, theme: "light", redact: true };
  const state = { agents: [], profiles: [], settings };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings") {
      if (r.request().method() === "POST") {
        saved.push(JSON.parse(r.request().postData()));
        Object.assign(settings, saved.at(-1));
      }
      return json(settings);
    }
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json(provPrefsProviders(["claude"], { claude: "Claude Code" }));
    if (url.pathname === "/api/claude/mode") return json({ tui: !settings.claudePrintMode, why: settings.claudePrintMode ? "off" : "" });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) for (const width of [1100, 420]) {
    const w = words[lang];
    test(`${engine} ${lang} ${width}: extra usage can be turned off`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 900 } })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const saved = [];
      await page.route("**/*", serve(lang, saved));
      await openProvPrefs(page, "claude");
      const row = page.locator(".editor .prov-prefs #claudeRunList > #claudeExtraRow");
      await inEditor(page, row);
      assert.equal(await row.locator(".name").textContent(), w.name);
      const sub = await row.locator(".sub").textContent();
      assert(sub.includes(w.sub) && sub.includes(w.risk), sub);
      for (const part of [".name", ".segs"]) {
        const fit = await row.locator(part).first().evaluate((e) => {
          const r = e.getBoundingClientRect();
          return e.scrollWidth <= e.clientWidth + 1 && r.width > 0 && r.right <= innerWidth + 1;
        });
        assert(fit, `${part} fits at ${width}`);
      }
      const on = row.locator(".segs button", { hasText: w.on });
      const off = row.locator(".segs button", { hasText: w.off });
      assert.equal(await on.getAttribute("aria-pressed"), "true", "On at first");

      const before = await page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]);
      const b = await off.boundingBox();
      await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
      await page.waitForFunction(() => document.querySelector("#claudeExtraRow"));
      for (let i = 0; i < 50 && !saved.length; i++) await page.waitForTimeout(50);
      assert.equal(saved.length, 1, "saved once");
      assert.equal(saved[0].claudeNoExtraUsage, true);
      assert.equal(saved[0].claudeLeanPrompt, false, "the row beside it kept");
      assert.equal(saved[0].redact, true, "the rest kept");
      assert.equal(saved[0].lang, lang);
      assert.deepEqual(await page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]), before, "the click moved nothing");

      // Settings has it no more, and its search leads to the editor
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/?view=settings&tab=models");
      await settingsDrawn(page);
      assert.equal(await page.locator("#view-settings #claudeExtraRow").count(), 0, "not in Settings");
      await page.locator("#setQ").fill(w.name);
      const link = page.locator("#provPrefLinks .agent-pref-link", { hasText: w.name }).first();
      await link.waitFor();
      await link.locator("button").click();
      await page.locator(".editor .prov-prefs #claudeExtraRow").waitFor();
      assert.deepEqual(errors, []);
    });
  }
}
