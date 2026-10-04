// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider editor's models can be let out to their full height and folded
// back (#833: 「模型」区域像设置里的 Agent 页面一样支持展开 / 收起). Held,
// the chips are a box a few lines tall, scrolled within; Expand the list
// under it shows every chip at once, and Collapse the list holds them again.
// A short list, which fits the box, isn't offered it. Opened, the next
// editor opens with its list let out too. The click scrolls nothing (the
// editor's body, the page), and the button has no border stripe. In English
// and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const fetched = new Date(Date.now() - 3600e3).toISOString();
const relay = (id, name, models, on) => ({
  id, name, icon: "generic", host: id + ".example.com", chat: `https://${id}.example.com/v1`, responses: "", anthropic: "", catalog: "",
  models, chosen: on, fetched, agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
});
const m = (id) => ({ id, name: id, on: false });
const many = relay("many", "Many Relay", Array.from({ length: 45 }, (_, i) => m(`a-rather-long-model-name-${i + 1}`)), ["a-rather-long-model-name-1"]);
const other = relay("other", "Other Relay", Array.from({ length: 40 }, (_, i) => m(`other-model-name-${i + 1}`)), []);
const few = relay("few", "Few Relay", [m("gpt-5"), m("gpt-5-mini")], ["gpt-5"]);

function serve(lang) {
  const providers = { providers: [many, other, few], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { open: "Expand the list", fold: "Collapse the list" },
  zh: { open: "展开列表", fold: "收起列表" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a provider's models are let out and folded back`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-models-fold.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=providers");

      const editor = page.locator("#modal:not([hidden]) .editor");
      const open = async (name) => {
        await page.locator(".row.provider", { hasText: name }).click();
        await editor.locator(".ehead b", { hasText: name }).waitFor();
        await editor.locator(".models > .mchips .mchip").first().waitFor();
        await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
      };
      const close = async (discard = false) => {
        await page.keyboard.press("Escape");
        if (discard) await page.locator("dialog.action-confirm[open] button").last().click();
        await page.locator("#modal").waitFor({ state: "hidden" });
      };
      const fold = editor.locator(".mchips-fold");
      const box = () => editor.locator(".models > .mchips").evaluate((c) => ({ shown: c.clientHeight, all: c.scrollHeight }));
      const scrolled = () => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop > 0 && !e.classList.contains("mchips")).map((e) => `${e.className}:${e.scrollTop}`)].join(" "));
      const settle = () => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));

      // a short list fits its box: nothing to let out
      await open("Few Relay");
      assert(await fold.isHidden(), "a short list offers Expand");
      await close();

      // a long one: held to a few lines, scrolled within
      await open("Many Relay");
      await fold.waitFor();
      assert.equal((await fold.textContent()).trim(), w.open);
      assert.equal(await fold.getAttribute("aria-expanded"), "false");
      assert(await fold.getAttribute("title"));
      const held = await box();
      assert(held.all > held.shown + 20, `held: ${JSON.stringify(held)}`);
      const border = await fold.evaluate((e) => parseFloat(getComputedStyle(e).borderLeftWidth));
      assert(!(border > 1), "no border stripe");

      // let out: every chip in sight, nothing scrolled by the click (the
      // editor's body scrolled to the button first, as a reader would)
      await editor.locator(".ebody").evaluate((b) => { b.scrollTop += b.querySelector(".mchips-fold").getBoundingClientRect().top - b.getBoundingClientRect().top - 120; });
      if (process.env.ARTIFACT_DIR) { await page.waitForTimeout(800); await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-models-fold-held.png`) }); }
      const before = await scrolled(), at = await fold.evaluate((e) => Math.round(e.getBoundingClientRect().top));
      await fold.click();
      await settle();
      const out = await box();
      assert(out.shown >= out.all - 1 && out.shown > held.shown + 20, `let out: ${JSON.stringify(out)}`);
      if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-models-fold-open.png`) });
      assert.equal((await fold.textContent()).trim(), w.fold);
      assert.equal(await fold.getAttribute("aria-expanded"), "true");
      assert.equal(await scrolled(), before, "the click scrolled");
      assert(await fold.evaluate((e) => Math.round(e.getBoundingClientRect().top)) > at, "the button goes down with the models it lets out");
      // a pick redraws the chips: still let out
      await editor.locator(".models > .mchips .mchip", { hasText: "a-rather-long-model-name-2" }).first().click();
      await settle();
      assert.equal((await box()).shown, out.all, "a pick folded the list");
      await close(true);

      // the next editor opens let out too, and folds back
      await open("Other Relay");
      await fold.waitFor();
      assert.equal((await fold.textContent()).trim(), w.fold);
      const next = await box();
      assert(next.shown >= next.all - 1, `the next editor held: ${JSON.stringify(next)}`);
      await fold.click();
      await settle();
      const back = await box();
      assert(back.all > back.shown + 20, `folded back: ${JSON.stringify(back)}`);
      assert.equal((await fold.textContent()).trim(), w.open);
      await close();

      const missing = await page.evaluate(() => ["Expand the list", "Collapse the list", "Show every model at once, not in a small box to scroll", "Hold the models to a few lines again"].filter((k) => !I18N.zh[k] || !I18N.ja[k] || !I18N.de[k]));
      assert.deepEqual(missing, [], "every string has its zh, ja and de");
      assert.deepEqual(errors, []);
    });
  }
}
