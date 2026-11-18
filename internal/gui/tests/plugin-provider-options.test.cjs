// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider plugin's own options on the Installed tab (plugins#592): one
// whose package suggests options (Devin's cacheKeepaliveInterval and
// cacheKeepaliveMaxPings) or that has some set gets the same Options
// editor a middleware has, starting from its package's example; one with
// neither, or switched off, gets none. Clearing them asks first, in words
// that don't call them a middleware's. Its declared switches are its
// provider's editor's: the JSON editor names that editor and opens it, so
// a plugin's options have one place each. Every language; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const DEVIN = "npm:@magpie-community/opencode-devin-auth", PLAIN = "npm:@magpie-community/opencode-plain-auth", OFF = "npm:@magpie-community/opencode-off-auth";
const example = { cacheKeepaliveInterval: 285, cacheKeepaliveMaxPings: 6 };
const state = { bun: true, bunVersion: "1.3.0", plugins: [
  { spec: DEVIN, ownName: "Devin Auth", providers: ["Devin"], moved: ["devin"], named: true, optionsExample: example },
  { spec: PLAIN, ownName: "Plain Auth", providers: ["Plain"], moved: [], named: true },
  { spec: OFF, ownName: "Offco Auth", off: true, providers: ["Offco"], moved: [], named: true, optionsExample: example },
] };

function server(lang, pluginState) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [
      { id: "devin", pid: "devin", name: "Devin", spec: DEVIN, signedIn: true, methods: [], models: 0, options: [{ key: "fast", label: "Fast", on: false }] },
      { id: "plain", pid: "plain", name: "Plain", spec: PLAIN, signedIn: true, methods: [], models: 0 },
    ] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugins/market" || url.pathname === "/api/plugins" || url.pathname === "/api/plugins/listings") return json(url.pathname === "/api/plugins" ? pluginState : url.pathname === "/api/plugins/listings" ? { listings: [] } : { listings: [], state: pluginState });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const W = {
  en: { switches: "Its switches are in Devin's editor", installed: "Installed", options: "Options", save: "Save", clear: "Clear", cancel: "Cancel", gone: /^Saved options will be removed\. The plugin will run with no options\.$/ },
  zh: { switches: "它的开关在 Devin 的编辑器里", installed: "已安装", options: "选项", save: "保存", clear: "清空", cancel: "取消", gone: /^已保存的选项将被移除。插件将不带选项运行。$/ },
  "zh-TW": { switches: "它的開關在 Devin 的編輯器裡", installed: "已安裝", options: "選項", save: "儲存", clear: "清空", cancel: "取消", gone: /^已儲存的選項將被移除。外掛將不帶選項執行。$/ },
  ja: { switches: "スイッチは Devin のエディタにあります", installed: "インストール済み", options: "オプション", save: "保存", clear: "クリア", cancel: "キャンセル", gone: /^保存したオプションが削除されます。プラグインはオプションなしで実行されます。$/ },
  de: { switches: "Die Schalter sind im Editor von Devin", installed: "Installiert", options: "Optionen", save: "Speichern", clear: "Leeren", cancel: "Abbrechen", gone: /^Die gespeicherten Optionen werden entfernt\. Das Plugin läuft ohne Optionen\.$/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a provider plugin's options in its row", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const [lang, w] of Object.entries(W)) {
      await t.test(lang, async () => {
        for (const width of [980, 420]) {
          const page = await (await browser.newContext({ viewport: { width, height: 820 } })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [], posted = [];
          page.on("pageerror", (e) => errors.push(e.message));
          const pluginState = structuredClone(state);
          await page.route("**/*", server(lang, pluginState));
          await page.route("**/api/plugins/options", (r) => {
            const body = r.request().postDataJSON();
            posted.push(body);
            pluginState.plugins[0].options = body.options ?? undefined;
            return r.fulfill({ json: pluginState });
          });
          await page.goto("http://magpie.test/?view=plugins");
          const view = page.locator("#view-plugins");
          await page.locator(":is(.lib-tabs .opt, .nav-s):visible", { hasText: w.installed }).click();
          const names = { [DEVIN]: "Devin Auth", [PLAIN]: "Plain Auth", [OFF]: "Offco Auth" };
          const row = (spec) => view.locator(".pm-row").filter({ has: page.locator(".name", { hasText: names[spec] }) });
          await row(DEVIN).waitFor();
          await row(PLAIN).waitFor();
          assert.equal(await row(PLAIN).getByRole("button", { name: w.options, exact: true }).count(), 0, "no Options on a plugin that has none");
          assert.equal(await row(OFF).getByRole("button", { name: w.options, exact: true }).count(), 0, "no Options on one switched off");
          const devin = row(DEVIN);
          await devin.getByRole("button", { name: w.options, exact: true }).click();
          const ta = devin.locator(".pm-opts textarea");
          await ta.waitFor();
          assert.deepEqual(JSON.parse(await ta.inputValue()), example);
          const box = await ta.boundingBox();
          assert.ok(box && box.x >= 0 && box.x + box.width <= width, `the editor fits at ${width}px: ${JSON.stringify(box)}`);
          assert.equal(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth), true, "no sideways scroll");
          await ta.fill('{"cacheKeepaliveInterval": 240, "cacheKeepaliveMaxPings": 10}');
          await devin.locator(".pm-opts").getByRole("button", { name: w.save, exact: true }).click();
          await devin.locator(".pm-opts").waitFor({ state: "detached" });
          assert.deepEqual(posted, [{ spec: DEVIN, options: { cacheKeepaliveInterval: 240, cacheKeepaliveMaxPings: 10 } }]);
          await devin.getByRole("button", { name: w.options, exact: true }).click();
          assert.deepEqual(JSON.parse(await ta.inputValue()), { cacheKeepaliveInterval: 240, cacheKeepaliveMaxPings: 10 });
          await devin.getByRole("button", { name: w.clear, exact: true }).click();
          const confirm = page.getByRole("alertdialog");
          await confirm.waitFor();
          assert.match((await confirm.locator("#actionConfirmMessage").textContent()).trim(), w.gone);
          await confirm.getByRole("button", { name: w.cancel, exact: true }).click();
          assert.equal(posted.length, 1, "Cancel keeps the saved options");
          await devin.getByRole("button", { name: w.clear, exact: true }).click();
          await confirm.locator("button").last().click();
          await devin.locator(".pm-opts").waitFor({ state: "detached" });
          assert.deepEqual(posted[1], { spec: DEVIN, options: null });
          assert.equal(await view.locator("select").count(), 0);
          // the switches Devin declares are in its editor: the link says
          // so and opens it, and the JSON editor goes with the page
          await devin.getByRole("button", { name: w.options, exact: true }).click();
          const go = devin.locator(".pm-opts").getByRole("button", { name: w.switches, exact: true });
          await go.click();
          await page.locator("#view-providers").waitFor();
          await page.waitForFunction(() => /view=providers/.test(location.search) || document.querySelector("#view-providers")?.offsetParent);
          assert.equal(await view.locator(".pm-opts").count(), 0);
          assert.deepEqual(errors, []);
          await page.context().close();
        }
      });
    }
  });
}
