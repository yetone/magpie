// Run with Node's test runner and Playwright on the module path; see README.md.
// A plugin subscription takes accounts from a file when its plugin has
// auth.import (marchen_cc on X: WorkBuddy's had no "Import accounts from
// a file…"). A moved built-in (WorkBuddy) and a plugin's own provider
// both list it, with the plugin's own words for which file, in the
// window's language; a plugin with no import lists none. English and
// Chinese; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const from = { en: "Choose WorkBuddy's sign-in file", zh: "选择 WorkBuddy 的登录文件" };
const plugin = (id, name, importable) => ({
  id, pid: id, name, icon: "generic", spec: "@magpie-community/opencode-" + id + "-auth", signedIn: true, models: 1,
  methods: [{ type: "oauth", label: name + " (browser)" }],
  ...(importable ? { importable: true, importFrom: from.en, importFroms: { zh: from.zh } } : {}),
});
const plugins = [plugin("workbuddy", "WorkBuddy", true), plugin("acme", "Acme", true), plugin("plain", "Plain", false)];
const row = (id, name) => ({ id, name, icon: "generic", chat: "plugin://" + id + "/v1", models: [], agents: [], key: {},
  account: { agent: id, agentName: name, agentIcon: "generic", user: "a@" + id, logins: [{ user: "a@" + id, active: true, on: true }] } });

function server(lang, posted) {
  const payload = () => ({
    providers: [row("workbuddy", "WorkBuddy"), row("acme", "Acme"), row("plain", "Plain")],
    presets: [], excluded: [], gateway: { running: true, window: true }, plugins, onPlugins: ["workbuddy"],
  });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(payload());
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/signin/import") {
      posted.push(route.request().postDataJSON());
      return json({ providers: payload(), results: [{ user: "b@workbuddy", status: "added" }] });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a plugin subscription imports from a file", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const page = await (await browser.newContext({ viewport: { width: 900, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posted = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posted));
        await page.goto("http://magpie.test/?view=providers");
        const T = (s, v) => page.evaluate(([s, v]) => t(s, v), [s, v || {}]);
        const imp = await T("Import accounts from a file…");
        const ed = page.locator("#modal .editor");
        const open = async (name) => {
          await page.locator(".row.provider", { hasText: name }).first().click();
          await ed.locator(".accts").waitFor();
        };
        const importRow = () => ed.locator(".accts .acc.add", { hasText: imp });

        for (const name of ["WorkBuddy", "Acme"]) {
          await open(name);
          assert.equal(await importRow().count(), 1, name + " lists no import");
          assert.equal(await importRow().getAttribute("title"), from[lang]);
          await page.keyboard.press("Escape");
          await ed.waitFor({ state: "detached" });
        }
        await open("Plain");
        assert.equal(await importRow().count(), 0, "a plugin with no import lists one");
        await page.keyboard.press("Escape");
        await ed.waitFor({ state: "detached" });

        // WorkBuddy's: the plugin's words, what is pasted posted as it is
        await open("WorkBuddy");
        await importRow().click();
        const box = page.locator(".signing.import");
        await box.waitFor();
        assert.equal(await box.locator(".tt > .s").first().textContent(), from[lang]);
        await box.locator("textarea").fill('{"auth":{}}');
        await box.locator("button.primary").click();
        await box.locator(".results").waitFor();
        assert.deepEqual(posted, [{ agent: "workbuddy", files: ['{"auth":{}}'] }]);
        assert.deepEqual(errors, []);
      });
    }
  });
}
