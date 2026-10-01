// Run with Node's test runner and Playwright on the module path; see README.md.
// Codex's subagents can start at an effort of their own (#469), its [agents]
// default_subagent_reasoning_effort. It is a square beside the subagents'
// one, not a third picker: unset it says what that means (the session's
// effort, or the subagent model's own default), it opens the effort slider
// with Default as its first stop, and a level picked is posted and lights
// the square. Claude Code's subagents (#468) have no square
// while there is no model to pick. In English and Chinese. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [{ value: "gpt-5.5", label: "GPT-5.5" }, { value: "gpt-5.4-mini", label: "GPT-5.4 mini" }];
const levels = ["low", "medium", "high", "xhigh"].map((value) => ({ value }));
const fresh = () => ({
  agents: [{
    id: "codex", name: "Codex", path: "/test/config.toml", icon: "codex-color",
    fields: [
      { key: "model", label: "model", value: "gpt-5.5", options: models },
      { key: "effort", label: "effort", value: "high", options: levels },
      { key: "subagent", label: "subagents", value: "", options: models },
      { key: "subagent_effort", label: "subagent effort", value: "", options: levels },
    ],
  },
  // Claude Code's subagents (#468): a model of magpie's, offered once it runs
  // through magpie; before, there is nothing to pick and no square
  ...[["cc-own", "opus", []], ["cc-magpie", "magpie/deepseek/pro", [{ value: "magpie/deepseek/flash", label: "DeepSeek Flash" }]]].map(([id, model, options]) => ({
    id, name: "Claude Code", path: "/test/settings.json", icon: "claudecode-color",
    fields: [{ key: "model", label: "model", value: model, options: [{ value: model }] }, { key: "subagent", label: "subagents", value: "", options }],
  }))],
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
  en: {
    unset: "subagent effort: default\nthe session's effort, or the subagent model's own default",
    title: "subagent effort", def: "default", high: "high", set: "subagent effort: high", follows: "subagents: same as model",
  },
  zh: {
    unset: "子 agent 推理强度：默认\n跟随当前会话的推理强度；子 agent 指定了模型时，用该模型的默认强度",
    title: "子 agent 推理强度", def: "默认", high: "高", set: "子 agent 推理强度：高", follows: "子 agent：同主模型",
  },
};

const codex = '.row.agent[data-id="codex"]';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: Codex's subagent effort is a square that opens the effort slider`, async (t) => {
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
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-subagent-effort.png`) });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/");
      await page.locator(codex).waitFor();

      // a square beside the subagents' one; model and effort alone are pickers
      const square = page.locator(`${codex} .extras-cell .field.extra[data-key="subagent_effort"]`);
      assert.equal(await square.count(), 1);
      assert.equal(await page.locator(`${codex} .extras-cell .field.extra[data-key="subagent"]`).count(), 1);
      assert.deepEqual(await page.locator(`${codex} .field:not(.extra)`).evaluateAll((es) => es.map((e) => e.dataset.key)), ["model", "effort"]);
      assert.equal(await square.getAttribute("aria-label"), w.unset);
      assert.equal(await square.evaluate((e) => e.classList.contains("set")), false);
      assert.equal(await page.locator('.row.agent[data-id="cc-own"] .field.extra[data-key="subagent"]').count(), 0, "a subagents square with nothing to pick");
      assert.equal(await page.locator('.row.agent[data-id="cc-magpie"] .field.extra[data-key="subagent"]').getAttribute("aria-label"), w.follows);

      // the effort slider, Default its first stop and the square's value on it
      const y = await page.evaluate(() => scrollY);
      await square.click();
      await page.locator("#effortControl:not([hidden])").waitFor();
      assert.equal(await page.evaluate(() => scrollY), y, "the click scrolled the page");
      assert.equal(await page.locator("#effortTitle").textContent(), w.title);
      assert.equal(await page.locator("#effortValue").textContent(), w.def);
      assert.equal(await page.locator("#effortTicks i").count(), 5);
      await page.locator("#effortRange").evaluate((r) => {
        r.value = "3";
        r.dispatchEvent(new Event("input", { bubbles: true }));
        r.dispatchEvent(new Event("change", { bubbles: true }));
      });
      assert.equal(await page.locator("#effortValue").textContent(), w.high);
      await page.waitForFunction(() => document.querySelector('.row.agent[data-id="codex"] .field.extra.set[data-key="subagent_effort"]'));
      await page.waitForFunction(() => document.querySelector("#status")?.textContent);
      assert.deepEqual(sets, [{ agent: "codex", field: "subagent_effort", value: "high" }]);
      assert.equal(await square.getAttribute("aria-label"), w.set);
      // the session's effort is untouched
      assert.equal(await page.locator(`${codex} .field[data-key="effort"] .v`).textContent(), w.high);
      assert.deepEqual(errors, []);
    });
  }
}
