// Run with Node's test runner and Playwright on the module path; see README.md.
// billion-context.json's settings in the Library's billion-context tab
// (@Luxzinw on X): below the agents, the summary model with its switch,
// fold only when nudged, and the nudge step in front; the compression
// philosophy under Advanced, folded until opened. Each change posts only
// what changed to /api/library/bili/settings, and the status says whether
// the running billion-context reloaded it. The model is picked from the
// app's menu (never a <select>): magpie's models and the file's own
// recipes. A file billion-context can't read is said so, with nothing to
// change. Every language, Chromium and WebKit, 1100 and 420 wide, nothing
// sideways. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/aimer";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const lib = () => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("pi", "Pi")],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], servers: [],
});
const bili = {
  path: `${HOME}/.local/bin/bili`, version: "0.1.213", url: "https://github.com/ranxianglei/billion-context",
  agents: [{ id: "claude", name: "Claude Code", icon: "", on: true }, { id: "pi", name: "Pi", icon: "", on: false }],
};
const MODELS = [
  { id: "gpt-5.4-mini", name: "GPT-5.4 mini", provider: "codex", providerName: "Codex" },
  { id: "glm-4.9-flash", name: "GLM-4.9 Flash", provider: "zhipu", providerName: "Zhipu" },
];
// /api/library/bili/settings as bili_config.go answers it
const settings = (st) => st.bad
  ? { path: `${HOME}/.config/billion-context/billion-context.json`, bad: "billion-context.json isn't JSON billion-context can read", targets: [], recipes: [], models: MODELS }
  : { path: `${HOME}/.config/billion-context/billion-context.json`, growth: st.growth, nudge: st.nudge, summary: st.summary, targets: st.targets,
    recipes: ["claude/claude-haiku"], philosophy: st.philosophy, risk: !!st.philosophy, reloaded: st.reloaded, running: 1, models: MODELS };

function server(lang, st) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib() });
    if (url.pathname === "/api/library/bili") return route.fulfill({ json: bili });
    if (url.pathname === "/api/library/bili/settings" && req.method() === "GET") return route.fulfill({ json: settings(st) });
    if (url.pathname === "/api/library/bili/settings") {
      const c = JSON.parse(req.postData());
      st.posts.push(c);
      if ("growth" in c) st.growth = c.growth;
      if ("nudge" in c) st.nudge = c.nudge;
      if ("model" in c) st.targets = ["magpie/" + c.model, ...st.targets.filter((x) => x !== "magpie/" + c.model)];
      if ("target" in c) st.targets = [c.target, ...st.targets.filter((x) => x !== c.target)];
      if ("summary" in c) st.summary = c.summary;
      if ("philosophy" in c) st.philosophy = c.philosophy;
      st.reloaded = 1;
      return route.fulfill({ json: settings(st) });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const SUMMARY = "Summarize with a fast model";
const RELOADED = "Saved — the running billion-context uses it now";

async function open(page, lang, st) {
  await page.route("**/*", server(lang, st));
  await page.goto("http://magpie.test/");
  await page.locator('button[data-view="library"]').click();
  const tabs = page.locator(":is(#view-library .lib-tabs .opt, .nav-s):visible");
  await tabs.first().waitFor();
  await page.waitForFunction(() => !document.querySelector(".nav-sub .nav-s") || window.navSections);
  // by its name, not its place: the sidebar's list can be drawn after the
  // tabs are read, in its own order, and an index then names another tab
  await tabs.filter({ hasText: /^billion-context$/ }).click();
  // the first page of a fresh WebKit loads every script cold: room for
  // that, not a bar on it
  await page.locator("#view-library .lib-bili-set").waitFor({ timeout: 30000 });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": billion-context.json's settings change from its tab", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of LANGS) for (const width of [1100, 420]) {
      await t.test(`${lang} ${width}`, async () => {
        const st = { growth: 0, nudge: false, summary: false, targets: [], philosophy: "", posts: [], reloaded: 0 };
        const ctx = await browser.newContext({ viewport: { width, height: 900 } });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await open(page, lang, st);
        const v = page.locator("#view-library");
        const set = v.locator(".lib-bili-set");
        await set.waitFor();
        const rows = set.locator(".lib-row");
        assert.equal(await rows.count(), 3);
        const first = (await rows.first().locator(".name").textContent()).trim();
        if (lang !== "en") assert.notEqual(first, SUMMARY);
        else assert.equal(first, SUMMARY);
        // no <select> anywhere; the philosophy folded away
        assert.equal(await v.locator("select").count(), 0);
        const more = v.locator(".lib-bili-more");
        assert.equal(await more.evaluate((d) => d.open), false, "Advanced is open at first");
        assert.equal(await v.locator(".lib-bili-phil textarea").isVisible(), false);
        // no model yet: the summary can't be switched on
        const sum = rows.nth(0).locator(".lib-switch");
        assert(await sum.isDisabled(), "summary switch works with no model");

        // a model of magpie's: picked and switched on in one go
        await rows.nth(0).locator(".lib-bili-model").click();
        const items = page.locator(".proto-menu .pm-item");
        assert.equal(await items.count(), 3, "magpie's two models and the file's recipe");
        await page.locator(".proto-menu .pm-item", { hasText: "GPT-5.4 mini" }).click();
        await page.waitForFunction(() => document.querySelector("#view-library .lib-bili-set .lib-switch")?.getAttribute("aria-checked") === "true");
        assert.deepEqual(st.posts.at(-1), { model: "gpt-5.4-mini", summary: true });
        assert.match(await rows.nth(0).locator(".lib-bili-model").textContent(), /GPT-5\.4 mini/);
        const said = (await page.locator("#status").textContent()).trim();
        if (lang !== "en") assert.notEqual(said, RELOADED);
        else assert.equal(said, RELOADED);

        // fold only when nudged
        await rows.nth(1).locator(".lib-switch").click();
        await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-bili-set .lib-switch")[1]?.getAttribute("aria-checked") === "true");
        assert.deepEqual(st.posts.at(-1), { nudge: true });

        // the nudge step, bili's default as the placeholder
        const num = rows.nth(2).locator("input");
        assert.equal(await num.getAttribute("placeholder"), "50000");
        await num.fill("80000");
        await num.press("Enter");
        await page.waitForFunction(() => document.querySelector("#view-library .lib-bili-growth")?.value === "80000" && !document.querySelector("#view-library .lib-bili-growth").disabled);
        assert.deepEqual(st.posts.at(-1), { growth: 80000 });

        // Advanced: the philosophy, saved
        await more.locator("summary").click();
        const ta = v.locator(".lib-bili-phil textarea");
        await ta.waitFor();
        await ta.fill("Keep every file path verbatim.");
        await v.locator(".lib-bili-phil .lib-acts button").click();
        await page.waitForFunction(() => document.querySelector("#view-library .lib-bili-phil textarea")?.value === "Keep every file path verbatim." && !document.querySelector("#view-library .lib-bili-phil textarea").disabled);
        assert.deepEqual(st.posts.at(-1), { philosophy: "Keep every file path verbatim." });
        assert.equal(await more.evaluate((d) => d.open), true, "Advanced closed again on its own");
        assert.equal(st.posts.length, 4);

        const sideways = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert(sideways <= 0, `the page scrolls sideways by ${sideways}px`);
        for (const c of await set.locator(".lib-row > *").all()) {
          const r = await c.boundingBox(), card = await set.boundingBox();
          if (r) assert(r.x + r.width <= card.x + card.width + 1, "a control of the settings is cut off");
        }
        await ctx.close();
      });
    }

    // a file billion-context can't read: said so, nothing to change
    for (const lang of LANGS) {
      await t.test(`${lang} unreadable`, async () => {
        const st = { bad: true, posts: [] };
        const ctx = await browser.newContext({ viewport: { width: 1100, height: 800 } });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await open(page, lang, st);
        const set = page.locator("#view-library .lib-bili-set");
        await set.waitFor();
        assert.equal(await set.locator(".lib-row").count(), 0);
        assert.equal(await set.locator(".lib-none").count(), 1);
        assert.equal(await page.locator("#view-library .lib-bili-more").count(), 0);
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
