// Run with Node's test runner and Playwright on the module path; see README.md.
// One model tested on its own (yonghe on Discord): in a provider's editor a
// model chip's right-click opens a menu, "Test this model", which posts
// provider/test with that model alone; its dot and title show how it
// answered, as Test models shows them, a second model's test keeps the
// first one's result, and the footer says which it was. The right-click
// neither picks nor unpicks the chip, and leaves the page where it is; Esc
// closes the menu. Test models still asks every one. In English and
// Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const IDS = ["gpt-5.1", "anthropic/claude-opus-4.5", "model-c"];
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: IDS.map((id) => ({ id, name: id, on: true })), agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};
const answer = (id) => id.startsWith("anthropic/")
  ? { ok: false, status: 404, error: "no such model", model: id }
  : id === "model-c"
  ? { ok: true, ms: 1234, model: id }
  : { ok: true, ms: 123, model: id };

function serve(lang, tests) {
  const providers = { providers: [relay], presets: [], excluded: [], gateway: { running: true, window: true } };
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
      return json({ results: body.test.map(answer) });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: {
    item: "Test this model", all: "Test models",
    bad: "anthropic/claude-opus-4.5 didn't answer: 404 · no such model", ok: "gpt-5.1 answered in 123 ms", okTitle: "Answered in 123 ms",
    okSeconds: "model-c answered in 1.2 s", okSecondsTitle: "Answered in 1.2 s",
    tip: "Right-click to test just this model",
  },
  zh: {
    item: "测试此模型", all: "测试模型",
    bad: "anthropic/claude-opus-4.5 没有响应：404 · no such model", ok: "gpt-5.1 在 123 毫秒内响应", okTitle: "123 毫秒内响应",
    okSeconds: "model-c 在 1.2 秒内响应", okSecondsTitle: "1.2 秒内响应",
    tip: "右键可单独测试这个模型",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a chip's right-click tests that model alone`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-model-test-one.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const tests = [];
      await page.route("**/*", serve(lang, tests));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Relay" }).click();
      await page.locator(".editor .mchips .mchip").first().waitFor();
      const chip = (id) => page.locator(".editor .mchips .mchip", { hasText: id });
      const menu = page.locator(".pop.row-menu");
      // which models, the editor's form aside (see provider-typed)
      const asked = (b) => ({ id: b.id, test: b.test });
      const wait = async (n) => { for (let i = 0; i < 60 && tests.length < n; i++) await page.waitForTimeout(50); };
      const picked = () => page.locator(".editor .mchips .mchip.on").count();

      assert((await chip("gpt-5.1").getAttribute("title") || "").includes(w.tip), "the chip says how to test it alone");

      // a right-click that neither picks the chip nor moves the page
      const tryOne = async (id) => {
        const c = chip(id);
        await c.scrollIntoViewIfNeeded();
        await page.waitForTimeout(150);
        const before = await c.evaluate((e) => e.getBoundingClientRect().top);
        const on = await picked();
        await c.click({ button: "right" });
        await menu.waitFor();
        await page.waitForTimeout(200);
        assert.equal(await c.evaluate((e) => e.getBoundingClientRect().top), before, `${id} moved on its right-click`);
        assert.equal(await picked(), on, "a right-click doesn't pick");
        const n = tests.length;
        await menu.getByRole("menuitem", { name: w.item }).click();
        await wait(n + 1);
        assert(await menu.count() === 0, "the menu closes");
        return asked(tests.at(-1));
      };

      let sent = await tryOne("anthropic/claude-opus-4.5");
      assert.deepEqual(sent, { id: "relay", test: ["anthropic/claude-opus-4.5"] }, "just that model is sent");
      await chip("anthropic/claude-opus-4.5").locator(".tdot.bad").waitFor();
      assert((await chip("anthropic/claude-opus-4.5").getAttribute("title")).startsWith("404 · no such model"));
      assert.equal(await page.locator(".editor .mchips .tdot").count(), 1, "the others weren't asked");
      assert.equal(await page.locator("#status").textContent(), w.bad);

      sent = await tryOne("gpt-5.1");
      assert.deepEqual(sent, { id: "relay", test: ["gpt-5.1"] });
      await chip("gpt-5.1").locator(".tdot.ok").waitFor();
      assert((await chip("gpt-5.1").getAttribute("title")).startsWith(w.okTitle));
      assert.equal(await chip("anthropic/claude-opus-4.5").locator(".tdot.bad").count(), 1, "the first model's result is kept");
      assert.equal(await page.locator("#status").textContent(), w.ok);

      // a model taking over 1s formats in seconds
      sent = await tryOne("model-c");
      assert.deepEqual(sent, { id: "relay", test: ["model-c"] });
      await chip("model-c").locator(".tdot.ok").waitFor();
      assert((await chip("model-c").getAttribute("title")).startsWith(w.okSecondsTitle));
      assert.equal(await page.locator("#status").textContent(), w.okSeconds);

      // Esc closes the menu, nothing sent
      await chip("gpt-5.1").click({ button: "right" });
      await menu.waitFor();
      const border = await page.evaluate(() => [...document.querySelectorAll(".pop.row-menu, .pop.row-menu *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no left-border accent");
      await page.keyboard.press("Escape");
      await menu.waitFor({ state: "detached" });
      assert.equal(tests.length, 3);
      assert(await page.locator(".editor").isVisible(), "Esc closed the menu, not the editor");

      // Test models still asks every one
      await page.locator(".editor .mfoot").getByRole("button", { name: w.all, exact: true }).click();
      await wait(3);
      assert.deepEqual(asked(tests.at(-1)), { id: "relay", test: IDS });

      const missing = await page.evaluate(() => [
        "Test this model", "Right-click to test just this model", "Right-click a model to test just it",
        "{model} answered in {took}", "{model} didn't answer: {error}", "Answered in {took}",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
