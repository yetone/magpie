// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Privacy's Masked words as a list, a word a row (noahhrreion on
// Discord: many words were hard to keep in one small box). Words saved by
// the one box before are drawn a row each, untouched until edited; the add
// box takes a word, or several split at commas as the box did, and keeps
// its focus for the next; a row is edited in place, × takes it out, an
// emptied row goes, Escape puts a row back; what is saved is the same list
// of strings, and a reload draws it again. Every language, Chromium and
// WebKit, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach, watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const remove = { en: "Remove Kestrel", zh: "移除 Kestrel", "zh-TW": "移除 Kestrel", ja: "Kestrel を削除", de: "Kestrel entfernen" };

function serve(lang, saved, box) {
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: box.settings });
    if (url.pathname === "/api/settings") {
      if (r.request().method() === "POST") {
        box.settings = JSON.parse(r.request().postData());
        saved.push(box.settings.redactWords);
      }
      return json(box.settings);
    }
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const LIST = '#redactList .word-list[data-list="words"]';
// the words on the page now, read in one go (a save redraws the list)
const rows = (page) => page.evaluate((sel) => [...document.querySelectorAll(`${sel} input[data-word]`)].map((i) => i.value), LIST);
// the page drawn again with these words, the list's own save landed
const drawn = (page, want) => page.waitForFunction(([sel, want]) =>
  JSON.stringify([...document.querySelectorAll(`${sel} input[data-word]`)].map((i) => i.dataset.word)) === want, [LIST, JSON.stringify(want)], { timeout: 5000 });
const savedAs = async (saved, want) => {
  for (let n = 0; n < 100 && JSON.stringify(saved.at(-1)) !== JSON.stringify(want); n++) await new Promise((r) => setTimeout(r, 50));
  assert.deepEqual(saved.at(-1), want);
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 400]) {
      test(`${engine} ${lang} ${width}px: masked words are a list, added, edited and removed a row at a time`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 1400 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], saved = [];
        // as the one box saved them: " , " split, trimmed, in order
        const box = { settings: { lang, theme: "light", redact: true, redactPersonal: false, redactWords: ["Nightjar", "百度", "build-01.internal"] } };
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, saved, box));
        await watchSettingsReads(page);
        await page.goto("http://magpie.test/?view=settings&tab=privacy");
        await page.locator(LIST).waitFor();
        await settingsDrawn(page);

        // what was saved before, a row each, nothing written on open
        assert.deepEqual(await rows(page), ["Nightjar", "百度", "build-01.internal"]);
        assert.deepEqual(saved, [], "opening the page saves nothing");
        const fit = await page.evaluate((sel) => {
          const l = document.querySelector("#redactList").getBoundingClientRect();
          return [...document.querySelectorAll(`${sel} input, ${sel} button`)].every((n) => { const b = n.getBoundingClientRect(); return b.left >= l.left - 1 && b.right <= l.right + 1; })
            && document.documentElement.scrollWidth <= document.documentElement.clientWidth;
        }, LIST);
        assert(fit, "every row fits the page");
        // no native select, and the words stay dots until Show
        assert.equal(await page.locator(`${LIST} select`).count(), 0);
        await page.locator('#redactList .row:has(.word-list[data-list="words"]) .redact-eye').click();

        // added: one, then two pasted with commas, the box keeping its focus
        const add = page.locator(`${LIST} input.word-add`);
        await reach(page, add);
        await add.fill("Kestrel");
        await add.press("Enter");
        await savedAs(saved, ["Nightjar", "百度", "build-01.internal", "Kestrel"]);
        await drawn(page, ["Nightjar", "百度", "build-01.internal", "Kestrel"]);
        await page.waitForFunction((sel) => document.activeElement?.matches(`${sel} input.word-add`) && document.activeElement.value === "", LIST);
        await page.keyboard.type(" Osprey，ops.example.com ,, ");
        await page.keyboard.press("Enter");
        const five = ["Nightjar", "百度", "build-01.internal", "Kestrel", "Osprey", "ops.example.com"];
        await savedAs(saved, five);
        await drawn(page, five);

        // edited in place: the row keeps its place
        const n = saved.length;
        const baidu = page.locator(`${LIST} input[data-word="百度"]`);
        await baidu.fill("百度在线");
        await baidu.press("Enter");
        const edited = ["Nightjar", "百度在线", "build-01.internal", "Kestrel", "Osprey", "ops.example.com"];
        await savedAs(saved, edited);
        await drawn(page, edited);
        // Escape puts a row back, and saves nothing
        const night = page.locator(`${LIST} input[data-word="Nightjar"]`);
        await night.fill("Nope");
        await night.press("Escape");
        assert.equal(await night.inputValue(), "Nightjar");
        assert.equal(saved.length, n + 1, "Escape saved nothing");

        // × takes a row out, its name said in the window's language
        const x = page.locator(`${LIST} .word-line:has(input[data-word="Kestrel"]) .word-x`);
        assert.equal(await x.getAttribute("aria-label"), remove[lang]);
        await x.click();
        const less = ["Nightjar", "百度在线", "build-01.internal", "Osprey", "ops.example.com"];
        await savedAs(saved, less);
        await drawn(page, less);
        // and a row emptied goes when it is left
        const ops = page.locator(`${LIST} input[data-word="ops.example.com"]`);
        await ops.fill("");
        await ops.press("Tab");
        const left = ["Nightjar", "百度在线", "build-01.internal", "Osprey"];
        await savedAs(saved, left);
        await drawn(page, left);

        // what was saved is drawn again on the next load
        await page.reload();
        await page.locator(LIST).waitFor();
        await settingsDrawn(page);
        assert.deepEqual(await rows(page), left);
        assert.deepEqual(box.settings.redactWords, left);
        assert.deepEqual(errors, []);
      });
    }
  }
}
