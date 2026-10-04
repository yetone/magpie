// Run with Node's test runner and Playwright on the module path; see README.md.
// #615: a remote MCP server is signed in to once, in magpie. Its editor has
// a Sign in that follows the sign-in to the end, the row then says Signed
// in, and Sign out puts it back; a server run as a command has none. In
// English and Chinese, and no click moves the page. No backend: the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/aimer";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const base = () => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("codex", "Codex"), agent("claude", "Claude Code")],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [],
  servers: [
    { name: "fs", transport: "stdio", command: "npx", args: ["fs-mcp"], agents: ["codex"] },
    { name: "neon", transport: "http", url: "https://mcp.neon.tech/mcp", agents: ["codex", "claude"], signIn: { signedIn: false } },
    { name: "exa", transport: "http", url: "https://mcp.exa.ai/mcp", agents: ["codex"], signIn: { signedIn: false } },
  ],
});
// mcpauth.ErrNoSignIn, as the API says it
const noSignIn = "this server already works without signing in, so there is nothing to sign in to: the agents given it can use it as it is. A key it takes goes in Headers; a server that signs in at another address (as Exa's ?login) needs that URL";

function server(lang, calls) {
  const lib = base();
  let polls = 0;
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib });
    if (url.pathname === "/api/library/mcp-signin") {
      const name = req.postDataJSON().name;
      calls.push("start " + name);
      // the backend signs in to the saved URL: Exa's plain one asks for none
      const s = lib.servers.find((x) => x.name === name);
      if (s.url === "https://mcp.exa.ai/mcp") return route.fulfill({ status: 400, json: { error: noSignIn } });
      polls = 0;
      // no url: the test's browser opens no window
      return route.fulfill({ json: { id: "s-" + name, server: name, state: "waiting" } });
    }
    if (url.pathname.startsWith("/api/library/mcp-signin/s-")) {
      const name = url.pathname.slice("/api/library/mcp-signin/s-".length);
      if (++polls < 2) return route.fulfill({ json: { id: "s-" + name, server: name, state: "waiting" } });
      lib.servers.find((x) => x.name === name).signIn = { signedIn: true, at: Date.now() };
      return route.fulfill({ json: { id: "s-" + name, server: name, state: "done" } });
    }
    if (url.pathname === "/api/library/servers/save") {
      const { old, server: s } = req.postDataJSON();
      calls.push("save " + s.name + " " + s.url + " " + JSON.stringify(s.headers || {}));
      const i = lib.servers.findIndex((x) => x.name === old);
      // a sign-in is to a URL: another one isn't signed in to
      const signIn = lib.servers[i].url === s.url ? lib.servers[i].signIn : { signedIn: false };
      lib.servers[i] = { ...lib.servers[i], ...s, signIn };
      return route.fulfill({ json: { ...lib, result: { changed: ["codex"] } } });
    }
    if (url.pathname === "/api/library/mcp-signout") {
      calls.push("out " + req.postDataJSON().name);
      lib.servers[1].signIn = { signedIn: false };
      return route.fulfill({ json: lib });
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

const words = {
  en: { signIn: "Sign in", signOut: "Sign out", signed: "Signed in", label: "Sign-in", done: "Signed in to neon — the agents given it use magpie's sign-in", out: "Signed out of neon — the agents are given the server's own address again", exaDone: "Signed in to exa — the agents given it use magpie's sign-in", noSignIn: "already works without signing in" },
  zh: { signIn: "登录", signOut: "退出登录", signed: "已登录", label: "登录", done: "已登录 neon——分配到它的 agent 都用 magpie 的登录", out: "已退出 neon——agent 重新使用服务器自己的地址", exaDone: "已登录 exa——分配到它的 agent 都用 magpie 的登录", noSignIn: "不登录就已经能用" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an MCP server is signed in to from its editor", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = words[lang];
        const calls = [];
        const ctx = await browser.newContext({ viewport: { width: 980, height: 800 } });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, calls));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const row = (name) => page.locator("#view-library .lib-row", { has: page.locator(".name", { hasText: new RegExp("^" + name) }) });
        await row("neon").waitFor();
        assert.equal(await row("neon").locator(".lib-tag").count(), 0, "not signed in: no tag");
        const scrolled = () => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("#view-library, #view-library *")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join(","));
        const before = await scrolled();

        // a command has nothing to sign in to
        await row("fs").click();
        await page.locator("#modal .lib-editor").waitFor();
        assert.equal(await page.locator("#modal .lib-signin").count(), 0);
        await page.keyboard.press("Escape");
        await page.locator("#modal .lib-editor").waitFor({ state: "detached" });

        await row("neon").click();
        const box = page.locator("#modal .lib-signin");
        await box.waitFor();
        assert.equal(await page.locator("#modal label", { hasText: new RegExp("^" + w.label + "$") }).count(), 1);
        await box.getByRole("button", { name: w.signIn, exact: true }).click();
        await page.waitForFunction((want) => document.querySelector("#status").textContent === want, w.done);
        assert.deepEqual(calls, ["start neon"]);
        await box.locator(".lib-tag", { hasText: w.signed }).waitFor();
        await row("neon").locator(".lib-tag", { hasText: w.signed }).waitFor();

        await box.getByRole("button", { name: w.signOut, exact: true }).click();
        await page.waitForFunction((want) => document.querySelector("#status").textContent === want, w.out);
        await box.getByRole("button", { name: w.signIn, exact: true }).waitFor();
        assert.deepEqual(calls, ["start neon", "out neon"]);
        await page.keyboard.press("Escape");
        await page.locator("#modal .lib-editor").waitFor({ state: "detached" });
        assert.equal(await row("neon").locator(".lib-tag").count(), 0, "signed out: no tag");

        // lc on Discord: Exa signs in at ?login, and Sign in before Save
        // signed in to the saved URL, which asks for none. Unchanged, it's
        // said to work as it is (in the reader's language), nothing saved
        await row("exa").click();
        const exa = page.locator("#modal .lib-signin");
        await exa.getByRole("button", { name: w.signIn, exact: true }).click();
        await exa.locator(".lib-signin-err", { hasText: w.noSignIn }).waitFor();
        assert.deepEqual(calls.slice(2), ["start exa"]);
        // the URL changed to ?login: Sign in saves it, the dialog stays,
        // and the sign-in is to it
        const urlField = page.locator("#modal .lib-editor input.mono").first();
        await urlField.fill("https://mcp.exa.ai/mcp?login");
        await exa.getByRole("button", { name: w.signIn, exact: true }).click();
        await page.waitForFunction((want) => document.querySelector("#status").textContent === want, w.exaDone);
        assert.deepEqual(calls.slice(3), ["save exa https://mcp.exa.ai/mcp?login {}", "start exa"]);
        await exa.locator(".lib-tag", { hasText: w.signed }).waitFor();
        assert.equal(await page.locator("#modal .lib-editor").count(), 1, "the dialog stays open");
        // signed in, Save is of the server as saved now; nothing changed,
        // nothing is saved again before a sign-in
        await page.locator("#modal .bar button.primary").click();
        await page.locator("#modal .lib-editor").waitFor({ state: "detached" });
        assert.deepEqual(calls.slice(5), ["save exa https://mcp.exa.ai/mcp?login {}"]);
        await row("exa").locator(".lib-tag", { hasText: w.signed }).waitFor();
        assert.equal(await scrolled(), before, "no click moved the page");
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
