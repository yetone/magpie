// Run with Node's test runner and Playwright on the module path; see README.md.
// 0xRichardH on X: a server an agent's own plugin declares, listed under
// "Installed in the agents themselves", can be copied into the library to
// give to other agents. One action in the row's menu; one that works only
// inside its agent (Codex's codex_app, cua_repl), reads values its agent
// fills in, or has a name the library holds already, is off and says why.
// In every language, both engines, at a phone's width too. No backend: the
// API is faked here, with the shapes the Go reader returns (NativePart's
// noCopy).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/me";
const CURATED = `${HOME}/.codex/plugins/cache/openai-curated-remote/linear/1.0.0`;
const lib = (servers) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [{ id: "codex", name: "Codex", icon: "", skills: `${HOME}/.codex/skills`, mcp: true, hooks: true }, { id: "claude", name: "Claude Code", icon: "", skills: `${HOME}/.claude/skills`, mcp: true, hooks: true }],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], hooks: [],
  servers: servers.map((name) => ({ name, transport: "http", url: "https://" + name + ".example/mcp", agents: [] })),
});
const native = [
  {
    agent: "codex", agentName: "Codex", name: "codex-app-tools", version: "26.1120.50957", source: "openai-bundled", scope: "user", dir: `${HOME}/.codex/plugins/cache/openai-bundled/codex-app-tools/26.1120.50957`, loaded: "unknown",
    parts: [{ kind: "mcp", name: "codex_app", detail: "stdio", noCopy: "app" }],
  },
  {
    agent: "codex", agentName: "Codex", name: "linear", version: "1.0.0", source: "openai-curated-remote", scope: "user", dir: CURATED, loaded: "unknown",
    parts: [{ kind: "mcp", name: "linear", detail: "http" }, { kind: "mcp", name: "taken", detail: "http" }],
  },
  {
    agent: "claude", agentName: "Claude Code", name: "fixture@fixmarket", version: "0.1.0", source: "fixmarket", scope: "user", dir: `${HOME}/.claude/plugins/cache/fixmarket/fixture/0.1.0`, loaded: "unknown",
    parts: [{ kind: "mcp", name: "keeps", detail: "stdio", noCopy: "agent" }],
  },
];

function server(lang, st) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};try{localStorage.setItem("magpie.libTab","mcp")}catch{}` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib(st.servers) });
    if (url.pathname === "/api/library/native") return route.fulfill({ json: native });
    if (url.pathname === "/api/library/servers/copy-native" && req.method() === "POST") {
      const body = req.postDataJSON();
      st.posts.push(body);
      st.servers.push(body.name);
      return route.fulfill({ json: { ...lib(st.servers), result: { problems: [] } } });
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
  en: { copy: "Copy to the library", app: "It works only inside Codex's app: the app starts it with a connection of its own.", agent: "It reads values Claude Code fills in when it starts it.", taken: "The library has a server called ", done: "linear is in the library now" },
  zh: { copy: "复制到资源库", app: "它只能在 Codex 的应用里运行：应用会用自己的连接启动它。", agent: "它要读取 Claude Code 启动它时填入的值。", taken: "资源库里已有名为 ", done: "linear 已加入资源库" },
  "zh-TW": { copy: "複製到資源庫", app: "它只能在 Codex 的應用程式裡執行：應用程式會用自己的連線啟動它。", agent: "它要讀取 Claude Code 啟動它時填入的值。", taken: "資源庫裡已有名為 ", done: "linear 已加入資源庫" },
  ja: { copy: "ライブラリにコピー", app: "Codex のアプリの中でしか動きません。アプリが独自の接続を用意して起動します。", agent: "Claude Code が起動時に埋める値を読み込みます。", taken: "ライブラリにはすでに ", done: "linear をライブラリに追加しました" },
  de: { copy: "In die Bibliothek kopieren", app: "Er läuft nur in der App von Codex: Die App startet ihn mit einer eigenen Verbindung.", agent: "Er liest Werte, die Claude Code beim Start einträgt.", taken: "Die Bibliothek hat schon einen Server namens ", done: "linear ist jetzt in der Bibliothek" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a server an agent's plugin declares is copied into the library from its row's menu", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of Object.keys(words)) for (const width of [980, 380]) {
      await t.test(`${lang} at ${width}px`, async () => {
        const w = words[lang], st = { servers: ["taken"], posts: [] };
        const ctx = await browser.newContext({ viewport: { width, height: 900 } });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, st));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        const rows = v.locator(".lib-nativebox .lib-native");
        await rows.first().waitFor();
        assert.deepEqual(await rows.locator(".name").allTextContents(), ["codex_app", "linear", "taken", "keeps"]);
        const row = (name) => rows.filter({ has: page.locator(".name", { hasText: new RegExp("^" + name + "$") }) });
        const menu = page.locator(".row-menu");
        // the one item of a row's menu: its name, and why it is off
        const open = async (name) => {
          const more = row(name).locator("button.lib-nativemore");
          assert.equal(await more.count(), 1, name);
          assert.equal(await more.getAttribute("aria-haspopup"), "menu");
          // a scroll outside the menu closes it: bring the row in first, and
          // wait until nothing scrolls any more
          await more.scrollIntoViewIfNeeded();
          await page.evaluate(() => new Promise((done) => {
            const at = () => [...document.querySelectorAll("*")].filter((x) => x.scrollTop || x.scrollLeft).map((x) => x.scrollTop + "," + x.scrollLeft).join() + scrollY;
            let last = at(), still = 0;
            const step = () => { const now = at(); still = now === last ? still + 1 : 0; last = now; still >= 3 ? done() : requestAnimationFrame(step); };
            requestAnimationFrame(step);
          }));
          await more.click();
          const items = menu.getByRole("menuitem");
          await items.first().waitFor();
          assert.equal(await items.count(), 1);
          return items.first();
        };
        const off = async (name, why) => {
          const it = await open(name);
          assert(await it.isDisabled(), name + " is off");
          assert.equal(await it.locator(".rm-why").textContent(), why);
          assert.equal((await it.locator(".rm-name").textContent()).replace(why, ""), w.copy);
          await page.keyboard.press("Escape");
          await menu.waitFor({ state: "detached" });
        };
        await off("codex_app", w.app);
        await off("keeps", w.agent);
        const it = await open("taken");
        assert(await it.isDisabled());
        assert((await it.locator(".rm-why").textContent()).includes("taken"));
        assert((await it.locator(".rm-why").textContent()).startsWith(w.taken));
        await page.keyboard.press("Escape");
        await menu.waitFor({ state: "detached" });

        // linear is copied: the agent, the plugin's folder and its name go
        const copy = await open("linear");
        assert(!(await copy.isDisabled()));
        assert.equal(await copy.locator(".rm-why").count(), 0);
        await copy.click();
        await page.waitForFunction(() => !document.querySelector("#view-library[aria-busy]"));
        assert.deepEqual(st.posts, [{ agent: "codex", dir: CURATED, name: "linear" }]);
        // and then the library has it, so the copy is off
        await page.waitForFunction(() => [...document.querySelectorAll("#view-library .row .name")].filter((x) => x.textContent === "linear").length > 1);
        // it says so; the footer changes size when the line goes, and a
        // scroll that makes closes a menu, so the next one waits for it
        const said = page.locator("#status");
        assert.equal(await said.textContent(), w.done);
        await page.waitForFunction(() => !document.querySelector("#status").textContent, null, { timeout: 15000 });
        const again = await open("linear");
        assert(await again.isDisabled());
        assert((await again.locator(".rm-why").textContent()).startsWith(w.taken));
        await page.keyboard.press("Escape");
        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert(over <= 0, `the page scrolls sideways by ${over}px`);
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
