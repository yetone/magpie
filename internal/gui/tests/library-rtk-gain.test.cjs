// Run with Node's test runner and Playwright on the module path; see README.md.
// #741: Library → RTK said "nothing saved yet" while rtk gain had records,
// and stayed so after the agent was restarted. The card now says what the
// count is (RTK's own, rtk gain: every agent and terminal), says why when
// rtk gain couldn't be read instead of "nothing saved", says when Codex runs
// its commands in its Windows sandbox, where what RTK saves isn't in that
// count, and reads the count again each time the RTK tab is opened. In
// English and Chinese. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "C:/Users/aimer";
const WHY = "rtk gain: Error: Failed to initialize tracking database";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const lib = () => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("codex", "Codex")],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], servers: [],
});
const views = [
  { gainErr: WHY, codexSandbox: "elevated" },
  { gain: { commands: 42, input: 9000, saved: 8000, pct: 88.9 } },
];

function server(lang, state) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib() });
    if (url.pathname === "/api/library/rtk") {
      const v = views[Math.min(state.reads++, views.length - 1)];
      return route.fulfill({ json: { path: `${HOME}/.local/bin/rtk.exe`, version: "0.51.0", url: "https://www.rtk-ai.app", agents: [{ id: "codex", name: "Codex", icon: "", on: true }], ...v } });
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
  en: {
    failed: `magpie couldn't read what RTK saved: ${WHY}`,
    scope: "RTK's own count (rtk gain): every command run through RTK on this computer, from any agent or terminal.",
    sandbox: "Codex runs its commands in its Windows sandbox, as its own sandbox account: RTK keeps what it saves there in that account's history, not yours, so it isn't counted here.",
    saved: "8.0k tokens saved over 42 commands — 89% on average",
    skills: "Skills",
  },
  zh: {
    failed: `magpie 读不到 RTK 的节省记录：${WHY}`,
    scope: "统计来自 RTK 自己的记录（rtk gain）：这台电脑上经过 RTK 的所有命令，不分 Agent 和终端。",
    sandbox: "Codex 在它的 Windows 沙箱里以沙箱专用账户运行命令：RTK 在那里节省的记录存在该账户下，不在你的账户里，所以这里统计不到。",
    saved: "42 条命令共节省 8.0k token，平均省 89%",
    skills: "技能",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": RTK's count says what it is, why it's missing, and is read again", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = words[lang], state = { reads: 0 };
        const ctx = await browser.newContext({ viewport: { width: 980, height: 800 } });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "rtk"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, state));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        const gain = v.locator(".lib-rtk-gain");
        await gain.waitFor();
        assert.equal(await gain.textContent(), w.failed + " " + w.scope);
        assert.equal(await v.locator(".lib-rtk-sandbox").textContent(), w.sandbox);

        // the RTK tab opened again: read again, the count it has now
        await v.locator(".lib-tabs button", { hasText: w.skills }).click();
        await v.locator(".lib-tabs button", { hasText: "RTK" }).click();
        await page.waitForFunction((want) => document.querySelector("#view-library .lib-rtk-gain")?.textContent.startsWith(want), w.saved).catch(() => {});
        assert.equal(await gain.textContent(), w.saved + " " + w.scope);
        assert.equal(await v.locator(".lib-rtk-sandbox").count(), 0);
        assert.equal(state.reads, 2);
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
