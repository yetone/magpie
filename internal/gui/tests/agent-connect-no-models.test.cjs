// Run with Node's test runner and Playwright on the module path; see README.md.
// Tystem on Discord: switching Codex on with no provider or subscription in
// magpie said only "Codex can't be connected to magpie", in English whatever
// the language. The server answers that case with code no_models, and the
// toast says what to do in the reader's language; the switch goes back off.
// No click moves the page. No backend, the API is faked here.
// znuaibot (#1989): with models in magpie but none left in the agent's
// model list (its one picked model's provider switched off), the code is
// all_hidden, and the toast says to pick one; the list to pick from opens
// there (94942f9f3, #2435), which agent-connect-all-hidden.test.cjs covers.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const options = [{ value: "gpt-5.4", label: "GPT-5.4" }, { value: "relay/m1", label: "m1", ref: "relay/m1", note: "Relay · via magpie" }];
const state = { agents: [{ id: "codex", name: "Codex", icon: "generic", path: "/fixture/codex", fields: [{ key: "model", label: "model", value: "gpt-5.4", options }] }], profiles: [] };

function server(lang, posts, answer) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ ...state, settings: { lang, theme: "light", guideHidden: true } });
    if (url.pathname === "/api/agents/connect/codex") {
      posts.push(url.pathname);
      return json(answer, 400);
    }
    if (url.pathname === "/api/agent-models/codex") return json({ models: [{ id: "deepseek/deepseek-chat", name: "deepseek-chat", group: "DeepSeek", context: 1e5, hidden: true }], ordered: false });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const cases = {
  no_models: {
    answer: { error: "Add a provider or subscription in magpie first, then connect Codex", code: "no_models", agent: "Codex" },
    said: {
      en: "Add a provider or subscription in magpie first, then connect Codex",
      zh: "请先在 magpie 中添加一个供应商或订阅，再接入 Codex",
      ja: "先に magpie でプロバイダかサブスクリプションを追加してから、Codex を接続してください",
      de: "Fügen Sie zuerst in magpie einen Anbieter oder ein Abo hinzu und verbinden Sie dann Codex",
    },
  },
  all_hidden: {
    answer: { error: "None of magpie's models is left in Codex's model list: pick one under Model list → Pick on the Agents page, then connect Codex", code: "all_hidden", agent: "Codex" },
    said: {
      en: "None of magpie's models is left in Codex's model list: pick one, then connect Codex",
      zh: "magpie 的模型都不在 Codex 的模型列表里：先选一个，再接入 Codex",
      ja: "Codex のモデル一覧に magpie のモデルが一つも残っていません：一つ選んでから、Codex を接続してください",
      de: "In der Modellliste von Codex ist keines der Modelle von magpie übrig: Wählen Sie eines aus und verbinden Sie dann Codex",
    },
  },
};
const sw = '.row.agent[data-id="codex"] .ag-conn';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [code, { answer, said }] of Object.entries(cases)) for (const lang of Object.keys(said)) {
    test(`${engine} ${lang} ${code}: connecting with no models to give says what to do`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 980, height: 520 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [], dialogs = [];
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("dialog", (d) => { dialogs.push(d.message()); d.dismiss(); });
      await page.route("**/*", server(lang, posts, answer));
      await page.goto("http://magpie.test/?view=agents");
      await page.locator(sw).waitFor();
      const tops = () => page.evaluate(() => [document.scrollingElement.scrollTop, document.querySelector("#view-agents")?.scrollTop]);
      const top = await tops();

      await page.locator(sw).click();
      await page.waitForFunction((s) => document.querySelector("#status")?.textContent.includes(s), said[lang]);
      assert.deepEqual(posts, ["/api/agents/connect/codex"]);
      assert.equal(await page.locator(sw).getAttribute("aria-checked"), "false", "the switch goes back off");
      assert.ok(!(await page.locator(sw).isDisabled()), "and can be tried again");
      assert.deepEqual(await tops(), top, "no click moved the page");
      assert.deepEqual(dialogs, [], "no native dialog");
      assert.deepEqual(errors, []);
    });
  }
}
