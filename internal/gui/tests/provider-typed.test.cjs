// Run with Node's test runner and Playwright on the module path; see README.md.
// A key pasted over a provider's saved one is tried before a Save (the user:
// Refresh and Test only knew the saved key, and Save closes the editor):
// Refresh, Test models, a model's own test and the endpoints' Test send the
// editor's form with typed: true, the new key with it, and the editor stays
// open with the key still in its box; nothing is saved by them. And a new
// key of a provider's: its box focuses the key, the name says it's optional.
// In English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const relay = {
  id: "relay", name: "Relay", preset: "relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-5.1", name: "gpt-5.1", on: true }], agents: [], fallback: [], headers: { "X-Team": "a" },
  key: { set: true, masked: "sk-…one" }, keyList: [{ id: "k1", name: "", masked: "sk-…one", on: true, active: true }],
  balanceToken: { takes: false, set: false }, proxy: "",
};

function serve(lang, calls) {
  const providers = { providers: [relay], presets: [{ id: "relay", name: "Relay", icon: "generic", chat: relay.chat, added: true }], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/provider/")) {
      const body = route.request().postDataJSON();
      calls.push([url.pathname.slice(14), body]);
      if (url.pathname === "/api/provider/models") return json({ count: 1, provider: relay });
      if (url.pathname === "/api/provider/test") return json({ results: body.test ? body.test.map((model) => ({ ok: true, ms: 9, model })) : [{ protocol: "chat", ok: true, ms: 9, model: "gpt-5.1" }] });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { refresh: "Refresh", all: "Test models", test: "Test", item: "Test this model", addKey: "Add another key", name: "Name (optional), e.g. Team", optional: "optional" },
  zh: { refresh: "刷新", all: "测试模型", test: "测试", item: "测试此模型", addKey: "添加另一个密钥", name: "名称（可选），例如 团队", optional: "可选" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: Refresh and Test try the key just pasted`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 900 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const calls = [];
      await page.route("**/*", serve(lang, calls));
      await page.goto("http://magpie.test/?view=providers");
      const missing = await page.evaluate((w) => Object.values(w).filter((s) => /[a-z]/i.test(s) && !I18N.zh[s] && !["optional"].includes(s)), words.en);
      if (lang === "en") assert.deepEqual(missing, [], "every string has its Chinese");
      await page.locator(".row.provider", { hasText: "Relay" }).click();
      const ed = page.locator(".editor");
      const key = ed.locator('input[type="password"]').first();
      await key.fill("sk-new-typed");
      const wait = async (n) => { for (let i = 0; i < 60 && calls.length < n; i++) await page.waitForTimeout(50); };
      const form = (b) => ({ typed: b.typed, key: b.key, chat: b.chat, headers: b.headers });
      const want = { typed: true, key: "sk-new-typed", chat: "https://relay.example.com/v1", headers: { "X-Team": "a" } };

      await ed.locator(".mfoot").getByRole("button", { name: w.refresh, exact: true }).click();
      await wait(1);
      assert.equal(calls[0][0], "models");
      assert.deepEqual(form(calls[0][1]), want, "Refresh asks with the form");
      await page.waitForTimeout(200);
      assert.ok(await ed.isVisible(), "the editor stays open");
      assert.equal(await ed.locator('input[type="password"]').first().inputValue(), "sk-new-typed", "the key stays typed");

      await ed.locator(".mfoot").getByRole("button", { name: w.all, exact: true }).click();
      await wait(2);
      assert.deepEqual([calls[1][0], calls[1][1].test], ["test", ["gpt-5.1"]]);
      assert.deepEqual(form(calls[1][1]), want, "Test models asks with the form");

      await ed.locator(".mchips .mchip", { hasText: "gpt-5.1" }).click({ button: "right" });
      await page.locator(".pop.row-menu").getByRole("menuitem", { name: w.item }).click();
      await wait(3);
      assert.deepEqual(form(calls[2][1]), want, "a model's own test asks with the form");

      await ed.locator(".eps").getByRole("button", { name: w.test, exact: true }).click();
      await wait(4);
      assert.equal(calls[3][1].test, undefined);
      assert.deepEqual(form(calls[3][1]), want, "the endpoints' Test asks with the form");
      assert.deepEqual(calls.map((c) => c[0]), ["models", "test", "test", "test"], "nothing saved");
      assert.ok(await ed.isVisible());

      // a provider's new key: the key is focused, the name is optional
      await ed.getByRole("button", { name: w.addKey }).click();
      const box = page.locator(".editor .acc.adding");
      await box.waitFor();
      await page.waitForTimeout(100);
      assert.equal(await page.evaluate(() => document.activeElement?.type), "password", "the key is focused");
      assert.ok(await box.locator('input[type="password"]').evaluate((e) => e === document.activeElement));
      const name = box.locator('input:not([type="password"])').first();
      assert.equal(await name.getAttribute("placeholder"), w.name);
      assert.ok((await name.getAttribute("placeholder")).includes(w.optional));
      assert.deepEqual(errors, []);
    });
  }
}
