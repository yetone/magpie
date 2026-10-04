// Run with Node's test runner and Playwright on the module path; see README.md.
// The owner: the 「接入」 switch, and the model picked in magpie in one click,
// both. Beside an agent's switch is the model it starts on. Not connected,
// it reads Pick a model and lists magpie's models alone; the one picked is
// posted to set, which connects the agent first, and the row is connected,
// on that model. Connected, its picker has every choice: the agent's own
// models and its default too. No click moves the page. In English and
// Chinese; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const options = [{ value: "gpt-5.4", label: "GPT-5.4" }, { value: "relay/m1", label: "m1", ref: "relay/m1", note: "Relay · via magpie" }, { value: "relay/m2", label: "m2", ref: "relay/m2", note: "Relay · via magpie" }];
const fresh = () => ({
  agents: [{ id: "codex", name: "Codex", icon: "generic", path: "/fixture/codex", fields: [{ key: "model", label: "model", value: "gpt-5.4", options }] }],
  profiles: [],
});

function server(lang, posts) {
  let cur = fresh();
  const reply = (route) => route.fulfill({ json: { ...cur, settings: { lang, theme: "light" } } });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return reply(route);
    if (url.pathname === "/api/set") {
      const body = JSON.parse(req.postData());
      posts.push(body);
      cur = JSON.parse(JSON.stringify(cur));
      const a = cur.agents.find((x) => x.id === body.agent);
      a.fields[0].value = body.value;
      if (body.value.startsWith("relay/")) a.wired = true;
      return reply(route);
    }
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

const words = {
  en: { pick: "Pick a model", on: "Connected · pick magpie's models with /model in Codex" },
  zh: { pick: "选模型", on: "已接入 · 在 Codex 里用 /model 选 magpie 的模型" },
};
const row = '.row.agent[data-id="codex"]';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a model picked beside the switch connects the agent on it`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 980, height: 520 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [], posts = [], dialogs = [];
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("dialog", (d) => { dialogs.push(d.message()); d.dismiss(); });
      await page.route("**/*", server(lang, posts));
      await page.goto("http://magpie.test/?view=agents");
      await page.locator(`${row} .ag-conn`).waitFor();
      const missing = await page.evaluate(() => [
        "Pick a model",
        "Pick one of magpie's models: {agent} is connected and starts on it",
        "Its last pick",
        "Unset, {agent} starts on its own last pick",
        "Connected · starts on the model picked here",
        "Switch an agent on and the models you set up in magpie show up as the {magpie} provider in its own model list; or pick its model right here.",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      const tops = () => page.evaluate(() => [document.scrollingElement.scrollTop, document.querySelector("#view-agents")?.scrollTop]);
      const top = await tops();
      const start = page.locator(`${row} > .field.ag-start`);
      const listed = async () => {
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        return page.locator("#pop #list li").allTextContents();
      };

      // not connected: magpie's models alone
      assert.equal((await start.textContent()).trim(), w.pick);
      assert.equal(await page.locator(`${row} .ag-conn`).getAttribute("aria-checked"), "false");
      await start.click();
      const off = await listed();
      assert.ok(off.some((s) => s.includes("m1")) && off.some((s) => s.includes("m2")), off.join(" | "));
      assert.ok(!off.some((s) => s.includes("GPT-5.4")), "the agent's own models aren't offered: " + off.join(" | "));
      await page.locator("#pop #list li", { hasText: "m2" }).first().click();
      await page.waitForFunction((r) => document.querySelector(r + " .ag-conn")?.getAttribute("aria-checked") === "true", row);
      assert.deepEqual(posts, [{ agent: "codex", field: "model", value: "relay/m2" }]);
      assert.equal(await page.locator(`${row} .ag-st`).textContent(), w.on);
      assert.match(await start.textContent(), /m2/);

      // connected: every choice
      await start.click();
      const on = await listed();
      assert.ok(on.some((s) => s.includes("GPT-5.4")) && on.some((s) => s.includes("m1")), on.join(" | "));
      await page.keyboard.press("Escape");
      assert.deepEqual(await tops(), top, "no click moved the page");
      assert.deepEqual(dialogs, [], "no native dialog");
      assert.deepEqual(errors, []);
    });
  }
}
