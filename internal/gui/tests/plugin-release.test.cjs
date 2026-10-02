// Run with Node's test runner and Playwright on the module path; see README.md.
// A plugin a built-in subscription is moved onto says so in Installed, and
// removing it or switching it off asks first, in the row, naming the
// subscription that goes back to the built-in with its accounts; only a
// yes sends the request. English and Chinese; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const ZED = "@magpie-community/opencode-zed-auth";

function server(lang, asked) {
  const installed = [
    { spec: ZED + "@0.1.2", providers: ["Zed"], version: "0.1.2", moved: ["zed"] },
    { spec: "opencode-gemini-auth", providers: ["Gemini"], version: "1.4.0", moved: [] },
  ];
  const onPlugins = ["zed"];
  const market = () => ({ listings: [{ package: ZED, name: "Zed", icon: "zed", providers: ["zed"], community: true, replaces: "zed", npm: { version: "0.1.2" } }],
    state: { bun: true, bunVersion: "1.3.0", plugins: installed } });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    const body = () => route.request().postDataJSON();
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") {
      return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, onPlugins,
        plugins: installed.some((e) => e.moved.length) ? [{ id: "zed", pid: "zed", name: "Zed", icon: "zed", spec: ZED + "@0.1.2", signedIn: true, models: 9, methods: [{ type: "oauth", label: "Sign in with Zed" }] }] : [] });
    }
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    // the page asks for the market in parts (#488)
    if (url.pathname === "/api/plugins/market" || url.pathname === "/api/plugins" || url.pathname === "/api/plugins/listings") { const m = market(); return json(url.pathname === "/api/plugins" ? m.state : url.pathname === "/api/plugins/listings" ? { listings: m.listings } : m); }
    if (url.pathname === "/api/plugins/remove" || url.pathname === "/api/plugins/off") {
      const b = body();
      asked.push([url.pathname.split("/").pop(), b]);
      await new Promise((r) => setTimeout(r, 150));
      const i = installed.findIndex((e) => e.spec === b.spec);
      if (url.pathname.endsWith("remove")) installed.splice(i, 1);
      else { installed[i].off = b.off; installed[i].moved = []; }
      onPlugins.length = 0;
      return json({});
    }
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") { asked.push(["fetched", url.href]); return route.fulfill({ status: 404, body: "" }); }
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { installed: "Installed", serves: "Serves Zed", off: "Switch off", remove: "Remove", cancel: "Cancel",
    askOff: "Zed goes back to the built-in, with its accounts, before the plugin is switched off.",
    askRemove: "Zed goes back to the built-in, with its accounts, before the plugin is removed.", back: "Zed is back on the built-in" },
  zh: { installed: "已安装", serves: "承载 Zed", off: "停用", remove: "移除", cancel: "取消",
    askOff: "停用插件前，Zed 会带着它的账号回到内置的。", askRemove: "移除插件前，Zed 会带着它的账号回到内置的。", back: "Zed 已回到内置的" },
};

const shot = async (loc, name) => {
  if (!process.env.ARTIFACT_DIR) return;
  await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
  await loc.screenshot({ path: path.join(process.env.ARTIFACT_DIR, name + ".png") });
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a plugin a subscription is moved onto", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = L[lang];
        const page = await (await browser.newContext({ viewport: { width: 980, height: 820 } })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [], asked = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, asked));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator('#nav button[data-view="plugins"]').click();
        const view = page.locator("#view-plugins");
        await view.waitFor({ state: "visible" });
        await view.locator(".lib-tabs .opt", { hasText: w.installed }).click();
        const zed = view.locator(".pm-row").filter({ hasText: "Zed" });
        const chip = zed.locator(".pm-chip.moved");
        await chip.waitFor();
        assert.equal(await chip.innerText(), w.serves);
        assert.equal(await chip.locator(".dot").count(), 1);
        assert.equal(await view.locator(".pm-row").filter({ hasText: "opencode-gemini-auth" }).locator(".pm-chip.moved").count(), 0);
        const top = await view.evaluate((e) => e.scrollTop);

        // switching off asks first; No leaves it on and sends nothing
        await zed.locator("button", { hasText: new RegExp("^" + w.off + "$") }).click();
        await zed.locator(".sub.ask", { hasText: w.askOff }).waitFor();
        await shot(zed, `plugin-release-ask-${engine}-${lang}`);
        await zed.locator("button", { hasText: w.cancel }).click();
        await zed.locator(".sub.ask").waitFor({ state: "detached" });
        assert.deepEqual(asked, [], "nothing sent before a yes");

        // removing asks too; yes moves Zed back and removes the plugin
        await zed.locator("button", { hasText: new RegExp("^" + w.remove + "$") }).click();
        await zed.locator(".sub.ask", { hasText: w.askRemove }).waitFor();
        await zed.locator("button.primary", { hasText: w.remove }).click();
        await page.waitForFunction(() => document.querySelectorAll("#view-plugins .pm-row").length === 1);
        assert.deepEqual(asked, [["remove", { spec: ZED + "@0.1.2" }]]);
        await page.locator("#status", { hasText: w.back }).waitFor();
        assert.equal(await view.evaluate((e) => e.scrollTop), top, "a click doesn't move the page");
        assert.deepEqual(asked.filter(([k]) => k === "fetched"), []);
        assert.deepEqual(errors, []);
      });
    }
  });
}
