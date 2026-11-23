// Run with Node's test runner and Playwright on the module path; see README.md.
// jinpwng (#7244): a plugin Codex installed from a marketplace and a package
// Pi installed list their skills, MCP servers and hooks in the Library's own
// tabs, once, with where they came from. A plugin Claude Code installed
// itself (#7266) too, its subagents on the Subagents tab. Read only: nothing there to switch,
// copy or remove, and no row twice. In every language, both engines, at a
// phone's width too. No backend: the API is faked here, with the shapes
// the Go reader returns for Codex's real cache layout (chrome's inline
// hooks, codex-app-tools' .mcp.json) and a Pi package. A Codex plugin a
// project's .codex/config.toml turns on or off says so per project, by
// whether Codex trusts the project (#7244 follow-up).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/me";
const lib = () => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [{ id: "codex", name: "Codex", icon: "", skills: `${HOME}/.codex/skills`, mcp: true, hooks: true }, { id: "claude", name: "Claude Code", icon: "", skills: `${HOME}/.claude/skills`, mcp: true, hooks: true }],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], servers: [], hooks: [],
});
const native = [
  {
    agent: "codex", agentName: "Codex", name: "chrome", version: "26.1029.61003", source: "openai-bundled", scope: "user", dir: `${HOME}/.codex/plugins/cache/openai-bundled/chrome/26.1029.61003`, enabled: false, loaded: "unknown",
    inProjects: [
      { dir: `${HOME}/work/app`, set: true, trusted: true, effective: true },
      { dir: `${HOME}/work/lab`, set: true, trusted: false, effective: false },
    ],
    parts: [
      { kind: "skill", name: "chrome", detail: "Drive Chrome" },
      { kind: "hook", name: "Stop", detail: "mcp_tool node_repl/turn_ended" },
    ],
  },
  {
    agent: "codex", agentName: "Codex", name: "codex-app-tools", version: "26.1029.61003", source: "openai-bundled", scope: "user", dir: `${HOME}/.codex/plugins/cache/openai-bundled/codex-app-tools/26.1029.61003`, loaded: "unknown",
    parts: [{ kind: "mcp", name: "codex_app", detail: "stdio" }],
  },
  {
    agent: "claude", agentName: "Claude Code", name: "fixture@fixmarket", version: "0.1.0", source: "fixmarket", scope: "project", project: `${HOME}/work/app`, dir: `${HOME}/.claude/plugins/cache/fixmarket/fixture/0.1.0`, enabled: true, loaded: "unknown",
    parts: [
      { kind: "skill", name: "fix-skill", detail: "A fixture skill" },
      { kind: "hook", name: "SessionStart", detail: "/usr/bin/true" },
      { kind: "subagent", name: "fix-agent", detail: "A fixture agent" },
    ],
  },
  {
    agent: "pi", agentName: "Pi", name: "proj-kit", version: "2.0.0", source: "npm:proj-kit", scope: "project", project: `${HOME}/work/app`, dir: `${HOME}/work/app/.pi/npm/node_modules/proj-kit`, loaded: "unknown",
    parts: [{ kind: "skill", name: "review", detail: "Review code" }],
  },
];

function server(lang, tab, calls) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};try{localStorage.setItem("magpie.libTab","${tab}")}catch{}` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib() });
    if (url.pathname === "/api/library/native") { calls.push(req.method()); return route.fulfill({ json: native }); }
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
  en: { onIn: "On in app", offIn: "Off in lab", head: "Installed in the agents themselves", off: "Off in Codex", project: "project scope", user: "user scope" },
  zh: { onIn: "在 app 中开启", offIn: "在 lab 中关闭", head: "Agent 自己安装的内容", off: "在 Codex 中已关闭", project: "项目范围", user: "用户范围" },
  "zh-TW": { onIn: "在 app 中開啟", offIn: "在 lab 中關閉", head: "Agent 自己安裝的內容", off: "在 Codex 中已關閉", project: "專案範圍", user: "使用者範圍" },
  ja: { onIn: "app でオン", offIn: "lab でオフ", head: "エージェント自身がインストールしたもの", off: "Codex ではオフ", project: "プロジェクトの範囲", user: "ユーザーの範囲" },
  de: { onIn: "An in app", offIn: "Aus in lab", head: "Von den Agents selbst installiert", off: "In Codex aus", project: "Projektbereich", user: "Benutzerbereich" },
};
// the tab, and the rows of its kind the fake lists
const tabs = {
  skills: ["chrome", "fix-skill", "review"],
  mcp: ["codex_app"],
  hooks: ["Stop", "SessionStart"],
  subagents: ["fix-agent"],
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": what Codex plugins and Pi packages declare is listed once, read only", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of Object.keys(words)) for (const width of [980, 380]) for (const tab of Object.keys(tabs)) {
      await t.test(`${tab} ${lang} at ${width}px`, async () => {
        const w = words[lang], calls = [];
        const ctx = await browser.newContext({ viewport: { width, height: 900 } });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, tab, calls));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        const rows = v.locator(".lib-nativebox .lib-native");
        await rows.first().waitFor();
        assert.deepEqual(await rows.locator(".name").allTextContents(), tabs[tab]);
        assert.equal(await v.locator(".lib-nativebox .label").textContent(), w.head);
        // each row says where it came from, once, with its scope
        const first = (await rows.first().textContent());
        if (tab === "subagents") {
          // Claude Code's, with its plugin id and scope, and its description
          assert(first.includes("Claude Code · fixture@fixmarket 0.1.0 · " + w.project), first);
          assert(first.includes("A fixture agent"), first);
          assert.equal(await rows.first().locator("svg").count(), 1);
        } else assert(first.includes("Codex · " + (tab === "mcp" ? "codex-app-tools" : "chrome") + " 26.1029.61003"), first);
        if (tab === "hooks") assert((await rows.nth(1).textContent()).includes("Claude Code · fixture@fixmarket 0.1.0"));
        if (tab === "skills") {
          assert(first.includes(w.user), first);
          assert((await rows.nth(1).textContent()).includes("Claude Code · fixture@fixmarket 0.1.0 · " + w.project), await rows.nth(1).textContent());
          assert((await rows.nth(2).textContent()).includes(w.project), await rows.nth(2).textContent());
          assert((await rows.nth(2).textContent()).includes("proj-kit 2.0.0"));
          // chrome's own settings turn it off
          assert.equal(await rows.first().locator(".lib-tag").first().textContent(), w.off);
          // and what each project that sets it makes of it: on where the
          // trusted project turns it on, off where Codex doesn't trust
          // the project that does
          const proj = rows.first().locator(".lib-nativeproj");
          assert.deepEqual(await proj.allTextContents(), [w.onIn, w.offIn]);
          assert.deepEqual(await proj.evaluateAll((xs) => xs.map((x) => x.dataset.project)), [`${HOME}/work/app`, `${HOME}/work/lab`]);
          const titles = await proj.evaluateAll((xs) => xs.map((x) => x.title));
          assert(titles.every((x) => x.includes(".codex/config.toml")), titles.join(" | "));
          assert.notEqual(titles[0], titles[1]);
        }
        // read only: nothing to switch, copy or remove
        assert.equal(await rows.getByRole("switch").count(), 0);
        assert.equal(await rows.locator("button.danger, .lib-icon").count(), 0);
        assert.deepEqual(calls, ["GET"]);
        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert(over <= 0, `the page scrolls sideways by ${over}px`);
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
