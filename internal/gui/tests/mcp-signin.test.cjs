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
    { name: "figma", transport: "http", url: "https://mcp.figma.com/mcp", agents: ["claude"], signIn: { signedIn: false } },
    { name: "sofa", transport: "http", url: "https://agents.stackoverflow.com/mcp", agents: ["claude"], signIn: { signedIn: false } },
    { name: "notion", transport: "http", url: "https://mcp.notion.com/mcp", agents: ["claude"], signIn: { signedIn: false } },
    { name: "linear", transport: "http", url: "https://mcp.linear.app/mcp", agents: ["claude"], signIn: { signedIn: false } },
  ],
});
// mcpauth.ErrNoSignIn, as the API says it
const noSignIn = "this server already works without signing in, so there is nothing to sign in to: the agents given it can use it as it is. A key it takes goes in Headers; a server that signs in at another address (as Exa's ?login) needs that URL";
// mcpauth.ErrClientRefused: Figma's registration 403s any app it doesn't list (#1619)
const refused = "this server's sign-in admits only the apps it lists and turned magpie away, so magpie can't sign in for the agents. The agents given it still have the server's own address: sign in from each agent itself (in Claude Code, run /mcp and pick the server)";

// mcpauth.ErrTurnedAway: a firewall's 403 page in front of the server
// (fremontdango on Discord, Cloudflare at agents.stackoverflow.com)
const turnedAway = "a firewall in front of this server turned magpie's request away (403) before the server could offer a sign-in. It judges the network the request came from: try again on another network, or give magpie a proxy in Settings";

// mcpauth.UnreachableError as api.go says it: the key of unreachableSaid
// by whether a proxy was used, with the host, the proxy and Go's words
// (CHENGJIAHEGOXIS on X read Notion's bare "couldn't reach mcp.notion.com:
// Post …: EOF" as Notion having no sign-in)
const unreachable = (host, proxy) => ({
  code: "unreachable", host, proxy, detail: `Post "https://${host}/mcp": EOF`,
  error: `couldn't reach ${host} (${proxy ? "through the proxy " + proxy : "directly, with no proxy"}): Post "https://${host}/mcp": EOF`,
  said: proxy
    ? "magpie got no answer from {host} through the proxy {proxy}, so the sign-in couldn't start. The server's sign-in isn't the problem: the way to {host} is. Check that the proxy reaches {host}, or pick another in Settings › Network and sharing, then click Sign in again ({detail})"
    : "magpie got no answer from {host}, going direct with no proxy, so the sign-in couldn't start. The server's sign-in isn't the problem: the network between this computer and {host} is. Give magpie a proxy in Settings › Network and sharing, or try another network, then click Sign in again ({detail})",
});

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
      if (s.url === "https://mcp.figma.com/mcp") return route.fulfill({ status: 400, json: { error: refused } });
      if (s.url === "https://agents.stackoverflow.com/mcp") return route.fulfill({ status: 400, json: { error: turnedAway } });
      if (s.url === "https://mcp.notion.com/mcp") return route.fulfill({ status: 400, json: unreachable("mcp.notion.com", "") });
      if (s.url === "https://mcp.linear.app/mcp") return route.fulfill({ status: 400, json: unreachable("mcp.linear.app", "http://127.0.0.1:7890") });
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
  en: { signIn: "Sign in", signOut: "Sign out", signed: "Signed in", label: "Sign-in", done: "Signed in to neon — the agents given it use magpie's sign-in", out: "Signed out of neon — the agents are given the server's own address again", exaDone: "Signed in to exa — the agents given it use magpie's sign-in", noSignIn: "already works without signing in", refused: "admits only the apps it lists", turnedAway: "a firewall in front of this server", saveSignIn: "Save and sign in",
    direct: "magpie got no answer from mcp.notion.com, going direct with no proxy, so the sign-in couldn't start. The server's sign-in isn't the problem", viaProxy: "magpie got no answer from mcp.linear.app through the proxy http://127.0.0.1:7890, so the sign-in couldn't start" },
  zh: { signIn: "登录", signOut: "退出登录", signed: "已登录", label: "登录", done: "已登录 neon，分配到它的 agent 共用 magpie 的登录", out: "已退出 neon，agent 改回使用服务器自身的地址", exaDone: "已登录 exa，分配到它的 agent 共用 magpie 的登录", noSignIn: "无需登录即可使用", refused: "只接受它列出的应用", turnedAway: "服务器前面的防火墙拦下了", saveSignIn: "保存并登录",
    direct: "magpie 直连（未用代理）mcp.notion.com 没有得到回应，所以登录没能开始。问题不在服务器的登录", viaProxy: "magpie 通过代理 http://127.0.0.1:7890 访问 mcp.linear.app 没有得到回应" },
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
        page.setDefaultNavigationTimeout(60000);
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
        // the URL changed to ?login: Sign in saves it, and says so (新v学员
        // on Discord), the dialog stays, and the sign-in is to it
        const urlField = page.locator("#modal .lib-editor input.mono").first();
        await urlField.fill("https://mcp.exa.ai/mcp?login");
        await exa.getByRole("button", { name: w.saveSignIn, exact: true }).click();
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

        // Xichun123 (#1619): Figma admits only the apps it lists. The
        // refusal says so, in the reader's language, and that each agent
        // signs in itself, not a bare "(403): Forbidden"
        await row("figma").click();
        const figma = page.locator("#modal .lib-signin");
        await figma.getByRole("button", { name: w.signIn, exact: true }).click();
        await figma.locator(".lib-signin-err", { hasText: w.refused }).waitFor();
        assert.match(await figma.locator(".lib-signin-err").textContent(), /\/mcp/);
        assert.deepEqual(calls.slice(6), ["start figma"]);
        await page.keyboard.press("Escape");
        await page.locator("#modal .lib-editor").waitFor({ state: "detached" });

        // a firewall's 403 says it was the firewall, judging the network,
        // in the reader's language, not "says nothing of a sign-in"
        await row("sofa").click();
        const sofa = page.locator("#modal .lib-signin");
        await sofa.getByRole("button", { name: w.signIn, exact: true }).click();
        await sofa.locator(".lib-signin-err", { hasText: w.turnedAway }).waitFor();
        assert.deepEqual(calls.slice(7), ["start sofa"]);
        await page.keyboard.press("Escape");
        await page.locator("#modal .lib-editor").waitFor({ state: "detached" });

        // a server magpie got no answer from: the network, said with the
        // way it went and Go's words, in the reader's language, never read
        // as the server having no sign-in
        for (const [name, want] of [["notion", w.direct], ["linear", w.viaProxy]]) {
          await row(name).click();
          const box = page.locator("#modal .lib-signin");
          await box.getByRole("button", { name: w.signIn, exact: true }).click();
          await box.locator(".lib-signin-err", { hasText: want }).waitFor();
          assert.match(await box.locator(".lib-signin-err").textContent(), /mcp": EOF/);
          await page.keyboard.press("Escape");
          await page.locator("#modal .lib-editor").waitFor({ state: "detached" });
        }
        assert.deepEqual(calls.slice(8), ["start notion", "start linear"]);
        assert.equal(await scrolled(), before, "no click moved the page");
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
