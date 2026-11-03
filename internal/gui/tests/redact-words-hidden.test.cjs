// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Privacy's Masked words are drawn as dots until Show
// (noahhrreion on Discord: a screen share showed the words kept from
// vendors), as a provider's key is. The box stays a text box, so an IME
// still writes into it; Show reveals them and holds through a redraw;
// a word typed while hidden saves as before. In every language, Chromium
// and WebKit, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { wheelTo, watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { name: "Masked words", show: "Show", hide: "Hide", on: "On" },
  zh: { name: "脱敏词", show: "显示", hide: "隐藏", on: "开启" },
  "zh-TW": { name: "脫敏詞", show: "顯示", hide: "隱藏", on: "開啟" },
  ja: { name: "マスクする語", show: "表示", hide: "非表示", on: "オン" },
  de: { name: "Maskierte Wörter", show: "Anzeigen", hide: "Ausblenden", on: "An" },
};

function serve(lang, saved) {
  let settings = { lang, theme: "light", redact: true, redactPersonal: false, redactWords: ["Nightjar", "百度"] };
  const state = { agents: [], profiles: [], settings };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings") {
      if (r.request().method() === "POST") {
        settings = JSON.parse(r.request().postData());
        saved.push(settings);
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

// what the box shows, read from the page as it is in one go (a save
// redraws the list, so a row found a moment before can be gone): dots by
// text security, or a password box where the engine has none
const shown = (page, name) => page.evaluate((name) => {
  const r = [...document.querySelector("#redactList").children].find((r) => r.querySelector(".who .name")?.textContent === name);
  const i = r.querySelector("input.words"), s = getComputedStyle(i), eye = r.querySelector(".redact-eye");
  const list = document.querySelector("#redactList").getBoundingClientRect(), b = i.getBoundingClientRect(), e = eye.getBoundingClientRect();
  return {
    type: i.type, dots: s.getPropertyValue("-webkit-text-security") === "disc" || i.type === "password", value: i.value,
    eye: eye.textContent, pressed: eye.getAttribute("aria-pressed"),
    inside: b.left >= list.left - 1 && e.right <= list.right + 1 && b.width >= 80,
    wide: document.documentElement.scrollWidth > document.documentElement.clientWidth,
  };
}, name);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, w] of Object.entries(words)) {
    for (const width of [1100, 400]) {
      test(`${engine} ${lang} ${width}px: masked words are dots until Show`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 1400 } })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], saved = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, saved));
        await watchSettingsReads(page);
        await page.goto("http://magpie.test/?view=settings&tab=privacy");
        const row = page.locator("#redactList > .row", { has: page.locator(".who .name", { hasText: new RegExp(`^${w.name}$`) }) });
        await row.locator(".redact-eye").waitFor();
        await settingsDrawn(page);
        await wheelTo(page, row, 300);

        let m = await shown(page, w.name);
        assert.equal(m.value, "Nightjar, 百度");
        assert(m.dots, "hidden at first");
        assert.equal(m.type, "text", "a text box, so an IME writes into it");
        assert.equal(m.eye, w.show);
        assert.equal(m.pressed, "false");
        assert(m.inside && !m.wide, "the box and Show fit the page");

        await row.locator(".redact-eye").click();
        m = await shown(page, w.name);
        assert(!m.dots, "shown");
        assert.equal(m.eye, w.hide);
        assert.equal(m.pressed, "true");

        // a redraw (another row's save) keeps it shown
        const whole = page.locator("#redactWordsWholeRow .segs .opt", { hasText: w.on });
        await whole.click();
        await page.waitForFunction(() => document.querySelector("#redactWordsWholeRow .segs .opt.on")?.textContent === t("On"));
        await row.locator(".redact-eye").waitFor();
        m = await shown(page, w.name);
        assert(!m.dots && m.eye === w.hide, "Show holds through a redraw");

        // hidden again, a word typed saves as before
        await row.locator(".redact-eye").click();
        assert((await shown(page, w.name)).dots);
        const box = row.locator("input.words");
        await box.fill("Nightjar, 百度, Kestrel");
        await box.press("Enter");
        for (let n = 0; n < 50 && !saved.at(-1)?.redactWords?.includes("Kestrel"); n++) await new Promise((r) => setTimeout(r, 100));
        assert.deepEqual(saved.at(-1).redactWords, ["Nightjar", "百度", "Kestrel"]);
        // the save redraws the page (Enter, then the old box's blur): wait
        // for the box on the page now, never one a redraw took away
        await page.waitForFunction(() => {
          const i = document.querySelector("#redactList input.words");
          return i?.isConnected && i.value.includes("Kestrel") && i.nextElementSibling?.classList.contains("redact-eye") && getComputedStyle(i).getPropertyValue("-webkit-text-security") === "disc";
        }, null, { timeout: 3000 }).catch(() => {});
        const last = await shown(page, w.name);
        assert(last.dots && last.eye === w.show, "still hidden after the save's redraw " + JSON.stringify(last));

        assert.deepEqual(errors, []);
      });
    }
  }
}
