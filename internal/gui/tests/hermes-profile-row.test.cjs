// Run with Node's test runner and Playwright on the module path; see README.md.
// Hermes is one row in the Agents list (geekbb on X); its other profiles
// (hermes#<name>) are the Library's, never rows here. The row says it starts on the model picked here as the
// Hermes row does, and the picker posts the encoded id (a raw # would be
// the URL's fragment). In every language, in both engines. No backend: the
// API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [{ value: "magpie/v/glm", label: "GLM", ref: "v/glm" }, { value: "magpie/v/flash", label: "Flash", ref: "v/flash" }];
const hermes = (id, bots) => ({
  id, name: "Hermes", path: "/test/.hermes/config.yaml", icon: "hermes", wired: true,
  fields: [
    { key: "model", label: "model", value: "magpie/v/glm", options: models },
    ...bots.map((b) => ({ key: "profile:" + b, label: b, value: b === "coder" ? "kimi-k3.5" : "magpie/v/glm", quiet: true, options: [{ value: "kimi-k3.5" }, ...models] })),
  ],
});
const fresh = () => ({
  agents: [
    { id: "hermes", name: "Hermes Agent", path: "/test/.hermes/config.yaml", icon: "hermes", wired: true, fields: [{ key: "model", label: "model", value: "magpie/v/glm", options: models }] },
  ],
  profiles: [],
});

function server(lang, sets) {
  let cur = fresh();
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { ...cur, settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/set") {
      const body = req.postDataJSON();
      sets.push(body);
      cur = JSON.parse(JSON.stringify(cur));
      cur.agents.find((a) => a.id === body.agent).fields.find((f) => f.key === body.field).value = body.value;
      return route.fulfill({ json: { ...cur, settings: { lang, theme: "light" } } });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/agents/cli") return route.fulfill({ json: { agents: {}, pending: false } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const W = {
  en: "Connected · starts on the model picked here",
  zh: "已接入 · 用这里选的模型启动",
  "zh-TW": "已接入 · 用這裡選的模型啟動",
  ja: "接続済み · ここで選んだモデルで起動",
  de: "Verbunden · startet mit dem hier gewählten Modell",
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(W)) {
    test(`${engine} ${lang}: Hermes is one row, its profiles are none`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage({ viewport: { width: 560, height: 800 } });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], sets = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, sets));
      t.after(() => browser.close());
      await page.goto("http://magpie.test/");
      const row = '.row.agent[data-id="hermes"]';
      await page.locator(row).waitFor();
      // geekbb on X: one entry for Hermes; its bots (profiles) are not rows
      assert.equal(await page.locator('.row.agent[data-id^="hermes"]').count(), 1);
      assert.ok(!(await page.locator("#agents, body").first().innerText()).includes("Hermes · "));
      assert.ok((await page.locator(row).innerText()).includes(W[lang]), await page.locator(row).innerText());
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "the Agents view overflows");
      assert.deepEqual(errors, []);
    });
  }
}
