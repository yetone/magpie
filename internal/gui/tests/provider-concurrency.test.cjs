// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider's Max concurrent requests (Discord, Lemon): its editor has the
// field, empty for none set; Save posts the number typed (0 for no limit),
// null when it is empty, and refuses one that isn't a whole number before
// anything is posted. A signed-in account (Codex) opens on what it has, and
// a plugin's provider shows what its plugin says as the placeholder. A
// click in the field leaves the page where it is. In English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
// the editor folds what few need under Advanced, this field among them:
// opened as a reader would, unless something set in it opened it already
const unfold = async (page) => {
  const adv = page.locator(".editor details.advanced");
  await adv.waitFor({ state: "attached" });
  if (!(await adv.evaluate((d) => d.open))) await adv.locator(":scope > summary").click();
};
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", maxConcurrency: null,
};
const codex = {
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS", logins: [{ user: "me@example.com", plan: "PLUS", active: true, on: true }] },
  proxy: "", maxConcurrency: 5,
};
const lemon = {
  id: "lemon", name: "Lemon", icon: "generic", chat: "plugin://lemon/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "m1", name: "M1", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "plugin", agentName: "Lemon", user: "lemon@example.com", plan: "" },
  proxy: "", maxConcurrency: null, pluginConcurrency: 4,
};

function serve(lang, posts) {
  const providers = { providers: [relay, codex, lemon], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/provider/") && route.request().method() === "POST") {
      posts.push({ action: url.pathname.slice("/api/provider/".length), body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { label: "Requests at once", save: "Save", none: "No limit", plugin: "4, as its plugin says", hint: "Over it, requests queue and go out in order; 0 or empty is no limit", bad: "Requests at once: a whole number from 0 to 1000" },
  zh: { label: "同时请求数", save: "保存", none: "不限制", plugin: "4（插件的默认值）", hint: "超出时排队，按顺序发出；0 或留空为不限制", bad: "同时请求数：请填写 0 到 1000 之间的整数" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    const open = async (t, name, scheme = "light") => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${name}-concurrency.png`) });
        }
        await browser.close();
      });
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce", colorScheme: scheme })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posts = [];
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: name }).click();
      await unfold(page);
      await page.locator(".editor input.concurrency").waitFor();
      await page.waitForTimeout(300); // the editor drawn again as what it asked for comes in
      return { page, errors, posts };
    };
    const box = (page) => page.locator(".editor input.concurrency");
    // a click in the field, the page left where it was
    const click = async (page) => {
      const b = box(page);
      await b.scrollIntoViewIfNeeded();
      await page.waitForTimeout(200);
      const before = await b.evaluate((e) => e.getBoundingClientRect().top);
      await b.click();
      await page.waitForTimeout(250);
      const after = await b.evaluate((e) => e.getBoundingClientRect().top);
      assert(Math.abs(after - before) <= 1, `the field moved from ${before} to ${after}`);
    };
    const save = async (page, posts) => {
      const n = posts.length;
      await page.locator(".editor .bar").getByRole("button", { name: w.save, exact: true }).click();
      for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
      return posts.at(-1);
    };

    test(`${engine} ${lang}: a provider's max concurrent requests are typed and saved`, async (t) => {
      const { page, errors, posts } = await open(t, "Relay");
      assert.equal(await page.locator(".editor label", { hasText: w.label }).count(), 1);
      assert.equal(await box(page).inputValue(), "", "none set");
      assert.equal(await box(page).getAttribute("placeholder"), w.none);
      assert.equal(await box(page).evaluate((e) => e.parentElement.querySelector(".hint")?.textContent), w.hint);

      // not a whole number: refused, nothing posted
      await click(page);
      await box(page).fill("2.5");
      await page.locator(".editor .bar").getByRole("button", { name: w.save, exact: true }).click();
      await page.locator(".editor .editor-error").waitFor();
      assert.equal(await page.locator(".editor .editor-error").textContent(), w.bad);
      assert.equal(posts.length, 0);

      await box(page).fill("3");
      let saved = await save(page, posts);
      assert.equal(saved.action, "save");
      assert.equal(saved.body.id, "relay");
      assert.equal(saved.body.maxConcurrency, 3);

      // emptied again: none set
      await page.locator(".row.provider", { hasText: "Relay" }).click();
      await unfold(page);
      await box(page).fill("");
      saved = await save(page, posts);
      assert(Object.hasOwn(saved.body, "maxConcurrency"), "an empty field is saved as none, not left out");
      assert.equal(saved.body.maxConcurrency, null);

      const missing = await page.evaluate(() => [
        "Requests at once", "No limit", "{n}, as its plugin says",
        "Over it, requests queue and go out in order; 0 or empty is no limit",
        "Over it, requests queue and go out in order; empty takes the plugin's {n}, 0 is no limit",
        "Requests at once: a whole number from 0 to 1000",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      // a left border thicker or another colour than the right one is a stripe
      const stripes = await page.evaluate(() => [...document.querySelectorAll(".editor input.concurrency, .editor input.concurrency ~ *")].map((e) => getComputedStyle(e)).filter((s) => parseFloat(s.borderLeftWidth) > parseFloat(s.borderRightWidth) || (parseFloat(s.borderLeftWidth) > 0 && s.borderLeftColor !== s.borderRightColor)).length);
      assert.equal(stripes, 0, "no border stripes");
      assert.deepEqual(errors, []);
    });

    // Discord, ARNO: in the dark the field was a white box (the editor styled
    // text, password and url inputs, not a number one) and its label ran to
    // two lines in the 72px label column
    for (const scheme of ["dark", "light"]) {
      test(`${engine} ${lang} ${scheme}: the field looks like the editor's other fields, its label on one line`, async (t) => {
        const { page, errors } = await open(t, "Relay", scheme);
        const look = (e) => { const s = getComputedStyle(e); return { bg: s.backgroundColor, border: s.borderTopColor, radius: s.borderTopLeftRadius, height: s.height, color: s.color }; };
        const other = await page.locator('.editor input[type="text"]').first().evaluate(look);
        assert.deepEqual(await box(page).evaluate(look), other, "styled as a text field");
        assert.equal(await box(page).evaluate((e) => e.getBoundingClientRect().width > 100), true, "wide enough for its placeholder");
        const label = page.locator(".editor label", { hasText: w.label });
        const lines = await label.evaluate((e) => Math.round(e.getBoundingClientRect().height / parseFloat(getComputedStyle(e).lineHeight || "0") || 0));
        const h = await label.evaluate((e) => e.getBoundingClientRect().height);
        const one = await page.locator(".editor label").first().evaluate((e) => e.getBoundingClientRect().height);
        assert(h <= one + 1, `the label takes ${h}px, one line is ${one}px (${lines})`);
        assert.deepEqual(errors, []);
      });
    }

    test(`${engine} ${lang}: a signed-in account's limit opens as it is and is saved`, async (t) => {
      const { page, errors, posts } = await open(t, "Codex");
      assert.equal(await box(page).inputValue(), "5");
      await click(page);
      await box(page).fill("0");
      const saved = await save(page, posts);
      assert.equal(saved.action, "save");
      assert.equal(saved.body.id, "codex");
      assert.equal(saved.body.maxConcurrency, 0, "0 is no limit, saved as such");
      assert.deepEqual(saved.body.models, ["gpt-6"], "its picks go with it");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a plugin's provider shows what its plugin says`, async (t) => {
      const { page, errors, posts } = await open(t, "Lemon");
      assert.equal(await box(page).inputValue(), "");
      assert.equal(await box(page).getAttribute("placeholder"), w.plugin);
      let saved = await save(page, posts);
      assert.equal(saved.body.maxConcurrency, null, "empty follows the plugin");
      await page.locator(".row.provider", { hasText: "Lemon" }).click();
      await unfold(page);
      await box(page).fill("8");
      saved = await save(page, posts);
      assert.equal(saved.body.maxConcurrency, 8);
      assert.deepEqual(errors, []);
    });
  }
}
