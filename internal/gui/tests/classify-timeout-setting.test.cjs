// Settings > Models > Replies: #2934 gives every routing classifier the
// selected timeout. Checks saving, other preferences, custom values,
// translations, narrow layouts and scroll position in both engines.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach, watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { name: "Intent classifier timeout", head: "Replies", sec: (n) => `${n} s`, sub: "A slow classifier may need longer" },
  zh: { name: "意图判断超时", head: "回复", sec: (n) => `${n} 秒`, sub: "较慢的判断模型可能需要更长时间" },
  "zh-TW": { name: "意圖判斷逾時", head: "回覆", sec: (n) => `${n} 秒`, sub: "較慢的判斷模型可能需要更長時間" },
  ja: { name: "インテント判定のタイムアウト", head: "応答", sec: (n) => `${n} 秒`, sub: "遅い分類モデル" },
  de: { name: "Zeitlimit für den Absicht-Klassifikator", head: "Antworten", sec: (n) => `${n} s`, sub: "Ein langsamer Klassifikator" },
};

function serve(lang, saved, own) {
  const settings = { lang, theme: "light", redact: true, imageGen: "off", ...own };
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

async function open(engine, lang, width, saved, own, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 900 } })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, saved, own));
  await watchSettingsReads(page);
  await page.goto("http://magpie.test/?view=settings&tab=models");
  await settingsDrawn(page); // rows found are the ones that stay (#1659)
  const row = page.locator("#replyList #classifyTimeoutRow");
  await row.waitFor();
  return { page, row, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1100, 420]) {
    const w = words[lang];
    test(`${engine} ${lang} ${width}: how long a classifier is given to answer`, async (t) => {
      const saved = [];
      const { page, row, errors } = await open(engine, lang, width, saved, {}, t);
      // scrolled to as a reader does, with the wheel
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
      assert.deepEqual(await row.locator(".segs button").allTextContents(), [8, 15, 30, 60].map(w.sec));
      assert.equal(await row.locator(".segs button[aria-pressed=true]").textContent(), w.sec(8), "8 s at first");

      const at = () => page.evaluate(() => [scrollX, scrollY, document.querySelector("#classifyTimeoutRow").closest(".view").scrollTop]);
      const moved = [];
      const click = async (name) => {
        const button = row.locator(".segs button", { hasText: new RegExp(`^${name}$`) });
        await reach(page, button);
        let before = await at();
        for (let i = 0; i < 40; i++) {
          await page.waitForTimeout(100);
          const now = await at();
          if (JSON.stringify(now) === JSON.stringify(before)) break;
          before = now;
        }
        const b = await button.boundingBox();
        await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
        await page.waitForTimeout(100);
        const after = await at();
        if (JSON.stringify(after) !== JSON.stringify(before)) moved.push(`${name}: ${before} → ${after}`);
      };
      await row.evaluate((e) => { e.dataset.before = "1"; });
      await click(w.sec(30));
      for (let i = 0; i < 200 && !saved.length; i++) await page.waitForTimeout(50);
      assert.equal(saved.length, 1, "saved once");
      assert.equal(saved[0].classifyTimeout, 30);
      assert.equal(saved[0].imageGen, "off", "image preferences kept");
      assert.equal(saved[0].redact, true, "the rest kept");
      assert.equal(saved[0].lang, lang);
      await page.waitForFunction((m) => {
        const r = document.querySelector("#classifyTimeoutRow");
        return r && !r.dataset.before && r.querySelector(".segs button[aria-pressed=true]")?.textContent === m;
      }, w.sec(30), { timeout: 10000 });
      await click(w.sec(8));
      for (let i = 0; i < 200 && saved.length < 2; i++) await page.waitForTimeout(50);
      assert.equal(saved.length, 2, `saved again: ${JSON.stringify(saved)}`);
      assert.equal(saved[1].classifyTimeout, 8);
      assert.deepEqual(moved, [], "the click moved nothing");
      assert.deepEqual(errors, []);
    });
  }
  test(`${engine}: seconds set by hand show as their own choice, and are sent on with other saves`, async (t) => {
    const saved = [];
    const { page, row, errors } = await open(engine, "en", 1100, saved, { classifyTimeout: 45 }, t);
    assert.deepEqual(await row.locator(".segs button").allTextContents(), ["8 s", "15 s", "30 s", "60 s", "45 s"]);
    assert.equal(await row.locator(".segs button[aria-pressed=true]").textContent(), "45 s");
    // another row's save keeps the seconds
    await page.evaluate(() => savePrefs({ ...prefsKeep(prefs), redact: false }));
    for (let i = 0; i < 200 && !saved.length; i++) await page.waitForTimeout(50);
    assert.equal(saved[0].classifyTimeout, 45, JSON.stringify(saved));
    assert.deepEqual(errors, []);
  });
}
