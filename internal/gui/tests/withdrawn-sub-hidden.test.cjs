// Run with Node's test runner and Playwright on the module path; see README.md.
// A built-in whose plugin magpie gives nobody new (noNew: Antigravity,
// whose plugin is withdrawn) isn't in the Add sheet for a user with no
// account on it, moved onto that plugin or not (the owner, 11-25: 这个自带的
// antigravity应该删掉啊。如果用户没启用的话): Antigravity (local agy CLI),
// under From plugins, is the way in. A user who still has its accounts
// keeps its tile. English and Chinese, Chromium and WebKit; the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const agyRow = { id: "agy-cli", name: "Antigravity (local agy CLI)", icon: "antigravity-color", chat: "plugin://agy-cli/v1", models: [{ id: "m", name: "M", on: true }], agents: [], fallback: [], headers: {}, keyList: [], key: {},
  account: { agent: "agy-cli", agentName: "Antigravity (local agy CLI)", user: "ada@g", logins: [{ user: "ada@g", active: true, on: true }] } };
const agRow = { id: "antigravity", name: "Antigravity", icon: "antigravity-color", chat: "plugin://antigravity/v1", models: [{ id: "m", name: "M", on: true }], agents: [], fallback: [], headers: {}, keyList: [], key: {},
  account: { agent: "antigravity", agentName: "Antigravity", user: "bo@g", logins: [{ user: "bo@g", active: true, on: true }] }, move: { state: "plugin" } };
const plugins = [
  { id: "agy-cli", pid: "agy-cli", name: "Antigravity", icon: "antigravity-color", spec: "@magpie-community/opencode-agy-cli-auth", signedIn: true, models: 4, methods: [{ type: "oauth", label: "agy" }] },
  { id: "antigravity", pid: "antigravity", name: "Antigravity", icon: "antigravity-color", spec: "@magpie-community/opencode-antigravity-auth", signedIn: false, models: 4, methods: [{ type: "oauth", label: "Google" }] },
];

function serve(lang, rows) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    // as the owner's: Antigravity moved onto its withdrawn plugin, Grok on
    // its own, Gemini CLI built in
    if (url.pathname === "/api/providers") return json({ providers: rows, presets: [], excluded: [], gateway: { running: true, window: true }, plugins,
      onPlugins: ["antigravity", "grok"], movable: ["antigravity", "grok"], noNew: ["antigravity"],
      movesTo: { antigravity: "@magpie-community/opencode-antigravity-auth", grok: "@magpie-community/opencode-grok-auth" } });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = { en: { subs: "Subscriptions", plugins: "From plugins" }, zh: { subs: "订阅", plugins: "来自插件" } };
// the tiles named name under the section headed head
const under = (page, head, name) => page.evaluate(([head, name]) => {
  let n = 0, inIt = false;
  for (const c of document.querySelectorAll("#addSheet .tiles > *")) {
    if (c.classList.contains("kind") || c.classList.contains("kind-row")) inIt = c.textContent.startsWith(head);
    else if (inIt) n += c.querySelectorAll(`.tile[data-pick="${name}"]`).length;
  }
  return n;
}, [head, name]);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: the built-in Antigravity is offered only to its users`, async (t) => {
      const w = L[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const [rows, kept] of [[[agyRow], false], [[agyRow, agRow], true]]) {
        const page = await (await browser.newContext({ viewport: { width: 900, height: 1000 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, rows));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator('.row.provider[data-id="agy-cli"]').waitFor({ timeout: 60000 });
        await page.locator(".after-list button").first().click();
        await page.locator('#addSheet .tile[data-pick="Grok"]').waitFor();
        assert.equal(await under(page, w.subs, "Gemini CLI"), 1, "Gemini CLI isn't under Subscriptions");
        assert.equal(await under(page, w.subs, "Grok"), 1, "Grok, moved, isn't under Subscriptions");
        assert.equal(await under(page, w.subs, "Antigravity"), kept ? 1 : 0, kept ? "a user with Antigravity accounts lost its tile" : "the built-in Antigravity is offered with no account on it");
        assert.equal(await under(page, w.plugins, "Antigravity"), 1, "the agy plugin's Antigravity isn't under From plugins");
        // a search for it finds the plugin's, never the built-in's
        if (!kept) {
          await page.locator("#addSheet input.find").fill("antigravity");
          await page.locator('#addSheet .tile[data-pick="Antigravity"]').first().waitFor();
          assert.equal(await under(page, w.subs, "Antigravity"), 0, "the search offers the built-in Antigravity");
          assert.equal(await under(page, w.plugins, "Antigravity"), 1, "the search lost the agy plugin's Antigravity");
        }
        assert.deepEqual(errors, []);
        await page.context().close();
      }
    });
  }
}
