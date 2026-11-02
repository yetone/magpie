// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Models › Disabled models (#1867, oxsean: a model the user
// never wants, claude-fable-5, kept being reached through a group, a
// fallback and an old session): the list says what it covers — only
// requests through magpie, on hover, as Settings' explanations are (the
// owner, 11-02) — each pattern with the models it turns off
// now and Enable; Disable… takes a model from the picker or a glob typed
// into it. Each saves the whole list on its own route, the page not moved
// by the click. In English, Chinese, Japanese and German, 1100 and 420 wide.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { head: "Disabled models", name: "Disable a model", add: "Disable…", enable: "Enable", scope: "Only requests through magpie are covered", hits: "Matches 1: anthropic/claude-fable-5" },
  zh: { head: "已停用的模型", name: "停用模型", add: "停用…", enable: "启用", scope: "只管经过 magpie 的请求", hits: "匹配 1 个：anthropic/claude-fable-5" },
  ja: { head: "無効にしたモデル", name: "モデルを無効にする", add: "無効にする…", enable: "有効にする", scope: "対象は magpie を通るリクエストだけです", hits: "1 件に一致：anthropic/claude-fable-5" },
  de: { head: "Deaktivierte Modelle", name: "Modell deaktivieren", add: "Deaktivieren …", enable: "Aktivieren", scope: "Nur Anfragen über magpie sind abgedeckt", hits: "Passt auf 1: anthropic/claude-fable-5" },
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

      // Disable…: a glob typed into the picker is taken as typed
      const before = await page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]);
      const b = await page.locator("#disabledAdd").boundingBox();
      await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
      await page.locator("#q").waitFor();
      assert.equal(await page.locator("#pop #list").evaluate((l) => l.textContent.includes("Mix")), false, "a routing group isn't offered");
      await page.locator("#q").fill("*opus*");
      await page.keyboard.press("Enter");
      for (let i = 0; i < 50 && !posts.length; i++) await page.waitForTimeout(50);
      assert.deepEqual(posts, [{ models: ["*fable*", "*opus*"] }], "the list saved whole, on its own route");
      await page.locator('.disabled-model[data-pattern="*opus*"]').waitFor();

      // Enable takes one off, the rest kept
      const en = fable.locator("button", { hasText: w.enable });
      // wheeled down to it as a reader does: the row *opus* adds, under
      // Replies' rows, puts it past the window's foot at 420 in German, and
      // under it at 1100 in Chinese with Image generation timeout drawn
      // above: reach wheels until nothing is over it
      await reach(page, en);
      const eb = await en.boundingBox();
      const at = await page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]);
      await page.mouse.click(eb.x + eb.width / 2, eb.y + eb.height / 2);
      for (let i = 0; i < 50 && posts.length < 2; i++) await page.waitForTimeout(50);
      assert.deepEqual(posts[1], { models: ["*opus*"] });
      await page.waitForFunction(() => !document.querySelector('.disabled-model[data-pattern="*fable*"]'));
      assert.deepEqual(await page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]), at, "the click moved nothing");
      assert(before.length === 3);
      assert.deepEqual(errors, []);
    });
  }
}
