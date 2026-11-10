// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Models › Disabled models (#1867, oxsean: a model the user
// never wants, claude-fable-5, kept being reached through a group, a
// fallback and an old session): the list says what it covers — only
// requests through magpie, on hover, as Settings' explanations are (the
// owner, 11-02) — each pattern with the models it turns off
// now and Enable; Disable… takes models from a picker that stays open,
// ticked to disable and clicked again to enable, or a glob typed into it,
// and Enable all clears the list (char1es_lu on X). Each saves the whole
// list on its own route, the page not moved by the click. In English, Chinese, Japanese and German, 1100 and 420 wide.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { head: "Disabled models", name: "Disable a model", add: "Disable…", enable: "Enable", scope: "Only requests through magpie are covered", hits: "Matches 1: anthropic/claude-fable-5", all: "Enable all" },
  zh: { head: "已停用的模型", name: "停用模型", add: "停用…", enable: "启用", scope: "只管经过 magpie 的请求", hits: "匹配 1 个：anthropic/claude-fable-5", all: "全部启用" },
  ja: { head: "無効にしたモデル", name: "モデルを無効にする", add: "無効にする…", enable: "有効にする", scope: "対象は magpie を通るリクエストだけです", hits: "1 件に一致：anthropic/claude-fable-5", all: "すべて有効にする" },
  de: { head: "Deaktivierte Modelle", name: "Modell deaktivieren", add: "Deaktivieren …", enable: "Aktivieren", scope: "Nur Anfragen über magpie sind abgedeckt", hits: "Passt auf 1: anthropic/claude-fable-5", all: "Alle aktivieren" },
};

function serve(lang, posts) {
  const settings = {
    lang, theme: "light", redact: true,
    disabledModels: ["*fable*"],
    disabledHits: { "*fable*": ["anthropic/claude-fable-5"] },
    titleModels: [
      { id: "fake/m1", name: "M1", provider: "fake", providerName: "Fake", icon: "generic" },
      { id: "group/mix", name: "Mix", provider: "", providerName: "Mix", icon: "generic" },
    ],
  };
  const state = { agents: [], profiles: [], settings };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings/disabled-models") {
      const body = JSON.parse(r.request().postData());
      posts.push(body);
      settings.disabledModels = body.models;
      settings.disabledHits = Object.fromEntries(body.models.map((m) => [m, []]));
      return json(settings);
    }
    if (url.pathname === "/api/settings") {
      if (r.request().method() === "POST") posts.push({ whole: JSON.parse(r.request().postData()) });
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
    test(`${engine} ${lang} ${width}: models are disabled and enabled`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 900 } })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posts = [];
      await page.route("**/*", serve(lang, posts));
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/?view=settings&tab=models");
      await settingsDrawn(page);
      const add = page.locator("#disabledModelsList #disabledAddRow");
      const fable = page.locator('#disabledModelsList .disabled-model[data-pattern="*fable*"]');
      await fable.waitFor();
      // scrolled to as a reader does, with the wheel
      await page.mouse.move(width / 2, 450);
      for (let i = 0; i < 60; i++) {
        const box = await fable.boundingBox();
        if (box && box.y >= 0 && box.y + box.height <= 900) break;
        await page.mouse.wheel(0, 120);
        await page.waitForTimeout(50);
      }
      assert.equal(await page.locator("#disabledModelsList").evaluate((l) => l.previousElementSibling.textContent.trim()), w.head);
      assert.equal(await add.locator(".name").textContent(), w.name);
      assert((await add.locator(".sub").textContent()).includes(w.scope), "the scope is said");
      assert.equal(await fable.locator(".name").textContent(), "*fable*");
      assert.equal(await fable.locator(".sub").textContent(), w.hits);
      for (const part of [`#disabledAddRow .name`, `#disabledAddRow .sub`, `#disabledAdd`, `.disabled-model .name`, `.disabled-model .sub`, `.disabled-model button`]) {
        const fit = await page.locator("#disabledModelsList " + part).first().evaluate((e) => {
          // the add row's line explains it: out of sight at rest, whole in
          // the hover card (settings-hints.test.cjs)
          if (getComputedStyle(e).getPropertyValue("--hint").trim() === "1") return "hint";
          const r = e.getBoundingClientRect();
          return e.scrollWidth <= e.clientWidth + 1 && r.width > 0 && r.right <= innerWidth + 1;
        });
        if (part === "#disabledAddRow .sub") assert.equal(fit, "hint", `${part} shows at rest at ${width}`);
        else assert.equal(fit, true, `${part} fits at ${width}`);
      }

      assert.equal(await page.locator("#disabledEnableAll").count(), 0, "Enable all waits for two");

      // Disable…: a picker that takes several (char1es_lu on X); a glob
      // typed into it is taken as typed, each model clicked is ticked and
      // disabled, one clicked again enabled, the picker open throughout
      const before = await page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]);
      const b = await page.locator("#disabledAdd").boundingBox();
      await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
      await page.locator("#q").waitFor();
      assert.equal(await page.locator("#pop #list").evaluate((l) => l.textContent.includes("Mix")), false, "a routing group isn't offered");
      const row = (v) => page.locator("#pop #list li").filter({ has: page.locator(".v", { hasText: new RegExp(`^${v.replace(/[*/]/g, "\\$&")}$`) }) });
      assert.equal(await row("*fable*").getAttribute("class").then((c) => / cur\b/.test(c)), true, "a pattern disabled already is ticked");
      const saved = async (n) => { for (let i = 0; i < 50 && posts.length < n; i++) await page.waitForTimeout(50); };
      await page.locator("#q").fill("*opus*");
      await page.keyboard.press("Enter");
      await saved(1);
      assert.deepEqual(posts, [{ models: ["*fable*", "*opus*"] }], "the list saved whole, on its own route");
      await row("M1").click();
      await saved(2);
      assert.deepEqual(posts[1], { models: ["*fable*", "*opus*", "fake/m1"] });
      await row("*opus*").click();
      await saved(3);
      assert.deepEqual(posts[2], { models: ["*fable*", "fake/m1"] }, "clicked again, it is enabled");
      assert.equal(await page.locator("#pop").isVisible(), true, "the picker stays open");
      await page.waitForFunction(() => document.querySelector('#pop #list li.cur') && [...document.querySelectorAll("#pop #list li.cur .v")].map((e) => e.textContent).join() === "*fable*,M1");
      await page.keyboard.press("Escape");
      await page.locator('.disabled-model[data-pattern="fake/m1"]').waitFor();
      // the last pick's list drawn, after the one before it
      await page.waitForFunction(() => !document.querySelector('.disabled-model[data-pattern="*opus*"]'));

      // Enable takes one off, the rest kept
      const en = fable.locator("button", { hasText: w.enable });
      // wheeled down to it as a reader does: reach wheels until nothing is
      // over it
      await reach(page, en);
      const eb = await en.boundingBox();
      const at = await page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]);
      await page.mouse.click(eb.x + eb.width / 2, eb.y + eb.height / 2);
      await saved(4);
      assert.deepEqual(posts[3], { models: ["fake/m1"] });
      await page.waitForFunction(() => !document.querySelector('.disabled-model[data-pattern="*fable*"]'));
      assert.deepEqual(await page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]), at, "the click moved nothing");

      // Enable all, with two or more, takes every one off
      await reach(page, page.locator("#disabledAdd"));
      await page.locator("#disabledAdd").click();
      await page.locator("#q").fill("*opus*");
      await page.keyboard.press("Enter");
      await saved(5);
      await page.keyboard.press("Escape");
      const all = page.locator("#disabledEnableAll");
      await all.waitFor();
      assert.equal(await all.textContent(), w.all);
      for (const part of ["#disabledAdd", "#disabledEnableAll"]) {
        const fit = await page.locator(part).evaluate((e) => {
          const r = e.getBoundingClientRect();
          return e.scrollWidth <= e.clientWidth + 1 && r.width > 0 && r.right <= innerWidth + 1;
        });
        assert.equal(fit, true, `${part} fits at ${width}`);
      }
      await reach(page, all);
      await all.click();
      await saved(6);
      assert.deepEqual(posts[5], { models: [] });
      await page.waitForFunction(() => !document.querySelector(".disabled-model"));
      assert(before.length === 3);
      assert.deepEqual(errors, []);
    });
  }
}
