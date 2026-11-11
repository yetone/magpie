// Run with Node's test runner and Playwright on the module path; see README.md.
// Elorynth on X: a remote MCP server holds several Authorization headers or
// several OAuth sign-ins, one in use, switched in place. With one, the
// editor looks as it did, plus a quiet "Add account". In every language, in
// both engines. No backend: the API is faked here.
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
    { name: "web", transport: "http", url: "https://web.example/mcp", headers: { Authorization: "Bearer base" }, agents: ["codex", "claude"] },
    { name: "two", transport: "http", url: "https://two.example/mcp", headers: {}, agents: ["codex"], account: "2",
      accounts: [{ id: "1", name: "personal", headers: { Authorization: "Bearer p" } }, { id: "2", name: "work", headers: { Authorization: "Bearer w" } }] },
    { name: "one", transport: "http", url: "https://one.example/mcp", agents: ["codex"], signIn: { signedIn: true, at: 1 } },
    { name: "many", transport: "http", url: "https://many.example/mcp", agents: ["codex"],
      signIn: { signedIn: true, at: 1, accounts: [{ id: "1", label: "me", active: true }, { id: "2", label: "team" }] } },
  ],
});

function server(calls) {
  const lib = base();
  const lang = process.env.LANG_UNDER_TEST;
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib });
    if (url.pathname === "/api/library/mcp-account") {
      const b = req.postDataJSON();
      calls.push([b.name, b.kind, b.op, b.id ?? "", b.label ?? "", JSON.stringify(b.headers ?? {})].join("|"));
      const s = lib.servers.find((x) => x.name === b.name);
      if (b.kind === "header") {
        s.accounts = s.accounts || [];
        if (b.op === "add") { const id = String(s.accounts.length + 1); s.accounts.push({ id, name: b.label, headers: b.headers }); s.account = id; }
        if (b.op === "use") s.account = b.id;
        if (b.op === "remove") { s.accounts = s.accounts.filter((a) => a.id !== b.id); if (s.account === b.id) s.account = ""; if (!s.accounts.length) delete s.accounts; }
      } else if (b.op === "use") s.signIn.accounts.forEach((a) => { a.active = a.id === b.id; });
      return route.fulfill({ json: lib });
    }
    if (url.pathname === "/api/library/mcp-signin") {
      calls.push("signin " + JSON.stringify(req.postDataJSON()));
      return route.fulfill({ status: 400, json: { error: "stop here" } });
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

// core words (i18n.js) and the Library plugin's own
const words = {
  en: { add: "＋ Add account", name: "Name", def: "Default", addBtn: "Add", accounts: "Accounts", signIn: "Sign in" },
  zh: { add: "＋ 添加账号", name: "名称", def: "默认", addBtn: "添加", accounts: "账号", signIn: "登录" },
  "zh-TW": { add: "＋ 新增帳號", name: "名稱", def: "預設", addBtn: "新增", accounts: "帳號", signIn: "登入" },
  ja: { add: "＋ アカウントを追加", name: "名前", def: "デフォルト", addBtn: "追加", accounts: "アカウント", signIn: "サインイン" },
  de: { add: "＋ Konto hinzufügen", name: "Name", def: "Standard", addBtn: "Hinzufügen", accounts: "Konten", signIn: "Anmelden" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a server's accounts are switched in its editor", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
      assert.deepEqual(errors, []);
    });
    for (const lang of Object.keys(words)) {
      await t.test(lang, async () => {
        const w = words[lang];
        const calls = [];
        process.env.LANG_UNDER_TEST = lang;
        const ctx = await browser.newContext({ viewport: { width: 980, height: 800 } });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(calls));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const row = (name) => page.locator("#view-library .lib-row", { has: page.locator(".name", { hasText: new RegExp("^" + name) }) });
        const close = async () => { await page.keyboard.press("Escape"); await page.locator("#modal .lib-editor").waitFor({ state: "detached" }); };
        await row("web").waitFor();

        // one set of headers: no list, only the quiet button
        await row("web").click();
        const accts = page.locator("#modal .lib-hdr-accts");
        await accts.waitFor();
        assert.equal(await accts.locator(".lib-acct").count(), 0);
        await accts.getByRole("button", { name: w.add, exact: true }).click();
        await accts.locator(".lib-acct-form > input").fill("work");
        await accts.locator(".lib-pair input").nth(1).fill("Bearer sk-work");
        await accts.getByRole("button", { name: w.addBtn, exact: true }).click();
        await accts.locator(".lib-acct.on", { hasText: "work" }).waitFor();
        assert.equal(calls.at(-1), 'web|header|add||work|{"Authorization":"Bearer sk-work"}');
        assert.deepEqual(await accts.locator(".lib-acct-name").allTextContents(), [w.def, "work"]);

        // a click on Default puts the server's own headers back in use
        await accts.locator(".lib-acct-pick", { hasText: w.def }).click();
        await accts.locator(".lib-acct.on", { hasText: w.def }).waitFor();
        assert.equal(calls.at(-1), "web|header|use|||{}");
        assert.equal(await accts.locator('[aria-checked="true"]').count(), 1);
        await accts.locator(".lib-acct", { hasText: "work" }).locator(".lib-x").click();
        await accts.locator(".lib-acct").first().waitFor({ state: "detached" });
        assert.equal(calls.at(-1), "web|header|remove|1||{}");
        await close();

        // several: the one in use is marked; another is put in use in place
        await row("two").click();
        const two = page.locator("#modal .lib-hdr-accts");
        await two.locator(".lib-acct.on", { hasText: "work" }).waitFor();
        await two.locator(".lib-acct-pick", { hasText: "personal" }).click();
        await two.locator(".lib-acct.on", { hasText: "personal" }).waitFor();
        assert.equal(calls.at(-1), "two|header|use|1||{}");
        assert.equal(await two.locator(".lib-acct.on").count(), 1);
        await close();

        // an OAuth server with one sign-in: its line as before, and the quiet button
        await row("one").click();
        const one = page.locator("#modal .lib-signin");
        await one.waitFor();
        assert.equal(await one.locator(".lib-accts").count(), 0);
        await one.getByRole("button", { name: w.add, exact: true }).click();
        await one.locator(".lib-acct-new input").fill("team");
        await one.getByRole("button", { name: w.signIn, exact: true }).click();
        await one.locator(".lib-signin-err").waitFor();
        assert.equal(calls.at(-1), 'signin {"name":"one","add":true,"label":"team"}');
        await close();

        // several sign-ins: a list, the one in use marked
        await row("many").click();
        const many = page.locator("#modal .lib-signin");
        await many.locator(".lib-acct.on", { hasText: "me" }).waitFor();
        await many.locator(".lib-acct-pick", { hasText: "team" }).click();
        await many.locator(".lib-acct.on", { hasText: "team" }).waitFor();
        assert.equal(calls.at(-1), "many|signin|use|2||{}");
        assert.equal(await many.locator(".lib-acct.on").count(), 1);
        // the page doesn't overflow sideways at a narrow width
        await page.setViewportSize({ width: 420, height: 800 });
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
        await ctx.close();
      });
    }
  });
}
