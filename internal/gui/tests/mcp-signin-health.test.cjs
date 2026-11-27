// Run with Node's test runner and Playwright on the module path; see README.md.
// 新v学员 on Discord: an MCP server signed in to with OAuth showed "Signed
// in" and "needs sign-in" at once. A server no agent has, checked by a
// click before its sign-in, kept that check's "needs sign-in" after it; it
// is checked again once its sign-in changed. And a check that found
// magpie's sign-in run out (the server refused even a renewed token) turns
// the row's tag to "Sign-in ran out". A renewal the server rate-limited
// (#7451, Asher-S-Wu: Canva's 429) reads "renewal rate-limited", in amber,
// beside a "Signed in" that still stands. Every language, Chromium and WebKit,
// 1100 and 390 wide; nothing scrolls sideways. No backend: the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = require("./strings.cjs").strings(); // i18n.js's and the plugins'
const tr = (lang, s, vars = {}) => ((lang === "en" ? s : I18N[lang][s]) || s).replace(/\{(\w+)\}/g, (_, k) => vars[k]);
const HOME = "/Users/xv";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });

function server(lang, calls) {
  const lib = {
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents: [agent("claude", "Claude Code")],
    instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [],
    servers: [
      // given to no agent: checked only on a click (#1597)
      { name: "notion", transport: "http", url: "https://mcp.notion.com/mcp", agents: [], signIn: { signedIn: false } },
      // signed in, and in use: checked as the tab opens
      { name: "linear", transport: "http", url: "https://mcp.linear.app/mcp", agents: ["claude"], signIn: { signedIn: true, at: 1 } },
      // signed in, its renewal turned away with 429
      { name: "canva", transport: "http", url: "https://mcp.canva.com/mcp", agents: ["claude"], signIn: { signedIn: true, at: 1 } },
      // signed in, its token endpoint down (5xx) for the renewal (#7915)
      { name: "figma", transport: "http", url: "https://mcp.figma.com/mcp", agents: ["claude"], signIn: { signedIn: true, at: 1 } },
    ],
  };
  let polls = 0;
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib });
    if (url.pathname === "/api/library/mcp/check") {
      const names = req.postDataJSON().names;
      calls.push("check " + names.join(","));
      const servers = {};
      for (const n of names) {
        const s = lib.servers.find((x) => x.name === n);
        // the server refused even a renewed token: the backend marked it run out
        if (n === "linear") s.signIn = { signedIn: true, dead: true, at: 1 };
        if (n === "linear") servers[n] = { state: "auth", code: 401, oauth: true, signIn: s.signIn };
        else if (n === "canva") servers[n] = { state: "error", why: "ratelimited", detail: "the token endpoint answered 429: too_many_requests (Too many refresh_token requests)", signIn: { ...s.signIn, retryAt: Date.now() + 60000 } };
        else if (n === "figma") servers[n] = { state: "error", why: "renewal-unavailable", detail: "sign-in renewal is temporarily unavailable: retry in 30s (the token endpoint answered 503)", signIn: { ...s.signIn, retryAt: Date.now() + 30000, retryUnavailable: true } };
        else servers[n] = s.signIn.signedIn ? { state: "ok", tools: 7, signIn: s.signIn } : { state: "auth", code: 401, oauth: true, signIn: s.signIn };
      }
      return route.fulfill({ json: { servers } });
    }
    if (url.pathname === "/api/library/mcp-signin") {
      const name = req.postDataJSON().name;
      calls.push("start " + name);
      polls = 0;
      return route.fulfill({ json: { id: "s-" + name, server: name, state: "waiting" } });
    }
    if (url.pathname.startsWith("/api/library/mcp-signin/s-")) {
      const name = url.pathname.slice("/api/library/mcp-signin/s-".length);
      if (++polls < 2) return route.fulfill({ json: { id: "s-" + name, server: name, state: "waiting" } });
      lib.servers.find((x) => x.name === name).signIn = { signedIn: true, at: 2 };
      return route.fulfill({ json: { id: "s-" + name, server: name, state: "done" } });
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

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a server's sign-in and its status agree", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 390]) {
        await t.test(`${lang} ${width}`, async () => {
          const w = (s, v) => tr(lang, s, v);
          const calls = [];
          const ctx = await browser.newContext({ viewport: { width, height: 800 } });
          await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
          const page = await ctx.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang, calls));
          await page.goto("http://magpie.test/");
          await page.locator('button[data-view="library"]').click();
          const row = (name) => page.locator("#view-library .lib-server", { has: page.locator(`.lib-health[data-server="${name}"]`) });
          const status = (name) => page.locator(`#view-library .lib-health[data-server="${name}"]`);
          const tags = (name) => row(name).locator(".name .lib-tag").allInnerTexts();

          // the check found magpie's sign-in to linear run out: the tag says so
          await status("linear").locator("span", { hasText: w("needs sign-in") }).waitFor();
          await row("linear").locator(".name .lib-tag", { hasText: w("Sign-in ran out") }).waitFor();
          assert.deepEqual(await tags("linear"), [w("Sign-in ran out")]);

          // canva's renewal waits out the server's 429: still signed in
          await status("canva").locator("span", { hasText: w("renewal rate-limited") }).waitFor();
          assert.equal(await status("canva").evaluate((b) => b.classList.contains("auth")), true);
          const tip = await status("canva").getAttribute("title");
          assert.ok(tip.includes(w("The server is limiting how often magpie renews its sign-in. It stays signed in, and magpie renews it once the wait is over")), tip);
          assert.deepEqual(await tags("canva"), [w("Signed in")]);

          // figma's renewal waits out its token endpoint's outage: still
          // signed in, amber, never "needs sign-in" (#7915)
          await status("figma").locator("span", { hasText: w("renewal failed, retrying") }).waitFor();
          assert.equal(await status("figma").evaluate((b) => b.classList.contains("auth")), true);
          const figmaTip = await status("figma").getAttribute("title");
          assert.ok(figmaTip.includes(w("The sign-in is kept. Renewal will be tried again after a wait")), figmaTip);
          assert.deepEqual(await tags("figma"), [w("Signed in")]);

          // notion, checked by a click, then signed in to
          await status("notion").locator("span", { hasText: w("not checked") }).waitFor();
          await status("notion").click();
          await status("notion").locator("span", { hasText: w("needs sign-in") }).waitFor();
          await row("notion").locator(".name").click();
          const box = page.locator("#modal .lib-signin");
          await box.getByRole("button", { name: w("Sign in"), exact: true }).click();
          await box.locator(".lib-tag", { hasText: w("Signed in") }).waitFor();
          await page.keyboard.press("Escape");
          await page.locator("#modal .lib-editor").waitFor({ state: "detached" });
          await status("notion").locator("span", { hasText: w("{n} tools", { n: 7 }) }).waitFor();
          assert.deepEqual(await tags("notion"), [w("Signed in")]);
          assert.deepEqual(calls.filter((c) => c.includes("notion")), ["check notion", "start notion", "check notion"]);
          // linear isn't checked again for its tag
          assert.equal(calls.filter((c) => c.startsWith("check ") && c.slice(6).split(",").includes("linear")).length, 1, calls.join("; "));
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
          await ctx.close();
        });
      }
    }
    assert.deepEqual(errors, []);
  });
}
