// Run with Node's test runner and Playwright on the module path; see README.md.
// xiao_wang24004 on X: 能不能在里面加一个保持电脑任务常亮的选项呢？电脑在做任务
// 的过程中经常自动关闭了. Settings → General has a "Prevent sleep" row
// (named so by 1ooogle on X), off by default: On and Off are saved as keepAwake true and
// false, the click moving nothing; another setting saved keeps it; a
// browser's page (magpie web, the gateway's own computer) shows it too.
// #975 (Hu9956: an agent recording the screen found it locked): a third
// choice keeps the display on too, saved as keepAwakeDisplay, its line
// saying so, and another setting's save keeps it. In every language, at
// 900px and 440px, with the API faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { name: "Prevent sleep", sub: "Keeps this computer awake while magpie runs tasks; closing the lid still puts it to sleep", on: "On", off: "Off", display: "Screen on too", displaySub: "Keeps this computer and its screen awake while magpie runs tasks; closing the lid still puts it to sleep" },
  zh: { name: "防止休眠", sub: "在 magpie 运行任务时，让电脑保持唤醒状态；合上盖子仍会休眠", on: "开启", off: "关闭", display: "屏幕也常亮", displaySub: "在 magpie 运行任务时，让电脑和屏幕保持唤醒状态；合上盖子仍会休眠" },
  ja: { name: "スリープを防ぐ", sub: "magpie がタスクを実行している間、このコンピューターをスリープさせません。蓋を閉じるとスリープします", on: "オン", off: "オフ", display: "画面もオン", displaySub: "magpie がタスクを実行している間、このコンピューターと画面をスリープさせません。蓋を閉じるとスリープします" },
  de: { name: "Ruhezustand verhindern", sub: "Hält diesen Computer wach, während magpie Aufgaben ausführt; beim Zuklappen schläft er trotzdem ein", on: "An", off: "Aus", display: "Bildschirm auch an", displaySub: "Hält diesen Computer und den Bildschirm wach, während magpie Aufgaben ausführt; beim Zuklappen schläft er trotzdem ein" },
};

function serve(lang, web, posted, st) {
  const settings = () => ({ lang, theme: "light", searchVendors: [], searchAPIs: [], ...st });
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data, status = 200) => r.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings() });
    if (url.pathname === "/api/settings") {
      if (r.request().method() === "POST") {
        const b = JSON.parse(r.request().postData());
        posted.push(b);
        Object.assign(st, b);
      }
      return json(settings());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(engine, lang, web, posted, st, t, width = 900) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 700 } })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, web, posted, st));
  await watchSettingsReads(page);
  await page.goto("http://magpie.test/?view=settings&tab=general");
  await settingsDrawn(page); // rows found are the ones that stay (#1659)
  await page.locator("#setPage-general .row.pref").first().waitFor();
  return { page, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) for (const width of [900, 440]) {
    const w = words[lang];
    test(`${engine} ${lang} ${width}px: keep awake is turned on and off in Settings → General`, async (t) => {
      const posted = [], st = {};
      const { page, errors } = await open(engine, lang, false, posted, st, t, width);
      const row = page.locator("#keepAwakeRow");
      await row.waitFor();
      // the row is near the page's end: scrolled by the reader, out from under the footer
      await page.mouse.move(width / 2, 350);
      const end = () => page.evaluate(() => { const m = document.querySelector("#view-settings"); return m.scrollTop + m.clientHeight >= m.scrollHeight - 1; });
      for (let i = 0; i < 20 && !(await end()); i++) {
        await page.mouse.wheel(0, 400);
        await page.waitForTimeout(150);
      }
      assert.equal(await row.locator(".name").innerText(), w.name);
      assert.equal(await row.locator(".sub").innerText(), w.sub);
      const opt = (name) => row.locator("#keepAwakeSegs .opt").getByText(name, { exact: true });
      assert.equal(await row.locator("#keepAwakeSegs .opt.on").innerText(), w.off, "off by default");
      const where = () => page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)]);
      const before = await where();
      const click = async (loc) => {
        // the last save's answer draws Settings again, every option replaced:
        // one measured before that is gone (null) by the time it is clicked
        await page.waitForFunction(() => prefsBusy === 0);
        const b = await loc.boundingBox();
        await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
      };
      await click(opt(w.on));
      for (let i = 0; i < 50 && !posted.length; i++) await page.waitForTimeout(50);
      assert.equal(posted.at(-1)?.keepAwake, true, `posted ${JSON.stringify(posted)}`);
      assert.equal(st.keepAwake, true);
      assert.equal(await row.locator("#keepAwakeSegs .opt.on").innerText(), w.on);
      // another setting saved keeps it on
      const n = posted.length;
      await click(page.locator("#dockSegs .opt").first()).catch(() => {});
      for (let i = 0; i < 20 && posted.length === n; i++) await page.waitForTimeout(50);
      if (posted.length > n) assert.equal(posted.at(-1).keepAwake, true, "another setting's save turned it off");
      // the display kept on too (#975)
      await click(opt(w.display));
      for (let i = 0; i < 50 && posted.at(-1)?.keepAwakeDisplay !== true; i++) await page.waitForTimeout(50);
      assert.equal(posted.at(-1).keepAwake, true);
      assert.equal(posted.at(-1).keepAwakeDisplay, true);
      assert.equal(await row.locator("#keepAwakeSegs .opt.on").innerText(), w.display);
      // the row is drawn again as the save comes back
      for (let i = 0; i < 50 && (await row.locator(".sub").innerText()) !== w.displaySub; i++) await page.waitForTimeout(50);
      assert.equal(await row.locator(".sub").innerText(), w.displaySub);
      const m = posted.length;
      await click(page.locator("#dockSegs .opt").first()).catch(() => {});
      for (let i = 0; i < 20 && posted.length === m; i++) await page.waitForTimeout(50);
      if (posted.length > m) assert.equal(posted.at(-1).keepAwakeDisplay, true, "another setting's save turned the display off");
      // the segments fit the row, none cut off
      const fit = await page.evaluate(() => {
        const row = document.querySelector("#keepAwakeRow").getBoundingClientRect();
        return [...document.querySelectorAll("#keepAwakeSegs .opt")].every((o) => { const b = o.getBoundingClientRect(); return b.width > 0 && b.right <= row.right + 1 && o.scrollWidth <= o.clientWidth + 1; });
      });
      assert(fit, "the segments don't fit the row");
      await click(opt(w.on));
      for (let i = 0; i < 50 && posted.at(-1)?.keepAwakeDisplay !== false; i++) await page.waitForTimeout(50);
      assert.equal(posted.at(-1).keepAwake, true);
      assert.equal(posted.at(-1).keepAwakeDisplay, false);
      for (let i = 0; i < 50 && (await row.locator(".sub").innerText()) !== w.sub; i++) await page.waitForTimeout(50);
      assert.equal(await row.locator(".sub").innerText(), w.sub);
      await click(opt(w.off));
      for (let i = 0; i < 50 && posted.at(-1)?.keepAwake !== false; i++) await page.waitForTimeout(50);
      assert.equal(posted.at(-1).keepAwake, false);
      assert.equal(posted.at(-1).keepAwakeDisplay, false);
      assert.deepEqual(await where(), before, "the clicks moved the page");
      // the row has no coloured stripe down its left
      const left = await row.evaluate((e) => getComputedStyle(e).borderLeftWidth);
      assert(parseFloat(left) <= 1, `left border ${left}`);
      assert.deepEqual(errors, []);
    });

    if (width === 900) test(`${engine} ${lang}: a browser's page has keep awake too`, async (t) => {
      const { page, errors } = await open(engine, lang, true, [], { keepAwake: true }, t);
      assert.equal(await page.locator("#keepAwakeRow").isVisible(), true);
      assert.equal(await page.locator("#keepAwakeSegs .opt.on").innerText(), words[lang].on);
      assert.deepEqual(errors, []);
    });

    if (width === 900) test(`${engine} ${lang}: the display kept on is shown as saved`, async (t) => {
      const { page, errors } = await open(engine, lang, false, [], { keepAwake: true, keepAwakeDisplay: true }, t);
      assert.equal(await page.locator("#keepAwakeSegs .opt.on").innerText(), words[lang].display);
      assert.equal(await page.locator("#keepAwakeRow .sub").innerText(), words[lang].displaySub);
      assert.deepEqual(errors, []);
    });
  }
}
