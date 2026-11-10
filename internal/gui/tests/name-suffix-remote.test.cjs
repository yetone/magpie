// Run with Node's test runner and Playwright on the module path; see README.md.
// ARNO on Discord (我在设置里关闭了"模型名称带供应商"但是接入的remote-magpie的
// 模型名字还是带上了…): a remote magpie's models are named in its list with
// its own provider after them ("DeepSeek Chat · Relay A"), by that magpie's
// setting, and Off here kept it. Model settings says what the agents' lists
// show under each choice, saved on Settings: On, that label with this
// provider after it; Off, the model's name there alone. Every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, posts, plainNames, saves = []) {
  let cur = { lang, theme: "light", plainNames, plainOwnNames: false };
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light", plainNames } };
  const models = [
    { id: "relaya/deepseek-chat", name: "DeepSeek Chat · Relay A", plain: "DeepSeek Chat", by: "Relay A", on: true, efforts: [], images: false },
  ];
  const provider = { id: "remote-magpie", name: "Remote magpie", preset: "remote-magpie", icon: "generic", chat: "http://192.168.1.20:3425/v1", responses: "http://192.168.1.20:3425/v1", anthropic: "http://192.168.1.20:3425",
    models, agents: [], key: { set: true, masked: "sk-…1234" }, ready: true };
  return async (r) => {
    const req = r.request(), url = new URL(req.url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings/plain-names") {
      const body = req.postDataJSON();
      posts.push(body);
      cur = { ...cur, plainNames: body.mode === "off", plainOwnNames: body.mode === "own" };
      return json(cur);
    }
    if (url.pathname === "/api/settings") return json(cur);
    if (url.pathname === "/api/provider/save") { saves.push(req.postDataJSON()); return json({ id: provider.id }); }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models: [], pools: [], groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [provider], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function says(loc, want) {
  for (let i = 0; i < 100 && (await loc.textContent()) !== want; i++) await new Promise((r) => setTimeout(r, 20));
  assert.equal(await loc.textContent(), want);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: a remote magpie's model is named alone under Off`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      t.after(() => browser.close());
      // narrow, as the panel is
      const page = await (await browser.newContext({ viewport: { width: 420, height: 900 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      // Provider in model names is Settings' (joe on Discord): the editor
      // says what each saved choice shows, On and then Off
      for (const [plain, label] of [[false, "DeepSeek Chat · Relay A · Remote magpie"], [true, "DeepSeek Chat"]]) {
        await page.unrouteAll({ behavior: "wait" });
        await page.route("**/*", serve(lang, posts, plain));
        await page.goto("http://magpie.test/?view=providers");
        const tr = (k, vars = {}) => page.evaluate(([k, vars]) => t(k, vars), [k, vars]);
        await page.locator(".row.provider").click();
        if (!(await page.locator(".mnames:not([hidden])").count())) await page.getByRole("button", { name: await tr("Model settings"), exact: true }).click();
        const sfx = page.locator(".mnames:not([hidden]) .msuffix");
        await sfx.waitFor();
        await says(sfx.locator(".hint"), await tr("Agents’ lists show “{label}”", { label }));
      }
      assert.deepEqual(posts, []);
      assert.deepEqual(errors, []);
    });
  }
}

// lcc580 on X: a Remote magpie's models named by the provider serving each
// there, so one model from two of its providers is told apart here. Its
// editor's switch says what the agents' lists show, under On and Off
// alike, and is saved with the provider.
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: a remote magpie's model named by its provider there`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 420, height: 900 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      // one route, its server swapped: WebKit lost a route set again after
      // the save, and the page's address went to DNS
      let served;
      await page.route("**/*", (r) => served(r));
      for (const plain of [false, true]) {
        const saves = [];
        served = serve(lang, posts, plain, saves);
        await page.goto("http://magpie.test/?view=providers");
        const tr = (k, vars = {}) => page.evaluate(([k, vars]) => t(k, vars), [k, vars]);
        await page.locator(".row.provider").click();
        if (!(await page.locator(".mnames:not([hidden])").count())) await page.getByRole("button", { name: await tr("Model settings"), exact: true }).click();
        const sfx = page.locator(".mnames:not([hidden]) .msuffix");
        await sfx.waitFor();
        const tick = page.locator("label.upstream-providers");
        assert.equal((await tick.textContent()).trim(), await tr("With the provider serving it there"));
        assert.equal(await tick.locator("input").isChecked(), false);
        await tick.locator("input").check();
        await says(sfx.locator(".hint"), await tr("Agents’ lists show “{label}”", { label: "DeepSeek Chat · Remote magpie › Relay A" }));
        await tick.locator("input").uncheck();
        await says(sfx.locator(".hint"), await tr("Agents’ lists show “{label}”", { label: plain ? "DeepSeek Chat" : "DeepSeek Chat · Relay A · Remote magpie" }));
        await tick.locator("input").check();
        const ed = page.locator(".editor").filter({ has: tick });
        await Promise.all([page.waitForResponse((r) => r.url().endsWith("/api/provider/save")), ed.locator(".bar button.primary").first().click()]);
        assert.equal(saves.length, 1);
        assert.equal(saves[0].upstreamProviders, true);
      }
      assert.deepEqual(posts, []);
      assert.deepEqual(errors, []);
    });
  }
}
