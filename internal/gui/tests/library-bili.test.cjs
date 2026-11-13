// Run with Node's test runner and Playwright on the module path; see README.md.
// billion-context in the Library (@Kling__Lab on X): a tab beside RTK. With
// no bili the card shows billion-context's own npm install and Install
// billion-context; once it is installed the card shows its version and that
// an agent switched on can't also run billion-context-pi or opencode-acp,
// and each agent's switch posts {agent, on} and is held while it runs.
// What it saved (Scott__D on X) is billion-context's own count from its
// running proxy, as GET /api/library/bili's saved: net tokens, folds and
// sessions, the gross before costs in the scope note, "as of" a kept
// answer when no bili runs, none folded yet, and nothing before it ran.
// Every language, Chromium and WebKit, 1100 and 420 wide, nothing sideways.
// No backend: the API is faked here.
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
const npm = `npm install -g billion-context --prefix=${HOME}/.local`;
const view = (st) => st.installed
  ? { path: `${HOME}/.local/bin/bili`, version: "0.1.213", url: "https://github.com/ranxianglei/billion-context",
    agents: [{ id: "claude", name: "Claude Code", icon: "", on: st.on.claude }, { id: "pi", name: "Pi", icon: "", on: st.on.pi }], restart: st.restart, saved: st.saved }
  : { url: "https://github.com/ranxianglei/billion-context", install: npm, agents: [{ id: "claude", name: "Claude Code", icon: "", on: false }, { id: "pi", name: "Pi", icon: "", on: false }] };

function server(lang, st) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib() });
    if (url.pathname === "/api/library/bili" && req.method() === "GET") return route.fulfill({ json: view(st) });
    if (url.pathname === "/api/library/bili") {
      const body = JSON.parse(req.postData());
      st.posts.push(body);
      await st.hold;
      st.on[body.agent] = body.on;
      st.restart = [body.agent];
      return route.fulfill({ json: view(st) });
    }
    if (url.pathname === "/api/library/bili/install") {
      st.installs++;
      st.installed = true;
      return route.fulfill({ json: view(st) });
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
const english = "An agent switched on here can't also run billion-context-pi or opencode-acp.";
const NONE = "What billion-context saves shows here once it has run for an agent.";
// /__bili/overview of bili 0.1.213 after ten turns on mote, as magpie
// passes it on (internal/library/testdata/bili/overview-0.1.213.json)
const SAVED = { net: 166285, gross: 166390, hasFolds: true, folds: 7, sessions: 1, requests: 10, input: 175279, at: "2026-11-10T07:21:30Z", live: true, version: "0.1.213" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": billion-context installs from its tab and switches each agent", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of LANGS) for (const width of [1100, 420]) {
      await t.test(`${lang} ${width}`, async () => {
        let release;
        const st = { installed: false, installs: 0, on: { claude: false, pi: false }, posts: [], hold: new Promise((r) => { release = r; }) };
        const ctx = await browser.newContext({ viewport: { width, height: 800 } });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, st));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        // its tab, right after RTK's
        // the page's tabs, or the side nav's at a wide window
        const tabs = page.locator(":is(#view-library .lib-tabs .opt, .nav-s):visible");
        await tabs.first().waitFor();
        // the sidebar's list is drawn again in sections once its marks are loaded
        await page.waitForFunction(() => !document.querySelector(".nav-sub .nav-s") || window.navSections);
        const names = await tabs.allTextContents();
        assert.equal(names[names.indexOf("RTK") + 1], "billion-context");
        await tabs.filter({ hasText: /^billion-context$/ }).click();

        // not installed: the npm command, and Install billion-context
        const cmd = v.locator(".lib-bili .lib-rtk-cmd");
        await cmd.waitFor();
        assert.equal(await cmd.textContent(), npm);
        const sw = v.locator(".lib-list .lib-switch");
        assert.equal(await sw.count(), 2);
        assert(await sw.first().isDisabled(), "a switch works with no bili");
        await v.locator(".lib-bili .lib-acts button").first().click();

        // installed: its version, the note, and the switches
        await v.locator(".lib-bili .note.mono").waitFor();
        assert.equal(await v.locator(".lib-bili .note.mono").textContent(), "v0.1.213");
        assert.equal(st.installs, 1);
        const note = await v.locator(".lib-bili .lib-rtk-gain:not(.lib-bili-saved)").textContent();
        // no bili has answered yet: said so, nothing counted
        const none = await v.locator(".lib-bili .lib-bili-saved").textContent();
        assert(!/\d/.test(none), none);
        if (lang !== "en") assert.notEqual(none, NONE);
        assert(note.includes("billion-context-pi") && note.includes("opencode-acp"), note);
        if (lang !== "en") assert.notEqual(note, english);
        const intro = await v.locator(".lib-body > .lib-intro").textContent();
        if (lang !== "en") assert(!intro.startsWith("billion-context folds"), intro);
        assert.equal(await sw.first().isDisabled(), false);

        // Pi switched on: held while it runs, then on
        await sw.nth(1).click();
        await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-list .lib-switch")[1]?.disabled);
        release();
        await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-list .lib-switch")[1]?.getAttribute("aria-checked") === "true");
        assert.deepEqual(st.posts, [{ agent: "pi", on: true }]);
        assert.equal(await sw.first().getAttribute("aria-checked"), "false");
        const sideways = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert(sideways <= 0, `the page scrolls sideways by ${sideways}px`);
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": billion-context's card says what it saved, in its own count", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    const cases = {
      live: SAVED,
      kept: { ...SAVED, live: false },
      unfolded: { ...SAVED, net: 0, gross: 0, hasFolds: false, folds: 0, requests: 3 },
    };
    for (const lang of LANGS) for (const width of [1100, 420]) for (const [name, saved] of Object.entries(cases)) {
      await t.test(`${lang} ${width} ${name}`, async () => {
        const st = { installed: true, installs: 0, on: { claude: false, pi: true }, posts: [], saved };
        const ctx = await browser.newContext({ viewport: { width, height: 800 } });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, st));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        const tabs = page.locator(":is(#view-library .lib-tabs .opt, .nav-s):visible");
        await tabs.first().waitFor();
        // the sidebar's list is drawn again in sections once its marks are loaded
        await page.waitForFunction(() => !document.querySelector(".nav-sub .nav-s") || window.navSections);
        await tabs.filter({ hasText: /^billion-context$/ }).click();
        const line = v.locator(".lib-bili .lib-bili-saved");
        await line.waitFor();
        const text = await line.textContent();
        assert(!text.includes("{"), text);
        if (name === "unfolded") {
          assert(text.includes("3") && !text.includes("166"), text);
          if (lang === "en") assert(text.startsWith("Nothing folded yet over 3 requests"), text);
        } else {
          // the net figure, then the gross in bili's own words
          assert(text.includes("166.3k") && text.includes("166.4k") && text.includes("7"), text);
          if (lang === "en") assert(text.startsWith("166.3k tokens saved over 7 folds in 1 session"), text);
          else assert(!text.includes("tokens saved"), text);
        }
        assert.equal(await v.locator(".lib-bili .lib-bili-asof").count(), name === "kept" ? 1 : 0);
        if (name === "kept") {
          const asof = await v.locator(".lib-bili .lib-bili-asof").textContent();
          assert(/2026/.test(asof), asof);
          if (lang !== "en") assert(!asof.startsWith("As of"), asof);
        }
        const sideways = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert(sideways <= 0, `the page scrolls sideways by ${sideways}px`);
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
