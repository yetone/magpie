// Run with Node's test runner and Playwright on the module path; see README.md.
// zhujiu on Discord: the Quit question (quit-ask.test.cjs) wants a way not
// to be asked again, and a switch to have it back. Settings › General ›
// Ask before quitting is On by default, says when it asks, and Off saves
// quitNoAsk with the rest of the settings kept; a quitNoAsk the question's
// Don't ask again saved shows Off, and On saves it false. The app's only:
// a browser's page (magpie web) quits without asking, so the row isn't
// there. In every language, both engines, 1100 and 420 wide; segments,
// never a native <select>, and the click moves nothing.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { name: "Ask before quitting", off: "Off", on: "On", sub: "Quit names them and asks first" },
  zh: { name: "退出前询问", off: "关闭", on: "开启", sub: "退出前先列出它们并询问" },
  "zh-TW": { name: "退出前詢問", off: "關閉", on: "開啟", sub: "退出前先列出它們並詢問" },
  ja: { name: "終了前に確認", off: "オフ", on: "オン", sub: "終了前にそれらを示して確認します" },
  de: { name: "Vor dem Beenden fragen", off: "Aus", on: "An", sub: "nennt Beenden sie und fragt zuerst nach" },
};

function serve(lang, saved, own, web) {
  const settings = { lang, theme: "light", redact: true, keepAwake: true, ...own };
  const state = { agents: [], profiles: [], settings };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
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
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) for (const width of [1100, 420]) {
    const w = words[lang];
    const open = async (t, own = {}, web = false) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 900 } })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      t.after(() => assert.deepEqual(errors, []));
      const saved = [];
      await page.route("**/*", serve(lang, saved, own, web));
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/?view=settings&tab=general");
      await settingsDrawn(page);
      return { page, saved };
    };
    // scrolled to as a reader does, with the wheel
    const reach = async (page, row) => {
      await page.mouse.move(width / 2, 450);
      for (let i = 0; i < 40; i++) {
        const box = await row.boundingBox();
        if (box && box.y >= 0 && box.y + box.height <= 900) break;
        await page.mouse.wheel(0, 120);
        await page.waitForTimeout(50);
      }
    };
    const press = async (page, button) => {
      const b = await button.boundingBox();
      await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
    };
    const until = async (f) => { for (let i = 0; i < 100 && !f(); i++) await new Promise((r) => setTimeout(r, 50)); };

    test(`${engine} ${lang} ${width}: Ask before quitting is On, and Off saves quitNoAsk`, async (t) => {
      const { page, saved } = await open(t);
      const row = page.locator("#setPage-general #quitAskRow");
      await row.waitFor();
      await reach(page, row);
      assert.equal(await row.locator(".name").textContent(), w.name);
      assert.ok((await row.locator(".sub").textContent()).includes(w.sub));
      for (const part of [".name", ".sub", ".segs"]) {
        const fit = await row.locator(part).first().evaluate((e) => {
          const r = e.getBoundingClientRect();
          return e.scrollWidth <= e.clientWidth + 1 && r.width > 0 && r.left >= 0 && r.right <= innerWidth + 1;
        });
        assert.ok(fit, `${part} fits at ${width}`);
      }
      assert.equal(await row.locator("select").count(), 0, "no native <select>");
      const on = row.locator(".segs button", { hasText: w.on });
      const off = row.locator(".segs button", { hasText: w.off });
      assert.equal(await on.getAttribute("aria-pressed"), "true", "On by default");

      const before = await page.evaluate(() => [scrollX, scrollY, document.querySelector("#view-settings")?.scrollTop]);
      await press(page, off);
      await until(() => saved.length);
      assert.equal(saved.length, 1, "saved once");
      assert.equal(saved[0].quitNoAsk, true);
      assert.equal(saved[0].keepAwake, true, "the row near it kept");
      assert.equal(saved[0].redact, true, "the rest kept");
      assert.equal(saved[0].lang, lang);
      assert.deepEqual(await page.evaluate(() => [scrollX, scrollY, document.querySelector("#view-settings")?.scrollTop]), before, "the click moved nothing");
    });

    test(`${engine} ${lang} ${width}: after Don't ask again it shows Off, and On asks again`, async (t) => {
      const { page, saved } = await open(t, { quitNoAsk: true });
      const row = page.locator("#setPage-general #quitAskRow");
      await row.waitFor();
      await reach(page, row);
      const on = row.locator(".segs button", { hasText: w.on });
      assert.equal(await row.locator(".segs button", { hasText: w.off }).getAttribute("aria-pressed"), "true", "Off, as the question left it");
      await press(page, on);
      await until(() => saved.length);
      assert.equal(saved.length, 1, "saved once");
      assert.equal(saved[0].quitNoAsk, false);
      assert.equal(saved[0].keepAwake, true, "the rest kept");
    });

    if (width === 420) test(`${engine} ${lang}: a browser's page has no Ask before quitting`, async (t) => {
      const { page } = await open(t, {}, true);
      await page.locator("#setPage-general #langPick").waitFor({ state: "attached" });
      assert.equal(await page.locator("#quitAskRow").isVisible(), false);
    });
  }
}
