// Run with Node's test runner and Playwright on the module path; see README.md.
// 新v学员 on Discord: GitHub's MCP server, brought in from CC Switch with its
// token as an Authorization header. They removed the header, picked Sign
// in, then Cancel without saving, and the header was gone all the same:
// Sign in had saved the whole form first. Cancel leaves the server as
// saved, whatever field was changed (headers, a header's value, env, the
// command's arguments). Sign in saves first only for another server than
// the saved one (a URL changed, a new server), and its button then says
// "Save and sign in"; what that saved, Cancel keeps without asking. Every
// language, Chromium and WebKit, 1100 and 390 wide; nothing scrolls
// sideways. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = require("./strings.cjs").strings(); // i18n.js's and the plugins'
const tr = (lang, s) => (lang === "en" ? s : I18N[lang][s]) || s;
const HOME = "/Users/xv";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const GH = "https://api.githubcopilot.com/mcp/";
const TOKEN = "Bearer ghp_0000000000000000000000000000000000AB";

function server(lang, calls) {
  const lib = {
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents: [agent("claude", "Claude Code")],
    instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [],
    servers: [
      // as CC Switch's mcp_servers row for GitHub's remote server brings it in
      { name: "github", transport: "http", url: GH, headers: { Authorization: TOKEN }, agents: ["claude"], signIn: { signedIn: false } },
      { name: "gh-local", transport: "stdio", command: "npx", args: ["-y", "@modelcontextprotocol/server-github"], env: { GITHUB_PERSONAL_ACCESS_TOKEN: "ghp_local" }, agents: ["claude"] },
    ],
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib });
    if (url.pathname === "/api/library/mcp/check") return route.fulfill({ json: { servers: {} } });
    if (url.pathname === "/api/library/mcp-signin") {
      calls.push("start " + req.postDataJSON().name);
      // no url: the test's browser opens no window
      return route.fulfill({ json: { id: "s1", server: req.postDataJSON().name, state: "waiting" } });
    }
    if (url.pathname === "/api/library/mcp-signin/s1/cancel") { calls.push("cancel"); return route.fulfill({ json: {} }); }
    if (url.pathname === "/api/library/mcp-signin/s1") return route.fulfill({ json: { id: "s1", state: "waiting" } });
    if (url.pathname === "/api/library/servers/save") {
      const { old, server: s } = req.postDataJSON();
      calls.push("save " + s.name + " " + (s.url || s.command) + " " + JSON.stringify(s.headers || s.env || {}));
      const i = lib.servers.findIndex((x) => x.name === old);
      lib.servers[i] = { ...lib.servers[i], ...s };
      return route.fulfill({ json: { ...lib, result: { changed: ["claude"] } } });
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
  test(engine + ": Cancel leaves an MCP server as saved", async (t) => {
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
          const w = (s) => tr(lang, s);
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
          const ed = page.locator("#modal .lib-editor");
          const box = ed.locator(".lib-signin");
          const pairs = () => ed.locator(".lib-pair input").evaluateAll((l) => l.map((i) => i.value));
          const open = async (name) => { await row(name).locator(".name").click(); await ed.waitFor(); };
          // Cancel, and Discard when asked; asked is whether it was
          const cancel = async (asked) => {
            await ed.locator(":scope > .bar button", { hasText: new RegExp("^" + w("Cancel") + "$") }).click();
            const ask = page.locator("dialog.action-confirm");
            if (asked) await ask.locator("button.primary", { hasText: w("Discard") }).click();
            await ed.waitFor({ state: "detached" });
            assert.equal(await ask.count(), 0, "asked to discard");
          };
          const stopSignIn = async () => {
            await box.getByRole("button", { name: w("Cancel"), exact: true }).click();
            await box.getByRole("button", { name: w("Sign in"), exact: true }).waitFor();
          };

          // the reporter's case: header removed, Sign in, Cancel
          await open("github");
          assert.deepEqual(await pairs(), ["Authorization", TOKEN]);
          await ed.locator(".lib-pair .lib-x").click();
          assert.deepEqual(await pairs(), []);
          await box.getByRole("button", { name: w("Sign in"), exact: true }).click();
          await stopSignIn();
          assert.deepEqual(calls, ["start github", "cancel"], "Sign in saved nothing");
          await cancel(true);
          await open("github");
          assert.deepEqual(await pairs(), ["Authorization", TOKEN], "the header is still there");

          // a header's value changed, then Escape
          await ed.locator(".lib-pair input").nth(1).fill("Bearer other");
          await page.keyboard.press("Escape");
          await page.locator("dialog.action-confirm button.primary").click();
          await ed.waitFor({ state: "detached" });
          await open("github");
          assert.deepEqual(await pairs(), ["Authorization", TOKEN]);
          await cancel(false);

          // a command's env and arguments
          await open("gh-local");
          assert.equal(await box.count(), 0);
          await ed.locator("textarea.lib-cmd").fill("npx -y @modelcontextprotocol/server-github --read-only");
          await ed.locator(".lib-pair .lib-x").click();
          await cancel(true);
          await open("gh-local");
          assert.equal(await ed.locator("textarea.lib-cmd").inputValue(), "npx -y @modelcontextprotocol/server-github");
          assert.deepEqual(await pairs(), ["GITHUB_PERSONAL_ACCESS_TOKEN", "ghp_local"]);
          await cancel(false);
          assert.deepEqual(calls.filter((c) => c.startsWith("save")), [], "nothing saved");

          // another URL: the button says it saves, and what it saved Cancel keeps
          await open("github");
          await ed.locator("input.mono").first().fill(GH + "readonly");
          await ed.locator(".lib-pair .lib-x").click();
          await box.getByRole("button", { name: w("Save and sign in"), exact: true }).click();
          await box.getByRole("button", { name: w("Cancel"), exact: true }).waitFor();
          await page.waitForFunction(() => document.querySelector("#modal .lib-editor > .bar button.primary"));
          await box.getByRole("button", { name: w("Cancel"), exact: true }).click();
          // saved: the same URL is no other server, so the button says Sign in
          await box.getByRole("button", { name: w("Sign in"), exact: true }).waitFor();
          assert.deepEqual(calls.slice(2), ["save github " + GH + "readonly {}", "start github", "cancel"]);
          await cancel(false);
          await row("github").locator(".sub", { hasText: GH + "readonly" }).waitFor();

          // a new server's sign-in saves it first, and says so
          const add = page.locator("#view-library .after-list button");
          // scrolled to with the wheel, as a reader does (the page puts back
          // a scroll no reader asked for): narrow, it's under the footer
          await page.locator("#view-library").hover();
          await page.mouse.wheel(0, 2000);
          await page.waitForFunction(() => { const v = document.querySelector("#view-library"); return v.scrollTop + v.clientHeight >= v.scrollHeight - 1; });
          await add.click();
          await ed.waitFor();
          await ed.locator(".segs .opt", { hasText: /^HTTP$/ }).click();
          await box.getByRole("button", { name: w("Save and sign in"), exact: true }).waitFor();
          await cancel(true);
          assert.equal(calls.length, 5, calls.join("; "));
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
          await ctx.close();
        });
      }
    }
    assert.deepEqual(errors, []);
  });
}
