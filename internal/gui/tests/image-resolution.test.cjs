// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Models › Images (skingstyle48487 on X: a 4K image asked through
// the image MCP came back at 2K): Image resolution is Automatic at first, a
// menu of the app's own (no native select) of Automatic, 1K, 2K and 4K;
// 4K saves imageResolution "4K" with the rest of the settings kept, and
// Automatic saves "". It is gone with image generation off, and another
// row's save keeps it. In English, Chinese, Traditional Chinese, Japanese
// and German, 1100 and 420 wide.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach, watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { name: "Image resolution", auto: "Automatic", sub: "when the agent names no size" },
  zh: { name: "生图分辨率", auto: "自动", sub: "智能体没指定尺寸时" },
  "zh-TW": { name: "生圖解析度", auto: "自動", sub: "代理沒指定尺寸時" },
  ja: { name: "画像の解像度", auto: "自動", sub: "サイズを指定しないとき" },
  de: { name: "Bildauflösung", auto: "Automatisch", sub: "keine Größe nennt" },
};

function serve(lang, saved, own) {
  const settings = { lang, theme: "light", redact: true, imageGen: "", imageGenAuto: "codex/gpt-image-2",
    imageGenModels: [{ id: "codex/gpt-image-2", name: "GPT Image 2", provider: "codex", providerName: "Codex", icon: "openai" }], ...own };
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
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

async function open(engine, lang, width, saved, own, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, saved, own));
  await watchSettingsReads(page);
  await page.goto("http://magpie.test/?view=settings&tab=models");
  await settingsDrawn(page); // rows found are the ones that stay (#1659)
  await page.locator("#imagesList #imageTimeoutRow").waitFor();
  return { page, errors };
}

const waitSaved = async (page, saved, n) => {
  for (let i = 0; i < 200 && saved.length < n; i++) await page.waitForTimeout(50);
};

async function pick(page, name) {
  const b = page.locator("#imageResolutionRow .sess-pick");
  await reach(page, b);
  await b.click();
  const menu = page.locator(".proto-menu");
  await menu.waitFor();
  const item = menu.locator(".pm-item", { has: page.locator(".pm-name", { hasText: new RegExp(`^${name}$`) }) });
  await item.click();
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) for (const width of [1100, 420]) {
    const w = words[lang];
    test(`${engine} ${lang} ${width}: how large an image is drawn`, async (t) => {
      const saved = [];
      const { page, errors } = await open(engine, lang, width, saved, {}, t);
      const row = page.locator("#imagesList #imageResolutionRow");
      await row.waitFor();
      assert.equal(await row.locator(".name").textContent(), w.name);
      assert((await row.locator(".sub").textContent()).includes(w.sub));
      assert.equal(await row.evaluate((r) => r.nextElementSibling?.id), "imageTimeoutRow", "above the timeout");
      assert.equal(await row.locator("select").count(), 0, "no native select");
      assert.equal((await row.locator(".sess-pick").textContent()).trim(), w.auto, "Automatic at first");
      const fit = await row.locator(".sess-pick").evaluate((e) => e.getBoundingClientRect().right <= innerWidth + 1);
      assert(fit, `the picker fits at ${width}`);

      const b = row.locator(".sess-pick");
      await reach(page, b);
      await b.click();
      const menu = page.locator(".proto-menu");
      await menu.waitFor();
      assert.deepEqual(await menu.locator(".pm-name").allTextContents(), [w.auto, "1K", "2K", "4K"]);
      assert.deepEqual(await menu.locator(".pm-item .pm-note").allTextContents(), ["", "", "", ""], "no notes, the names say it");
      assert.equal(await menu.locator(".pm-item[aria-checked=true] .pm-name").textContent(), w.auto);
      const inside = await menu.evaluate((m) => { const r = m.getBoundingClientRect(); return r.left >= -1 && r.right <= innerWidth + 1; });
      assert(inside, `the menu fits at ${width}`);
      await menu.locator(".pm-item", { has: page.locator(".pm-name", { hasText: /^4K$/ }) }).click();
      await waitSaved(page, saved, 1);
      assert.equal(saved.length, 1, "saved once");
      assert.equal(saved[0].imageResolution, "4K");
      assert.equal(saved[0].redact, true, "the rest kept");
      assert.equal(saved[0].lang, lang);
      await page.waitForFunction(() => document.querySelector("#imageResolutionRow .sess-pick")?.textContent.trim() === "4K", null, { timeout: 10000 });

      await pick(page, w.auto);
      await waitSaved(page, saved, 2);
      assert.equal(saved.length, 2, JSON.stringify(saved));
      assert.equal(saved[1].imageResolution, "");
      assert(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth + 1), `no sideways scroll at ${width}px`);
      assert.deepEqual(errors, []);
    });
  }
  test(`${engine}: other saves leave the tier set alone, and the row goes with image generation off`, async (t) => {
    const saved = [];
    const { page, errors } = await open(engine, "en", 1100, saved, { imageResolution: "2K" }, t);
    assert.equal((await page.locator("#imageResolutionRow .sess-pick").textContent()).trim(), "2K");
    await page.evaluate(() => savePrefs({ ...prefsKeep(prefs), redact: false }));
    await waitSaved(page, saved, 1);
    // the server keeps what a save doesn't send (TestSettingsImageResolution)
    assert(!("imageResolution" in saved[0]), JSON.stringify(saved));
    const off = [];
    const o = await open(engine, "en", 1100, off, { imageGen: "off" }, t);
    assert.equal(await o.page.locator("#imageResolutionRow").count(), 0);
    assert.deepEqual([...errors, ...o.errors], []);
  });
}
