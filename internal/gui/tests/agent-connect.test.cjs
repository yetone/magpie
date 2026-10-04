// Run with Node's test runner and Playwright on the module path; see README.md.
// The owner: an agent is connected to magpie with a switch, and its models
// are picked in the agent itself. An agent magpie has models for gets a
// switch, off while magpie isn't in its config, saying so under its name;
// switching it on posts agents/connect and the row says where in the agent
// magpie's models are picked (Codex's /model). Switching it off asks first
// in the app's own dialog and posts agents/disconnect. An agent with none of
// magpie's models (Cursor) has no switch. No click moves the page. In
// English and Chinese; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const options = [{ value: "gpt-5.4", label: "GPT-5.4" }, { value: "relay/m1", label: "m1", ref: "relay/m1", note: "Relay · via magpie" }];
const fresh = () => ({
  agents: [
    { id: "codex", name: "Codex", icon: "generic", path: "/fixture/codex", fields: [{ key: "model", label: "model", value: "gpt-5.4", options }] },
    { id: "cursor", name: "Cursor", icon: "generic", path: "/fixture/cursor", fields: [{ key: "model", label: "model", value: "auto", options: [{ value: "auto", label: "Auto" }] }] },
  ],
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
    const act = url.pathname.match(/^\/api\/agents\/(connect|disconnect)\/(\w+)$/);
    if (act) {
      posts.push(url.pathname);
      cur = JSON.parse(JSON.stringify(cur));
      const a = cur.agents.find((x) => x.id === act[2]);
      if (act[1] === "connect") { a.wired = true; a.fields[0].value = "relay/m1"; }
      else { delete a.wired; a.fields[0].value = "gpt-5.4"; }
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
  en: { off: "Not connected · Codex uses its own settings", on: "Connected · pick magpie's models with /model in Codex", go: "Disconnect", done: /Codex is connected to magpie/ },
  zh: { off: "未接入 · 用 Codex 自己的设置", on: "已接入 · 在 Codex 里用 /model 选 magpie 的模型", go: "断开", done: /Codex 已接入 magpie/ },
};
const row = (id) => `.row.agent[data-id="${id}"]`;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: an agent's switch connects it to magpie and back`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 980, height: 520 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [], posts = [], dialogs = [];
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("dialog", (d) => { dialogs.push(d.message()); d.dismiss(); });
      await page.route("**/*", server(lang, posts));
      await page.goto("http://magpie.test/?view=agents");
      await page.locator(`${row("codex")} .ag-conn`).waitFor();
      const missing = await page.evaluate(() => [
        "Not connected · {agent} uses its own settings",
        "Connected · pick magpie's models with {cmd} in {agent}",
        "Connected · magpie's models are in {agent}'s model list",
        "Connect {agent} to magpie",
        "Connected · switch off to put back what {agent} had before magpie",
        "Switch on and {agent}'s model list gets magpie's models",
        "{agent} is connected to magpie",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      const tops = () => page.evaluate(() => [document.scrollingElement.scrollTop, document.querySelector("#view-agents")?.scrollTop]);
      const top = await tops();
      const sw = () => page.locator(`${row("codex")} .ag-conn`);
      const said = () => page.locator(`${row("codex")} .ag-st`).textContent();

      assert.equal(await page.locator(`${row("cursor")} .ag-conn`).count(), 0, "nothing of magpie's to connect it to");
      assert.equal(await sw().getAttribute("role"), "switch");
      assert.equal(await sw().getAttribute("aria-checked"), "false");
      assert.equal(await said(), w.off);

      await sw().click();
      await page.waitForFunction((r) => document.querySelector(r + " .ag-conn")?.getAttribute("aria-checked") === "true", row("codex"));
      assert.deepEqual(posts, ["/api/agents/connect/codex"]);
      assert.equal(await said(), w.on);
      assert.match(await page.locator("#status").textContent(), w.done);

      // off asks first, in the app's own dialog
      await sw().click();
      const ask = page.locator(".editor.disconnect-ask");
      await ask.waitFor();
      assert.deepEqual(posts, ["/api/agents/connect/codex"], "switching off asks before it posts");
      await ask.locator(".bar button", { hasText: w.go }).click();
      await ask.waitFor({ state: "detached" });
      await page.waitForFunction((r) => document.querySelector(r + " .ag-conn")?.getAttribute("aria-checked") === "false", row("codex"));
      assert.deepEqual(posts, ["/api/agents/connect/codex", "/api/agents/disconnect/codex"]);
      assert.equal(await said(), w.off);
      assert.deepEqual(await tops(), top, "no click moved the page");
      assert.deepEqual(dialogs, [], "no native dialog");
      assert.deepEqual(errors, []);
    });
  }
}
