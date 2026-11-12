// Run with Node's test runner and Playwright on the module path; see README.md.
// tmr11235 on X: in a provider with several accounts or keys the model test
// goes to the one routing picks, and says nothing of which. A model chip's
// right-click now offers "Test on account…", the app's own menu (never a
// native select) of the accounts or keys that are on (testPicks): the pick
// is posted as `account` with provider/test, and the status line names the
// one that was asked. A provider with one shows no such entry. Narrow, as
// the panel is, in every language, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const base = { icon: "generic", responses: "", anthropic: "", catalog: "", agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "" };
const PICKS = [{ id: "k1", name: "Work" }, { id: "k2", name: "Spare" }];
const relay = { ...base, id: "relay", name: "Relay", chat: "https://relay.example.com/v1",
  models: [{ id: "gpt-5.5", name: "gpt-5.5", on: true }], testPicks: PICKS };
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
      return json({ results: body.test.map((model) => ({ ok: true, ms: 123, model, protocol: "responses", account: (PICKS.find((k) => k.id === body.account) || PICKS[0]).name })) });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: a model is tested on the account picked`, async (t) => {
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
          ["Test on account…", "Pick the account or key the test is sent to", "Account to test"]]);
        assert.deepEqual(missing, [], `every string has its ${lang}`);
      }
      const menu = page.locator(".pop.row-menu");
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
      const entry = menu.getByRole("menuitem", { name: new RegExp("^" + (await tr("Test on account…")).replace(/[.*+?^${}()|[\]\\]/g, "\\$&")) });
      assert.equal(await entry.getAttribute("title"), await tr("Pick the account or key the test is sent to"));
      await entry.click();
      // the app's menu, not a native select
      const pick = page.locator(".pop.proto-menu");
      await pick.waitFor();
      assert.equal(await page.locator(".editor select").count(), 0, "no native select");
      assert.equal(await pick.locator(".pm-head").textContent(), await tr("Account to test"));
      assert.deepEqual(await pick.locator(".pm-name").allTextContents(), ["Work", "Spare"]);
      const box = await pick.boundingBox();
      assert(box.x >= 0 && box.x + box.width <= 420, `the menu fits: ${JSON.stringify(box)}`);
      await pick.locator(".pm-item", { hasText: "Spare" }).click();
      for (let i = 0; i < 60 && !tests.length; i++) await page.waitForTimeout(50);
      assert.deepEqual(tests.map((b) => ({ id: b.id, test: b.test, account: b.account })), [{ id: "relay", test: ["gpt-5.5"], account: "k2" }]);
      await chip.locator(".tdot.ok").waitFor();
      // the result says which was asked
      const said = await page.locator("#status").textContent();
      assert(said.endsWith(" · Spare"), said);

      // Test this model names no account: routing's pick, still named
      await rightClick(chip);
      await menu.getByRole("menuitem", { name: await tr("Test this model") }).click();
      for (let i = 0; i < 60 && tests.length < 2; i++) await page.waitForTimeout(50);
      assert.equal(tests[1].account, undefined, "a plain test names no account");
      await page.waitForFunction(() => document.querySelector("#status")?.textContent.endsWith(" · Work"));

      // a provider with one account has no such entry
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Plain" }).click();
      const other = page.locator(".editor .mchips .mchip", { hasText: "model-p" });
      await other.waitFor();
      await rightClick(other);
      assert.equal(await menu.getByRole("menuitem", { name: await tr("Test on account…") }).count(), 0);
      assert.deepEqual(errors, []);
    });
  }
}
