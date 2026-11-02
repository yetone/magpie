// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Models › Replies (DD on Discord: Anthropic's safety filter
// refused a turn, and magpie asked account after account where Claude Code
// would have switched to its fallback model): Ask again after a refusal is
// All at first, says what Never ask again does, and Never ask again saves
// refusalRetries 0, then All saves null, with the rest of the settings kept
// and the page not moved by the clicks. In English, Chinese, Traditional
// Chinese, Japanese and German, 1100 and 420 wide.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach, watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { name: "Ask again after a refusal", head: "Replies", never: "Never ask again", all: "All", sub: "Claude Code then switches to its own fallback model" },
  zh: { name: "被拒答后再问", head: "回复", never: "不再问", all: "全部", sub: "Claude Code 随后会像直连 Anthropic API 时一样" },
  "zh-TW": { name: "被拒答後再問", head: "回覆", never: "不再問", all: "全部", sub: "Claude Code 隨後會像直連 Anthropic API 時一樣" },
  ja: { name: "拒否のあと聞き直す", head: "応答", never: "聞き直さない", all: "すべて", sub: "フォールバックモデルに切り替えます" },
  de: { name: "Nach einer Ablehnung erneut fragen", head: "Antworten", never: "Nie", all: "Alle", sub: "wechselt dann selbst zu seinem Ausweichmodell" },
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
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1100, 420]) {
    const w = words[lang];
    test(`${engine} ${lang} ${width}: how often a refusal is asked again`, async (t) => {
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
      const row = page.locator("#replyList #refusalRetriesRow");
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
      const opts = row.locator(".segs button");
      assert.deepEqual(await opts.allTextContents(), [w.never, "1", "2", "3", w.all]);
      const all = row.locator(".segs button", { hasText: w.all });
      assert.equal(await all.getAttribute("aria-pressed"), "true", "All at first");

      // where the page is, the document and the view the row is in
      const at = () => page.evaluate(() => [scrollX, scrollY, document.querySelector("#refusalRetriesRow").closest(".view").scrollTop]);
      const moved = [];
      // pressed where the reader sees it, with nothing over it: the first
      // save's "Saved" note sat over the row at 420, and a press on it saved
      // nothing (reproduced with six runs at once)
      const click = async (name) => {
        const button = row.locator(".segs button", { hasText: name });
        // the wheel come to rest: WebKit's went on after reach had the row,
        // and at 420 carried All under the window's foot once Image
        // generation timeout was drawn above, so it is reached again until
        // it is still clear when the wheel has stopped
        let before;
        for (let tries = 0; tries < 5; tries++) {
          await reach(page, button);
          before = await at();
          for (let i = 0; i < 40; i++) {
            await page.waitForTimeout(100);
            const now = await at();
            if (JSON.stringify(now) === JSON.stringify(before)) break;
            before = now;
          }
          const clear = await button.evaluate((e) => {
            const b = e.getBoundingClientRect(), hit = document.elementFromPoint(b.left + b.width / 2, b.top + b.height / 2);
            return !!hit && (e === hit || e.contains(hit));
          });
          if (clear) break;
        }
        const b = await button.boundingBox();
        await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
        await page.waitForTimeout(100);
        const after = await at();
        if (JSON.stringify(after) !== JSON.stringify(before)) moved.push(`${name}: ${before} → ${after}`);
      };
      // the row as drawn before the save, for the one drawn with its answer
      // to be told apart: the second click goes to that one, as a reader's
      // does, not to one about to be taken away
      await row.evaluate((e) => { e.dataset.before = "1"; });
      await click(w.never);
      for (let i = 0; i < 200 && !saved.length; i++) await page.waitForTimeout(50);
      assert.equal(saved.length, 1, "saved once");
      assert.equal(saved[0].refusalRetries, 0);
      assert.equal(saved[0].noLoopGuard, false, "the row beside it kept");
      assert.equal(saved[0].redact, true, "the rest kept");
      assert.equal(saved[0].lang, lang);
      await page.waitForFunction((never) => {
        const r = document.querySelector("#refusalRetriesRow");
        return r && !r.dataset.before && r.querySelector(".segs button[aria-pressed=true]")?.textContent === never;
      }, w.never, { timeout: 10000 });
      await click(w.all);
      for (let i = 0; i < 200 && saved.length < 2; i++) await page.waitForTimeout(50);
      assert.equal(saved.length, 2, `saved again: ${JSON.stringify(saved)}`);
      assert.ok("refusalRetries" in saved[1] && saved[1].refusalRetries === null, `All sent as null: ${JSON.stringify(saved)}`);
      assert.deepEqual(moved, [], "the clicks moved nothing");
      assert.deepEqual(errors, []);
    });
  }
}
