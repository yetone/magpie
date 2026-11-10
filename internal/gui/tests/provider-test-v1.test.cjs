// Run with Node's test runner and Playwright on the module path; see README.md.
// A relay typed without the /v1 it serves OpenAI's API under (Cyrus_4433 on
// X: claude-relay-service at http://host:port/openai, every model 404 Not
// Found): Test models is answered under …/openai/v1 and says so (v1), and the
// editor's Base URL takes it, so Save keeps …/openai/v1 instead of putting
// the URL back as typed. Right-click's one-model test does the same. In
// English and Chinese, Chromium and WebKit, at 900px and 420px.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const BASE = "http://216.36.117.18:55088/openai";
const IDS = ["gpt-6.1-sol", "gpt-6-sol"];
const relay = {
  id: "my-relay", name: "My Relay", icon: "generic", chat: BASE, responses: "", anthropic: "", catalog: "",
  models: IDS.map((id) => ({ id, name: id, on: true })), chosen: IDS, agents: [], fallback: [], headers: {},
  key: { set: true, masked: "cr_1…4209" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", listOff: true,
};

function serve(lang, posts) {
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
    if (url.pathname.startsWith("/api/provider/")) {
      const body = route.request().postDataJSON();
      posts.push({ path: url.pathname, body });
      if (url.pathname === "/api/provider/test") return json({ results: body.test.map((id) => ({ ok: true, ms: 321, model: id, protocol: "chat", status: 200, v1: BASE + "/v1" })) });
      return json({});
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const width of [900, 420]) {
      for (const how of ["all", "one"]) {
        test(`${engine} ${lang} ${width}px ${how}: a test answered under /v1 sets the Base URL`, async (t) => {
          const zh = lang === "zh";
          const L = { all: zh ? "测试模型" : "Test models", item: zh ? "测试此模型" : "Test this model", save: zh ? "保存" : "Save" };
          const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
          const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
          t.after(() => browser.close());
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          const posts = [];
          await page.route("**/*", serve(lang, posts));
          await page.goto("http://magpie.test/?view=providers");
          await page.locator(".row.provider", { hasText: "My Relay" }).click();
          const ed = page.locator(".editor");
          await ed.locator(".base-url").waitFor();
          assert.equal(await ed.locator(".base-url").inputValue(), BASE);
          const tested = () => posts.filter((p) => p.path === "/api/provider/test").length;
          if (how === "all") {
            await ed.locator(".mfoot").getByRole("button", { name: L.all, exact: true }).click();
          } else {
            const chip = ed.locator(".mchips .mchip", { hasText: IDS[0] });
            await chip.scrollIntoViewIfNeeded();
            await page.waitForTimeout(150); // the editor's scroll settles (model-test-one)
            await chip.click({ button: "right" });
            await page.locator(".pop.row-menu").getByRole("menuitem", { name: L.item }).click();
          }
          for (let i = 0; i < 60 && !tested(); i++) await page.waitForTimeout(50);
          assert.equal(tested(), 1);
          await page.waitForFunction((want) => document.querySelector(".editor .base-url")?.value === want, BASE + "/v1");
          await ed.locator(":scope > .bar").getByRole("button", { name: L.save, exact: true }).click();
          for (let i = 0; i < 60 && !posts.some((p) => p.path === "/api/provider/save"); i++) await page.waitForTimeout(50);
          const save = posts.find((p) => p.path === "/api/provider/save");
          assert(save, "saved");
          assert.equal(save.body.chat, BASE + "/v1", "Save keeps the /v1 the test found");
          assert.deepEqual(errors, []);
        });
      }
    }
  }
}
