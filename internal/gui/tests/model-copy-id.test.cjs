// Run with Node's test runner and Playwright on the module path; see README.md.
// A model's id copied from its chip (ARNO on Discord: 希望provider的模型右击菜单
// 除了测试模型外，还能添加拷贝模型id的功能): in a provider's editor a model
// chip's right-click menu has "Copy model ID" under "Test this model", which
// puts that model's id — a prefixed one and one picked by hand alike — on the
// clipboard through magpie (/api/copy), says so in the footer, tests nothing,
// neither picks nor unpicks the chip and leaves the page where it is. It is
// there where the model can't be tested too. In English and Chinese,
// Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
// by-hand/model-x is a pick the vendor's list doesn't have, which magpie
// lists first, as picked
const IDS = ["by-hand/model-x", "gpt-5.1", "zai/glm-5.3-flash", "cline-free/mimo-v2.6-flash"];
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: IDS.map((id) => ({ id, name: id === "zai/glm-5.3-flash" ? "GLM 5.3 Flash" : id, on: id !== "gpt-5.1" })), agents: [], fallback: [], headers: {},
  chosen: ["zai/glm-5.3-flash", "cline-free/mimo-v2.6-flash", "by-hand/model-x"],
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};
// one reached through its own API: its models can't be tested, and can
// still be copied
const account = {
  id: "acct", name: "Account", icon: "generic", chat: "", responses: "", anthropic: "", catalog: "", modelTest: "own-api",
  models: [{ id: "acct/model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: false }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};

function serve(lang, copies, tests) {
  const providers = { providers: [relay, account], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/copy") {
      copies.push(route.request().postDataJSON().text);
      return json({});
    }
    if (url.pathname === "/api/provider/test") {
      tests.push(route.request().postDataJSON());
      return json({ results: [] });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { copy: "Copy model ID", test: "Test this model", said: (id) => `Model ID ${id} copied` },
  zh: { copy: "复制模型 ID", test: "测试此模型", said: (id) => `已复制模型 ID ${id}` },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a chip's right-click copies the model's id`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-model-copy-id.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const copies = [], tests = [];
      await page.route("**/*", serve(lang, copies, tests));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Relay" }).click();
      await page.locator(".editor .mchips .mchip").first().waitFor();
      const chip = (text) => page.locator(".editor .mchips .mchip", { hasText: text });
      const menu = page.locator(".pop.row-menu");
      const picked = () => page.locator(".editor .mchips .mchip.on").count();
      const wait = async (n) => { for (let i = 0; i < 60 && copies.length < n; i++) await page.waitForTimeout(50); };

      const copyOf = async (text) => {
        const c = chip(text);
        await c.scrollIntoViewIfNeeded();
        await page.waitForTimeout(150);
        const before = await c.evaluate((e) => e.getBoundingClientRect().top);
        const scroll = await page.evaluate(() => [...document.querySelectorAll("*")].map((e) => e.scrollTop).join());
        const on = await picked();
        await c.click({ button: "right" });
        await menu.waitFor();
        const names = await menu.getByRole("menuitem").allTextContents();
        assert.equal(names[0], w.test, "testing stays first");
        assert(names.includes(w.copy), `the menu has ${w.copy}: ${names}`);
        const n = copies.length;
        await menu.getByRole("menuitem", { name: w.copy }).click();
        await wait(n + 1);
        await menu.waitFor({ state: "detached" });
        assert.equal(await picked(), on, "copying doesn't pick");
        assert.equal(await c.evaluate((e) => e.getBoundingClientRect().top), before, `${text} moved`);
        assert.equal(await page.evaluate(() => [...document.querySelectorAll("*")].map((e) => e.scrollTop).join()), scroll, "nothing scrolled");
        return copies.at(-1);
      };

      // the id, not the name it is shown by
      assert.equal(await copyOf("GLM 5.3 Flash"), "zai/glm-5.3-flash");
      assert.equal(await page.locator("#status").textContent(), w.said("zai/glm-5.3-flash"));
      assert.equal(await copyOf("cline-free/mimo-v2.6-flash"), "cline-free/mimo-v2.6-flash");
      assert.equal(await copyOf("gpt-5.1"), "gpt-5.1");
      // one picked by hand, which the vendor's list doesn't have
      assert.equal(await copyOf("by-hand/model-x"), "by-hand/model-x");
      assert.equal(tests.length, 0, "copying tests nothing");

      // no left-border accent on the menu
      await chip("gpt-5.1").click({ button: "right" });
      await menu.waitFor();
      const border = await page.evaluate(() => [...document.querySelectorAll(".pop.row-menu, .pop.row-menu *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no left-border accent");
      await page.keyboard.press("Escape");
      await menu.waitFor({ state: "detached" });

      // a model that can't be tested can still be copied
      await page.keyboard.press("Escape");
      await page.locator(".row.provider", { hasText: "Account" }).click();
      await page.locator(".editor .mchips .mchip", { hasText: "Model A" }).waitFor();
      await chip("Model A").click({ button: "right" });
      await menu.waitFor();
      assert(!(await menu.getByRole("menuitem", { name: w.test }).isEnabled()), "this one can't be tested");
      const copyItem = menu.getByRole("menuitem", { name: w.copy });
      assert(await copyItem.isEnabled(), "Copy model ID is on");
      await copyItem.click();
      await wait(5);
      assert.equal(copies.at(-1), "acct/model-a");
      assert.deepEqual(errors, []);
    });
  }
}
