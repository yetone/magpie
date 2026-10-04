// Run with Node's test runner and Playwright on the module path; see README.md.
// Which models can be tested on their own, and why not where they can't (ARNO
// on Discord: 有些provider里的模型可以右击检测，有些却不可以，为什么？). A
// signed-in account sent requests at an endpoint (Codex) has the chip's
// right-click and Test models as a key does. One reached through its agent's
// own API (Kiro: modelTest "own-api") and a classifier ("decide") still open
// the menu on a right-click, its Test this model off with the reason under it
// and as its title, and Test models is off with the reason as its title;
// nothing is posted and the page doesn't move. A System One provider's
// model, one typed in by hand too, is tested (decideTest); Workers AI's
// isn't. In English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const base = { icon: "generic", chat: "", responses: "", anthropic: "", catalog: "", agents: [], fallback: [], headers: {}, keyList: [], proxy: "" };
const providers = [
  {
    ...base, id: "codex", name: "Codex", icon: "codex-color", responses: "https://chatgpt.com/backend-api/codex",
    models: [{ id: "gpt-6", name: "", on: true }, { id: "gpt-6-mini", name: "", on: true }],
    account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS", logins: [{ user: "me@example.com", plan: "PLUS", active: true, on: true }] },
  },
  {
    ...base, id: "kiro", name: "Kiro", icon: "kiro-color", modelTest: "own-api",
    models: [{ id: "claude-sonnet-5", name: "", on: true }, { id: "claude-haiku-5", name: "", on: true }],
    account: { agent: "kiro", agentName: "Kiro", user: "k@example.com", plan: "PRO", logins: [{ user: "k@example.com", plan: "PRO", active: true, on: true }] },
  },
  // a System One provider (OpenRouter's) with a model typed in by hand
  // (ARNO on Discord), and Workers AI, whose models can't each be asked
  {
    ...base, id: "or-decide", name: "Deciders", decide: "https://openrouter.ai/api/v1", decideTest: true, chosen: ["respan/span-01"],
    models: [{ id: "liquid/d1", name: "", on: true }, { id: "respan/span-01", name: "", on: true }], key: { set: true, masked: "sk-…one" },
  },
  {
    ...base, id: "cf-jev", name: "Workers", decide: "https://api.cloudflare.com/client/v4/accounts/a/ai/run", modelTest: "decide",
    models: [{ id: "typesafe/jev", name: "", on: true }], key: { set: true, masked: "sk-…two" },
  },
];

function serve(lang, tests) {
  const list = { providers, presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(list);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/provider/test") {
      const body = route.request().postDataJSON();
      tests.push(body);
      return json({ results: (body.test || []).map((model) => ({ ok: true, ms: 321, model })) });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: {
    item: "Test this model", all: "Test models", ok: "gpt-6-mini answered in 321 ms",
    own: "Kiro is reached through its own API, which magpie translates each agent request for, so a test request can't be sent to it on its own. Ask the model from an agent to try it.",
  },
  zh: {
    item: "测试此模型", all: "测试模型", ok: "gpt-6-mini 在 321 毫秒内响应",
    own: "Kiro 走的是它自己的接口，magpie 会为 Agent 的每个请求做转换，所以无法单独给它发测试请求。请在 Agent 里用这个模型试一下。",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    const open = async (t, name, tests) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${name}-model-test-why.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, tests));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: name }).first().click();
      await page.locator(".editor .mchips .mchip").first().waitFor();
      await page.waitForTimeout(300); // the editor is drawn again once its provider is in
      return { page, errors };
    };
    const chip = (page, id) => page.locator(".editor .mchips .mchip", { hasText: new RegExp(`^${id}`) });
    const scrolled = (page) => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop > 0).map((e) => `${e.className}:${e.scrollTop}`)].join(" "));

    test(`${engine} ${lang}: a signed-in account's model is tested from its chip`, async (t) => {
      const tests = [];
      const { page, errors } = await open(t, "Codex", tests);
      const c = chip(page, "gpt-6-mini");
      await c.scrollIntoViewIfNeeded();
      const before = await c.evaluate((e) => e.getBoundingClientRect().top), sc = await scrolled(page);
      await c.click({ button: "right" });
      const menu = page.locator(".pop.row-menu");
      await menu.waitFor();
      const item = menu.getByRole("menuitem", { name: w.item });
      assert(await item.isEnabled(), "the account's model can be tested");
      await item.click();
      for (let i = 0; i < 60 && !tests.length; i++) await page.waitForTimeout(50);
      assert.deepEqual({ id: tests[0]?.id, test: tests[0]?.test }, { id: "codex", test: ["gpt-6-mini"] });
      await c.locator(".tdot.ok").waitFor();
      assert.equal(await page.locator("#status").textContent(), w.ok);
      assert.equal(await c.evaluate((e) => e.getBoundingClientRect().top), before, "the chip moved");
      assert.equal(await scrolled(page), sc, "the page scrolled");
      const all = page.locator(".editor .mfoot").getByRole("button", { name: w.all, exact: true });
      assert(await all.isEnabled(), "Test models is offered");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a model reached through its agent's own API says why it can't be tested`, async (t) => {
      const tests = [];
      const { page, errors } = await open(t, "Kiro", tests);
      const c = chip(page, "claude-haiku-5");
      await c.scrollIntoViewIfNeeded();
      const before = await c.evaluate((e) => e.getBoundingClientRect().top), sc = await scrolled(page);
      await c.click({ button: "right" });
      const menu = page.locator(".pop.row-menu");
      await menu.waitFor();
      // Test this model, off, and Copy model ID, which is on
      const items = menu.locator(".rm-item");
      assert.equal(await items.count(), 2);
      assert(await items.nth(1).isEnabled(), "its id can still be copied");
      const item = items.first();
      assert(await item.isDisabled(), "Test this model is off");
      assert.equal(await item.getAttribute("title"), w.own);
      assert.equal(await item.locator(".rm-why").textContent(), w.own, "the reason is said in the menu");
      assert(await item.locator(".rm-why").isVisible());
      const box = await menu.boundingBox();
      assert(box.width <= 300 && box.x >= 0 && box.x + box.width <= 900, `the menu fits: ${JSON.stringify(box)}`);
      const border = await page.evaluate(() => [...document.querySelectorAll(".pop.row-menu, .pop.row-menu *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no left-border accent");
      await item.click({ force: true });
      await page.keyboard.press("Escape");
      await menu.waitFor({ state: "detached" });
      assert(await page.locator(".editor").isVisible(), "Esc closed the menu, not the editor");
      assert.equal(await c.evaluate((e) => e.getBoundingClientRect().top), before, "the chip moved");
      assert.equal(await scrolled(page), sc, "the page scrolled");
      const all = page.locator(".editor .mfoot").getByRole("button", { name: w.all, exact: true });
      assert(await all.isDisabled(), "Test models is shown, off");
      assert.equal(await all.getAttribute("title"), w.own);
      await page.waitForTimeout(150);
      assert.equal(tests.length, 0, "nothing was sent");
      const missing = await page.evaluate(() => [
        "{name} is reached through its own API, which magpie translates each agent request for, so a test request can't be sent to it on its own. Ask the model from an agent to try it.",
        "A classifier's models aren't sent test requests: Test under Endpoints asks Jev's endpoint for them.",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a System One model typed in by hand is tested from its chip`, async (t) => {
      const tests = [];
      const { page, errors } = await open(t, "Deciders", tests);
      const c = chip(page, "respan/span-01");
      await c.click({ button: "right" });
      const menu = page.locator(".pop.row-menu");
      await menu.waitFor();
      const item = menu.getByRole("menuitem", { name: w.item });
      assert(await item.isEnabled(), "the decision model can be tested");
      await item.click();
      for (let i = 0; i < 60 && !tests.length; i++) await page.waitForTimeout(50);
      assert.deepEqual({ id: tests[0]?.id, test: tests[0]?.test }, { id: "or-decide", test: ["respan/span-01"] });
      await c.locator(".tdot.ok").waitFor();
      const all = page.locator(".editor .mfoot").getByRole("button", { name: w.all, exact: true });
      assert(await all.isEnabled(), "Test models is offered");
      await all.click();
      for (let i = 0; i < 60 && tests.length < 2; i++) await page.waitForTimeout(50);
      assert.deepEqual(tests[1]?.test, ["respan/span-01"], "Test models asks the picked decision model");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a Workers AI classifier's model still says why it can't be tested`, async (t) => {
      const tests = [];
      const { page, errors } = await open(t, "Workers", tests);
      await chip(page, "typesafe/jev").click({ button: "right" });
      const menu = page.locator(".pop.row-menu");
      await menu.waitFor();
      assert(await menu.locator(".rm-item").first().isDisabled(), "Test this model is off");
      await page.keyboard.press("Escape");
      await page.waitForTimeout(150);
      assert.equal(tests.length, 0, "nothing was sent");
      assert.deepEqual(errors, []);
    });
  }
}
