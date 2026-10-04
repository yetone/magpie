// Run with Node's test runner and Playwright on the module path; see README.md.
// omp's subagents, smol and slow roles fall back to its model when unset
// (#325), so each is a square after the pickers, not a picker of its own:
// unset it says it follows the model, its picker opens on "Same as model",
// and set it names the model it is on. OpenCode's small model, which
// doesn't follow the model, stays a picker. Both read in the connected
// agent's opened row. In English and Chinese.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const options = [
  { value: "magpie/deepseek/pro", label: "DeepSeek Pro", ref: "deepseek/pro" },
  { value: "magpie/deepseek/flash", label: "DeepSeek Flash", ref: "deepseek/flash" },
];
const field = (key, label, value) => ({ key, label, value, options });
const fresh = () => ({
  agents: [
    {
      id: "omp", name: "omp", path: "/test/config.yml", icon: "omp", wired: true,
      fields: [field("model", "model", "magpie/deepseek/pro"), field("subagent", "subagents", ""), field("small", "smol", ""), field("slow", "slow", "magpie/deepseek/flash")],
    },
    { id: "opencode", name: "OpenCode", path: "/test/opencode.json", wired: true, fields: [field("model", "model", "magpie/deepseek/pro"), field("small", "small", "")] },
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

// the words as i18n.js has them
const W = {
  en: { follows: (l) => `${l}: same as model`, on: (l, m) => `${l}: ${m}`, same: "Same as model", subagents: "subagents", smol: "smol", slow: "slow" },
  zh: { follows: (l) => `${l}：同主模型`, on: (l, m) => `${l}：${m}`, same: "同主模型", subagents: "子 agent", smol: "小模型", slow: "慢模型" },
};

const omp = '.row.agent[data-id="omp"]';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: omp's roles follow the model as squares, OpenCode's small model is a picker`, async (t) => {
      const w = W[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage({ viewport: { width: 1100, height: 700 } });
      page.setDefaultTimeout(5000);
      const errors = [], sets = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, sets));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-omp-roles.png`) });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/");
      await page.locator(omp).waitFor();
      await page.locator(`${omp} .ag-link`).click();

      // the three roles are squares in the row's squares cell; the model alone is a picker
      const square = (key) => page.locator(`${omp} .extras-cell .field.extra[data-key="${key}"]`);
      for (const key of ["subagent", "small", "slow"]) assert.equal(await square(key).count(), 1, `${key} is a square`);
      assert.deepEqual(await page.locator(`${omp} .field:not(.extra)`).evaluateAll((es) => es.map((e) => e.dataset.key)), ["model"]);
      // unset: following the model; set: the model it is on
      assert.equal(await square("subagent").getAttribute("aria-label"), w.follows(w.subagents));
      assert.equal(await square("small").getAttribute("aria-label"), w.follows(w.smol));
      assert.equal(await square("small").evaluate((e) => e.classList.contains("set")), false);
      assert.equal(await square("slow").getAttribute("aria-label"), w.on(w.slow, "DeepSeek Flash"));
      assert.equal(await square("slow").evaluate((e) => e.classList.contains("set")), true);

      // smol's picker opens on following the model, the model named beside it
      await square("small").click();
      const first = page.locator("#pop:not([hidden]) #list li").first();
      await first.waitFor();
      assert.match(await first.innerText(), new RegExp(`${w.same}[\\s\\S]*DeepSeek Pro`));
      await page.locator("#pop:not([hidden]) #list li").filter({ hasText: "DeepSeek Flash" }).first().click();
      await page.waitForFunction(() => document.querySelector('.row.agent[data-id="omp"] .field.extra.set[data-key="small"]'));
      assert.deepEqual(sets, [{ ...sets[0], agent: "omp", field: "small", value: "magpie/deepseek/flash" }]);
      assert.equal(await square("small").getAttribute("aria-label"), w.on(w.smol, "DeepSeek Flash"));

      // OpenCode's small model doesn't follow the model: a picker, not a square
      const oc = '.row.agent[data-id="opencode"]';
      await page.locator(`${oc} .ag-link`).click();
      await page.locator(`${oc} .ag-exp`).waitFor();
      assert.equal(await page.locator(`${oc} .field.extra`).count(), 0);
      assert.equal(await page.locator(`${oc} .field[data-key="small"]`).count(), 1);
      assert.deepEqual(errors, []);
    });
  }
}
