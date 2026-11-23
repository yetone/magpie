// Run with Node's test runner and Playwright on the module path; see README.md.
// #7549 (jinpwng): a Library plugin installed from a git source pinned to a
// release tag says so in its dialog, with the repository's newer stable
// tags (GET /api/library/plugin-tags), and a button for each that posts
// plugins/upgrade {name, tag}; Update stays and posts plugins/update. A
// plugin with no tag pin doesn't ask for tags. Chromium and WebKit, every
// language, at 420 and 1100 with nothing wider than the window. No
// backend: the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/chuan";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const PINNED = { name: "acme", version: "1.0.0", agents: ["claude"], skills: ["tidy"], source: "https://github.com/acme/kit/tree/v1.0.0/plugins/acme", installed: "2026-11-20" };
const LOOSE = { name: "loose", version: "2.0.0", agents: ["claude"], skills: ["x"], source: "https://github.com/acme/loose", installed: "2026-11-20" };
const lib = (plugins) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code")],
  instructions: { agents: [] }, servers: [], skills: [], foundServers: [], projects: [], foundSkills: [], problems: [],
  hooks: [], hookAgents: [], subagents: [], subagentAgents: [], plugins,
});

let I18N;
function tr(lang, key, vars = {}) {
  let s = key;
  if (lang !== "en") {
    assert.equal(typeof I18N[lang][key], "string", `${lang} has no ${JSON.stringify(key)}`);
    s = I18N[lang][key];
  }
  return s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m));
}

function server(lang, state, posts, asks) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(lib(state.plugins));
    if (url.pathname === "/api/library/plugin-tags") {
      const name = url.searchParams.get("name");
      asks.push(name);
      const p = state.plugins.find((x) => x.name === name);
      const pin = /\/tree\/(v[\d.]+)/.exec(p.source)?.[1];
      return json({ name, pin, newer: pin === "v1.0.0" ? ["v1.2.0", "v1.1.0"] : [] });
    }
    if (url.pathname.startsWith("/api/library/") && req.method() === "POST") {
      const body = req.postDataJSON(), what = url.pathname.replace("/api/library/", "");
      posts.push({ what, body });
      if (what === "plugins/upgrade") state.plugins = state.plugins.map((x) => (x.name === body.name ? { ...x, source: x.source.replace(/v[\d.]+/, body.tag) } : x));
      return json({ ...lib(state.plugins), result: { changed: ["claude"] } });
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const settled = (page) => page.waitForFunction(() => !document.querySelector("#modal").getAnimations({ subtree: true }).length);
const sideways = (page) => page.evaluate(() => {
  const over = [];
  const w = document.documentElement.clientWidth;
  if (document.documentElement.scrollWidth > w) over.push("page " + document.documentElement.scrollWidth);
  for (const e of document.querySelectorAll("#modal .lib-pldetail *")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push((e.className || e.tagName) + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  return over;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a plugin pinned to a release tag offers the newer ones", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1100, 420]) {
      await t.test(`${lang} at ${width}px`, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const state = { plugins: [{ ...PINNED }, { ...LOOSE }] };
        const posts = [], asks = [], errors = [];
        const ctx = await browser.newContext({ viewport: { width, height: 1000 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "plugins"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
        await page.route("**/*", server(lang, state, posts, asks));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const row = (name) => page.locator("#view-library .lib-plugin").filter({ has: page.locator(".name", { hasText: new RegExp("^" + name + "( |$)") }) });
        await row("acme").waitFor();

        // the plugin with no pin asks for no tags
        await row("loose").locator(".name").click();
        const d = page.locator("#modal .lib-pldetail");
        await d.waitFor();
        await settled(page);
        await page.waitForTimeout(150);
        assert.equal(await d.locator(".lib-plpin").count(), 0);
        assert.deepEqual(asks, []);
        await d.locator(".bar button", { hasText: w("Cancel") }).click();
        await d.waitFor({ state: "detached" });

        await row("acme").locator(".name").click();
        await d.waitFor();
        const pin = d.locator(".lib-plpin");
        await pin.locator(".hint").waitFor();
        await settled(page);
        assert.equal((await pin.locator(".hint").textContent()).trim(), w("Pinned to {tag}. Newer releases:", { tag: "v1.0.0" }));
        const ups = pin.locator(".lib-plpintags button");
        assert.deepEqual((await ups.allTextContents()).map((x) => x.trim()), [w("Update to {tag}", { tag: "v1.2.0" }), w("Update to {tag}", { tag: "v1.1.0" })]);
        // Update is still there, and keeps the pin
        assert.equal(await d.locator(".bar button", { hasText: new RegExp("^" + w("Update") + "$") }).count(), 1);
        assert.deepEqual(asks, ["acme"]);
        assert.deepEqual(await sideways(page), [], "sideways scroll at " + width);

        await ups.first().click();
        await d.waitFor({ state: "detached" });
        assert.deepEqual(posts, [{ what: "plugins/upgrade", body: { name: "acme", tag: "v1.2.0" } }]);

        // at the newest release: said so, nothing to press
        await row("acme").locator(".name").click();
        await d.waitFor();
        await pin.locator(".hint").waitFor();
        assert.equal((await pin.locator(".hint").textContent()).trim(), w("Pinned to {tag}, the newest release.", { tag: "v1.2.0" }));
        assert.equal(await pin.locator("button").count(), 0);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
