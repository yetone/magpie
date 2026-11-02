// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Models › Replies: Claude Code's interactive mode is On, says
// what it saves, that On never runs -p and Off always does; Off saves
// claudePrintMode with the rest of the settings kept, the page not moved
// by the click, and On turns -p back off. There is no third choice: the
// interactive mode on never falls back to -p (the owner, 11-01). In
// English, Chinese, Japanese and German, 1100 and 420 wide.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { name: "Claude Code's interactive mode", head: "Replies", off: "Off", on: "On", sub: "about half", risk: "claude -p" },
  zh: { name: "Claude Code 交互模式", head: "回复", off: "关闭", on: "开启", sub: "约为 -p 的一半", risk: "claude -p" },
  ja: { name: "Claude Code の対話モード", head: "応答", off: "オフ", on: "オン", sub: "-p の約半分", risk: "claude -p" },
  de: { name: "Interaktiver Modus von Claude Code", head: "Antworten", off: "Aus", on: "An", sub: "Hälfte von -p", risk: "claude -p" },
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
    test(`${engine} ${lang} ${width}: Claude Code's interactive mode can be turned off`, async (t) => {
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
      const row = page.locator("#replyList #claudeTuiRow");
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
      const sub = await row.locator(".sub").textContent();
      assert(sub.includes(w.sub) && sub.includes(w.risk), sub);
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
      const seg = (text) => row.locator(".segs button", { hasText: new RegExp(`^${text}$`) });
      const on = seg(w.on), off = seg(w.off);
      assert.equal(await row.locator(".segs button").count(), 2, "On and Off, nothing between");
      assert.equal(await on.getAttribute("aria-pressed"), "true", "On at first");

      const before = await page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]);
      await row.evaluate((e) => { e.dataset.drawn = "before"; });
      const b = await off.boundingBox();
      await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
      await page.waitForFunction(() => document.querySelector("#claudeTuiRow"));
      for (let i = 0; i < 50 && !saved.length; i++) await page.waitForTimeout(50);
      assert.equal(saved.length, 1, "saved once");
      assert.equal(saved[0].claudePrintMode, true);
      assert.equal(saved[0].claudeLeanPrompt, false, "the row beside it kept");
      assert.equal(saved[0].redact, true, "the rest kept");
      assert.equal(saved[0].lang, lang);
      assert(!("claudeTUIOnly" in saved[0]), "the retired setting isn't sent");
      assert.deepEqual(await page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]), before, "the click moved nothing");

      // the save draws the page again: the buttons are the new row's
      await page.waitForFunction(() => { const r = document.querySelector("#replyList #claudeTuiRow"); return r && !r.dataset.drawn; });
      assert.equal(await seg(w.off).getAttribute("aria-pressed"), "true", "Off once saved");
      await seg(w.on).click();
      for (let i = 0; i < 50 && saved.length < 2; i++) await page.waitForTimeout(50);
      assert.equal(saved.length, 2, "saved again");
      assert.equal(saved[1].claudePrintMode, false, "-p turned back off");
      assert.equal(await seg(w.on).getAttribute("aria-pressed"), "true", "On once saved");
      assert.deepEqual(errors, []);
    });
  }
}
