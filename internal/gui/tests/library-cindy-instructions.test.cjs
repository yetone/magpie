// Run with Node's test runner and Playwright on the module path; see README.md.
// T_t__biu on X (#7854): Cindy is a row of the Library's Instructions. Its
// file is Pi's own (~/.pi/agent), which Cindy's Pi reads: the row says so,
// and that its Codex and Claude Code read those agents' files; Pi's own row
// doesn't say it. An AGENTS.override.md of the user's is named as what
// Cindy reads instead. Chromium and WebKit, every language; no backend,
// the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/foobra";
const PI = `${HOME}/.pi/agent/AGENTS.md`;
const library = {
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [{ id: "pi", name: "Pi", icon: "pi", instructions: PI }, { id: "cindy", name: "Cindy", icon: "cindy", instructions: PI }],
  instructions: {
    shared: "Be brief.",
    sets: [{ id: "default", name: "", active: true, text: "Be brief." }],
    agents: [
      { agent: "pi", name: "Pi", icon: "pi", path: PI, on: false, extra: "", own: 0 },
      { agent: "cindy", name: "Cindy", icon: "cindy", path: PI, on: true, extra: "", own: 0, override: `${HOME}/.pi/agent/AGENTS.override.md` },
    ],
  },
  foundServers: [], projects: [], foundSkills: [], skills: [], servers: [],
};

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: library });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const view = "#view-library";
const note = {
  en: "This is Pi's own file, which Cindy's Pi reads. Cindy's Codex and Claude Code read Codex's and Claude Code's files: switch those on for them.",
  zh: "这是 Pi 自己的文件，Cindy 的 Pi 会读取它。Cindy 的 Codex 和 Claude Code 读取 Codex 和 Claude Code 的文件：要让它们读到，请打开那两项。",
  "zh-TW": "這是 Pi 自己的檔案，Cindy 的 Pi 會讀取它。Cindy 的 Codex 和 Claude Code 讀取 Codex 和 Claude Code 的檔案：要讓它們讀到，請開啟那兩項。",
  ja: "これは Pi 自身のファイルで、Cindy の Pi が読み込みます。Cindy の Codex と Claude Code は Codex と Claude Code のファイルを読むので、そちらをオンにしてください。",
  de: "Das ist Pis eigene Datei, die Cindys Pi liest. Cindys Codex und Claude Code lesen die Dateien von Codex und Claude Code: Schalte diese für sie ein.",
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Cindy's instructions row is Pi's file, and says where its Codex and Claude Code read theirs", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of Object.keys(note)) {
      await t.test(lang, async () => {
        const ctx = await browser.newContext({ viewport: { width: 980, height: 860 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "instructions"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        await page.locator(`${view} .lib-row`, { hasText: "Cindy" }).first().click();
        const det = page.locator(`${view} .lib-detail[data-detail="agent:cindy"]`);
        await det.locator("p.lib-aside.lib-cindy", { hasText: note[lang] }).waitFor();
        await det.locator(".lib-warn", { hasText: "~/.pi/agent/AGENTS.override.md" }).waitFor();
        await page.locator(`${view} .lib-row`).filter({ hasText: "Pi" }).filter({ hasNotText: "Cindy" }).first().click();
        await page.locator(`${view} .lib-detail[data-detail="agent:pi"]`).waitFor();
        assert.equal(await page.locator(`${view} .lib-detail[data-detail="agent:pi"] p.lib-cindy`).count(), 0, "Pi's own row speaks of Cindy");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
