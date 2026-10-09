// Run with Node's test runner and Playwright on the module path; see README.md.
// A move held back by a model the plugin doesn't offer says for which
// account (and its plan) the built-in serves it, so a reader can tell a
// model really lost from one their plan never had; a why with no account
// (an older magpie's) still reads as before. In English and Chinese; the
// API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const pkg = "@magpie-community/opencode-zed-auth";

function serve(lang, posts, why) {
  let move = { package: pkg, state: "" };
  const payload = () => {
    const onPlugin = move.state === "plugin";
    const zed = {
      id: "zed", name: "Zed", icon: "zed", chat: onPlugin ? "plugin://zed/v1" : "", responses: "", anthropic: onPlugin ? "plugin://zed" : "", catalog: "",
      // enough models that the editor scrolls to reach Runs on
      models: Array.from({ length: 30 }, (_, i) => ({ id: "m" + i, name: "Model " + i, on: i === 0 })),
      agents: [], fallback: [], headers: {}, keyList: [], key: {},
      account: { agent: "zed", agentName: "Zed", user: "ada", logins: [{ user: "ada", active: true, on: true, own: true }, { user: "bob", on: true }] },
      move,
    };
    return {
      providers: [zed], presets: [], excluded: [], gateway: { running: true, window: true },
      plugins: onPlugin ? [{ id: "zed", pid: "zed", name: "Zed", icon: "zed", spec: pkg, signedIn: true, models: 1, methods: [] }] : [],
      onPlugins: onPlugin ? ["zed"] : [],
    };
  };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(payload());
    if (url.pathname === "/api/groups") return json({ groups: [] });
    // the page asks for the market in parts (#488)
    if (url.pathname === "/api/plugins/market" || url.pathname === "/api/plugins" || url.pathname === "/api/plugins/listings") {
      const m = ({ listings: [{ package: pkg, name: "Zed", icon: "zed", providers: ["zed"], community: true, replaces: "zed", summary: { en: "Zed", zh: "Zed" }, npm: { version: "0.1.0" } }],
        state: { bun: true, bunVersion: "1.3.0", plugins: [], movable: move.state === "plugin" ? [] : [{ id: "zed", name: "Zed", package: pkg, accounts: 2 }] } });
      return json(url.pathname === "/api/plugins" ? m.state : url.pathname === "/api/plugins/listings" ? { listings: m.listings } : m);
    }
    if (url.pathname === "/api/provider/move" || url.pathname === "/api/provider/moveback") {
      posts.push({ path: url.pathname, body: route.request().postDataJSON() });
      await new Promise((r) => setTimeout(r, 400)); // long enough to read the button
      // the plugin doesn't offer a model the built-in serves on ada
      if (url.pathname.endsWith("/move")) {
        move = { package: pkg, state: "failed", error: "unserved", why };
        return route.fulfill({ status: 400, json: { error: move.error, why } });
      }
      move = { package: pkg, state: url.pathname.endsWith("moveback") ? "back" : "plugin" };
      return json(payload());
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { look: "Review the move",
    withUser: "It stays built-in: the plugin doesn't serve Model 0 for ada (Zed Pro), though the built-in does. Untick them under Models, or keep the built-in.",
    bare: "It stays built-in: the plugin doesn't serve Model 0. Untick them under Models, or keep the built-in." },
  zh: { look: "查看迁移",
    withUser: "仍使用内置：插件没有为 ada (Zed Pro) 提供 Model 0（内置提供）。可在「模型」里取消勾选，或继续使用内置。",
    bare: "仍使用内置：插件不提供 Model 0。可在「模型」里取消勾选，或继续使用内置。" },
};

const launch = (engine) => engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" });

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const [kind, why] of [["withUser", { code: "unserved", args: { models: "Model 0", user: "ada (Zed Pro)" } }], ["bare", { code: "unserved", args: { models: "Model 0" } }]]) {
      test(`${engine} ${lang}: a move short of a model says it (${kind})`, async (t) => {
        const w = L[lang];
        const browser = await launch(engine);
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts, why));
        await page.goto("http://magpie.test/?view=providers");
        const line = page.locator("#movable");
        await line.locator("button", { hasText: w.look }).click();
        const ed = page.locator("#modal .editor");
        await ed.locator(".move button").click();
        const said = ed.locator(".move-why");
        await said.waitFor({ state: "visible" });
        assert.equal(await said.innerText(), w[kind]);
        assert.deepEqual(errors, []);
      });
    }
  }
}

// Try again moves with the models as the editor shows them picked, not
// yet saved: the reason said to untick one under Models (noting_ever on
// X: deep-model unticked, and the move failed on it all the same). The
// first try, with the picks unchanged, sends none.
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: Try again takes the models unticked`, async (t) => {
      const browser = await launch(engine);
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts, { code: "unserved", args: { models: "Model 0" } }));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator("#movable button", { hasText: L[lang].look }).click();
      const ed = page.locator("#modal .editor");
      await ed.locator(".move button").click();
      await ed.locator(".move-why").waitFor({ state: "visible" });
      // Model 1 ticked, Model 0 unticked, nothing saved
      await ed.locator(".mchip", { hasText: "Model 1" }).first().click();
      await ed.locator(".mchip.on", { hasText: "Model 0" }).click();
      await ed.locator(".move button").click();
      await page.waitForFunction(() => document.querySelector("#modal .editor .move-why")?.hidden === false);
      assert.deepEqual(posts.map((p) => p.body), [{ id: "zed" }, { id: "zed", models: ["m1"] }]);
      assert.deepEqual(errors, []);
    });
  }
}
