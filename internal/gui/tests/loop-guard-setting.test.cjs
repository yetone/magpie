// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Models › Replies (#1359, Moody-Sin: DeepSeek's reasoning went
// round "Now." "Writing." "Go." to its output limit and the agent waited):
// Stop looping replies is On, says what it does, and Off saves noLoopGuard
// with the rest of the settings kept, the page not moved by the click. In
// English, Chinese, Japanese and German, 1100 and 420 wide. The gateway's error names it by
// the English path, Settings › Models › Replies › Stop looping replies.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { name: "Stop looping replies", head: "Replies", off: "Off", on: "On", sub: "an error the agent can retry" },
  zh: { name: "停止循环的回复", head: "回复", off: "关闭", on: "开启", sub: "可以重试的错误" },
  ja: { name: "ループする応答を止める", head: "応答", off: "オフ", on: "オン", sub: "再試行できるエラー" },
  de: { name: "Schleifende Antworten beenden", head: "Antworten", off: "Aus", on: "An", sub: "den der Agent wiederholen kann" },
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
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) for (const width of [1100, 420]) {
    const w = words[lang];
    test(`${engine} ${lang} ${width}: replies that loop are stopped`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 900 } })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const saved = [];
      await page.route("**/*", serve(lang, saved));
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/?view=settings&tab=models");
      await settingsDrawn(page); // rows found are the ones that stay (#1659)
      const row = page.locator("#replyList #loopGuardRow");
      await row.waitFor();
      // scrolled to as a reader does, with the wheel: WebKit at 420 put
      // back a scroll made from script
      await page.mouse.move(width / 2, 450);
      for (let i = 0; i < 40; i++) {
        const box = await row.boundingBox();
        if (box && box.y >= 0 && box.y + box.height <= 900) break;
        await page.mouse.wheel(0, 120);
        await page.waitForTimeout(50);
      }
      assert.equal(await row.locator(".name").textContent(), w.name);
      assert((await row.locator(".sub").textContent()).includes(w.sub));
      // its explanation rests out of sight, shown whole in the row's hover card
      // (settings-hints.test.cjs)
      assert.equal(await row.locator(".sub").first().evaluate((e) => getComputedStyle(e).getPropertyValue("--hint").trim()), "1", "the explanation shows at rest");
      for (const part of [".name", ".segs"]) {
        const fit = await row.locator(part).first().evaluate((e) => {
          const r = e.getBoundingClientRect();
          return e.scrollWidth <= e.clientWidth + 1 && r.width > 0 && r.right <= innerWidth + 1;
        });
        assert(fit, `${part} fits at ${width}`);
      }
      assert.equal(await page.locator("#replyList").evaluate((l) => l.previousElementSibling.textContent.trim()), w.head);
      const on = row.locator(".segs button", { hasText: w.on });
      const off = row.locator(".segs button", { hasText: w.off });
      assert.equal(await on.getAttribute("aria-pressed"), "true", "On at first");

      const before = await page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]);
      const b = await off.boundingBox();
      await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
      await page.waitForFunction(() => document.querySelector("#loopGuardRow"));
      for (let i = 0; i < 50 && !saved.length; i++) await page.waitForTimeout(50);
      assert.equal(saved.length, 1, "saved once");
      assert.equal(saved[0].noLoopGuard, true);
      assert.equal(saved[0].replyModel, "", "the row beside it kept");
      assert.equal(saved[0].redact, true, "the rest kept");
      assert.equal(saved[0].lang, lang);
      assert.deepEqual(await page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]), before, "the click moved nothing");
      assert.deepEqual(errors, []);
    });
  }
}
