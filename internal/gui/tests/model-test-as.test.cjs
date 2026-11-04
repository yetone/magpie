// Run with Node's test runner and Playwright on the module path; see README.md.
// 耍赖天都爱 on Discord (模型测试的时候有的必须是codex 客户端或者claude 客户端):
// a relay that serves only Codex or Claude Code turns magpie's own test
// request away, while the agent's requests through magpie are served. A
// model chip's right-click offers "Test as Codex" and "Test as Claude
// Code" where the provider says it can be asked so (testsAs), posting
// provider/test with that model and `as`; one that can't be yet is off,
// saying why. A provider that offers neither shows neither. Narrow, as the
// panel is, in every language, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const NOT_YET = "no Claude Code request has come through magpie yet: run Claude Code through magpie once, then test again";
const base = { icon: "generic", responses: "", anthropic: "", catalog: "", agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "" };
const relay = { ...base, id: "relay", name: "Relay", chat: "https://relay.example.com/v1", anthropic: "https://relay.example.com",
  models: [{ id: "gpt-5.5", name: "gpt-5.5", on: true }], testsAs: { codex: "", "claude-code": NOT_YET } };
const plain = { ...base, id: "plain", name: "Plain", chat: "https://plain.example.com/v1",
  models: [{ id: "model-p", name: "model-p", on: true }] };

function serve(lang, tests) {
  const providers = { providers: [relay, plain], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/provider/test") {
      const body = route.request().postDataJSON();
      tests.push(body);
      return json({ results: body.test.map((model) => ({ ok: true, ms: 123, model, protocol: "responses" })) });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: a model is tested as Codex or Claude Code asks`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 420, height: 900 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], tests = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, tests));
      await page.goto("http://magpie.test/?view=providers");
      const tr = (k, vars = {}) => page.evaluate(([k, vars]) => t(k, vars), [k, vars]);
      if (lang !== "en") {
        const missing = await page.evaluate(([lang, keys]) => keys.filter((k) => !I18N[lang]?.[k]), [lang,
          ["Test as Codex", "Test as Claude Code", "Asked as {agent} asks, for a relay that serves only {agent}", NOT_YET]]);
        assert.deepEqual(missing, [], `every string has its ${lang}`);
      }
      const menu = page.locator(".pop.row-menu");
      // a right-click once the editor has settled: a scroll closes the menu
      const rightClick = async (c) => {
        await c.scrollIntoViewIfNeeded();
        await page.waitForTimeout(200);
        await c.click({ button: "right" });
        await menu.waitFor();
      };

      await page.locator(".row.provider", { hasText: "Relay" }).click();
      const chip = page.locator(".editor .mchips .mchip", { hasText: "gpt-5.5" });
      await chip.waitFor();
      await rightClick(chip);
      const codex = menu.getByRole("menuitem", { name: await tr("Test as Codex") });
      const claude = menu.getByRole("menuitem", { name: new RegExp("^" + (await tr("Test as Claude Code")).replace(/[.*+?^${}()|[\]\\]/g, "\\$&")) });
      assert(await codex.isEnabled(), "Test as Codex is on");
      assert.equal(await codex.getAttribute("title"), await tr("Asked as {agent} asks, for a relay that serves only {agent}", { agent: "Codex" }));
      assert(await claude.isDisabled(), "Test as Claude Code is off until Claude Code came through");
      assert.equal(await claude.locator(".rm-why").textContent(), await tr(NOT_YET));
      // fits the narrow panel
      const box = await menu.boundingBox();
      assert(box.x >= 0 && box.x + box.width <= 420, `the menu fits: ${JSON.stringify(box)}`);
      const border = await page.evaluate(() => [...document.querySelectorAll(".pop.row-menu, .pop.row-menu *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no left-border accent");

      await codex.click();
      for (let i = 0; i < 60 && !tests.length; i++) await page.waitForTimeout(50);
      assert.deepEqual(tests.map((b) => ({ id: b.id, test: b.test, as: b.as })), [{ id: "relay", test: ["gpt-5.5"], as: "codex" }]);
      await chip.locator(".tdot.ok").waitFor();

      // Test this model asks as magpie does
      await rightClick(chip);
      await menu.getByRole("menuitem", { name: await tr("Test this model") }).click();
      for (let i = 0; i < 60 && tests.length < 2; i++) await page.waitForTimeout(50);
      assert.equal(tests[1].as, undefined, "a plain test names no client");

      // a provider that offers neither shows neither
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Plain" }).click();
      const other = page.locator(".editor .mchips .mchip", { hasText: "model-p" });
      await other.waitFor();
      await rightClick(other);
      assert.equal(await menu.getByRole("menuitem").count(), 3, "Test this model, Copy model ID and image input alone");
      assert.deepEqual(errors, []);
    });
  }
}
