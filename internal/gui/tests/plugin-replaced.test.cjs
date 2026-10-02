// Run with Node's test runner and Playwright on the module path; see README.md.
// ARNO's (on Discord): a deprecated built-in subscription whose plugin is
// installed is the plugin's row alone in the add sheet (Qoder CN was listed
// twice), its signed-in accounts still listed as providers. English and
// Chinese; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function server(lang, st) {
  // the Qoder plugin serves both; ARNO's was signed in to two Qoder
  // accounts of its own, so under its own id, and Qoder CN not at all
  const pkg = "@magpie-community/opencode-qoder-auth";
  const plugins = () => !st.installed ? [] : [
    { id: st.moved ? "qoder" : "qoder-plugin", pid: "qoder", name: "Qoder", icon: "qoder", spec: pkg, signedIn: true, accounts: [{ user: "a@q" }, { user: "b@q" }], models: 3, methods: [{ type: "oauth", label: "Qoder" }] },
    { id: "qoder-cn-plugin", pid: "qoder-cn", name: "Qoder CN", icon: "qoder", spec: pkg, signedIn: false, models: 3, methods: [{ type: "oauth", label: "Qoder CN" }] },
  ];
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({
      providers: st.signedIn ? [{ id: "qoder-cn", name: "Qoder CN", icon: "qoder", chat: "https://x.test", models: [], agents: [], key: {},
        account: { agent: "qoder-cn", agentName: "Qoder CN", agentIcon: "qoder", user: "a@q", logins: [{ user: "a@q", active: true, on: true }] } }] : [],
      presets: [], excluded: [], gateway: { running: true, window: true }, plugins: plugins(),
      movable: ["qoder", "qoder-cn"], movesTo: { qoder: pkg, "qoder-cn": pkg }, onPlugins: st.moved ? ["qoder"] : [],
    });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { subs: "Subscriptions", plugins: "From plugins", intl: "Qoder" },
  zh: { subs: "订阅", plugins: "来自插件", intl: "Qoder 国际版" },
};

async function open(browser, lang, st, view) {
  const page = await (await browser.newContext({ viewport: { width: 900, height: 600 } })).newPage();
  page.setDefaultTimeout(5000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", server(lang, st));
  await page.goto("http://magpie.test/?view=" + view);
  return { page, errors };
}

// each Qoder tile in the add sheet, as [section, name]
async function qoderTiles(page) {
  const sheet = page.locator("#addSheet");
  // with no provider yet the sheet is open already
  await page.locator(".row.provider, #addSheet .tile").first().waitFor();
  if (!(await sheet.locator(".tile").count())) await page.locator("#addProvider").click();
  await sheet.locator(".tile").first().waitFor();
  return sheet.evaluate((s) => {
    const out = [];
    let at = "";
    for (const x of s.querySelectorAll(".kind b, .tile")) {
      if (x.matches(".kind b")) at = x.textContent;
      else if ((x.dataset.pick || "").startsWith("Qoder")) out.push([at, x.dataset.pick]);
    }
    return out;
  });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a built-in its installed plugin serves is listed once", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang + ": the add sheet", async () => {
        const w = L[lang];
        // no plugin: the built-ins, deprecated, under Subscriptions, where
        // a click installs the plugin and signs in through it
        let { page, errors } = await open(browser, lang, { installed: false }, "providers");
        assert.deepEqual(await qoderTiles(page), [[w.subs, w.intl], [w.subs, "Qoder CN"]]);
        // the plugin installed: its tiles alone, not the built-ins' beside
        // them (ARNO's sheet had all four)
        ({ page, errors } = await open(browser, lang, { installed: true }, "providers"));
        assert.deepEqual(await qoderTiles(page), [[w.plugins, "Qoder"], [w.plugins, "Qoder CN"]]);
        // Qoder moved onto it: one tile, the built-in's, signing in through
        // the plugin; Qoder CN the plugin's
        ({ page, errors } = await open(browser, lang, { installed: true, moved: true }, "providers"));
        assert.deepEqual(await qoderTiles(page), [[w.subs, w.intl], [w.plugins, "Qoder CN"]]);
        // signed in to the built-in Qoder CN too: its provider is still
        // listed and works, the sheet the plugin's tiles alone
        ({ page, errors } = await open(browser, lang, { installed: true, signedIn: true }, "providers"));
        assert.equal(await page.locator(".row.provider", { hasText: "Qoder CN" }).count(), 1);
        assert.deepEqual(await qoderTiles(page), [[w.plugins, "Qoder"], [w.plugins, "Qoder CN"]]);
        assert.deepEqual(errors, []);
      });
    }
  });
}
