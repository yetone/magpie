// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Models › Replies (#5371, tlerbao: an Antigravity quota ran out,
// and OpenCode waited out the hours-long Retry-After before its own fallback
// model): Longest wait an agent is told is No limit at first, says what a
// shorter wait does, and 1 min saves retryAfterCap 60, then No limit saves
// null, with the rest of the settings kept and the page not moved by the
// clicks. In English, Chinese, Traditional Chinese, Japanese and German,
// 1100 and 420 wide.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach, watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { name: "Longest wait an agent is told", head: "Replies", min: (n) => `${n} min`, none: "No limit", sub: "The accounts rest as long either way" },
  zh: { name: "告诉智能体的最长等待", head: "回复", min: (n) => `${n} 分钟`, none: "不限制", sub: "账号休息的时长不变" },
  "zh-TW": { name: "告訴智慧體的最長等待", head: "回覆", min: (n) => `${n} 分鐘`, none: "不限制", sub: "帳號休息的時長不變" },
  ja: { name: "エージェントに伝える最長の待ち時間", head: "応答", min: (n) => `${n} 分`, none: "上限なし", sub: "アカウントの休止時間は変わりません" },
  de: { name: "Längste Wartezeit für einen Agenten", head: "Antworten", min: (n) => `${n} min`, none: "Kein Limit", sub: "Die Konten ruhen in beiden Fällen gleich lange" },
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
    test(`${engine} ${lang} ${width}: the longest wait an agent is told`, async (t) => {
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
      const row = page.locator("#replyList #retryAfterCapRow");
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
      assert.deepEqual(await opts.allTextContents(), [w.min(1), w.min(5), w.min(15), w.none]);
      const none = row.locator(".segs button", { hasText: w.none });
      assert.equal(await none.getAttribute("aria-pressed"), "true", "No limit at first");

      // where the page is, the document and the view the row is in
      const at = () => page.evaluate(() => [scrollX, scrollY, document.querySelector("#retryAfterCapRow").closest(".view").scrollTop]);
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
      await click(w.min(1));
      for (let i = 0; i < 200 && !saved.length; i++) await page.waitForTimeout(50);
      assert.equal(saved.length, 1, "saved once");
      assert.equal(saved[0].retryAfterCap, 60);
      assert.ok("refusalRetries" in saved[0] && saved[0].refusalRetries === null, "the row beside it kept");
      assert.equal(saved[0].redact, true, "the rest kept");
      assert.equal(saved[0].lang, lang);
      await page.waitForFunction((one) => {
        const r = document.querySelector("#retryAfterCapRow");
        return r && !r.dataset.before && r.querySelector(".segs button[aria-pressed=true]")?.textContent === one;
      }, w.min(1), { timeout: 10000 });
      await click(w.none);
      for (let i = 0; i < 200 && saved.length < 2; i++) await page.waitForTimeout(50);
      assert.equal(saved.length, 2, `saved again: ${JSON.stringify(saved)}`);
      assert.ok("retryAfterCap" in saved[1] && saved[1].retryAfterCap === null, `No limit sent as null: ${JSON.stringify(saved)}`);
      assert.deepEqual(moved, [], "the clicks moved nothing");
      assert.deepEqual(errors, []);
    });
  }
}
