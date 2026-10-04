// Run with Node's test runner and Playwright on the module path; see README.md.
// #741: on Windows, Library → RTK → Upgrade showed only a red, cut-short
// "winget upgrade --id rtk-ai.rtk --exact --silent ..." in the status pill —
// no result, no exit code, no version. While it runs, the card now says what
// it runs; a failure stays on the card with the tool's exit code and what it
// said (the backend's message, faked here), and the card is read again for
// the version it left; a success says the new version, which the card
// shows. In English and Chinese. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "C:/Users/aimer";
const UPGRADE = "winget upgrade --id rtk-ai.rtk --exact --silent --accept-package-agreements --accept-source-agreements --disable-interactivity";
const WHY = "winget failed (exit code 0x8A150101): RTK is in use — close the agents running it and try again";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const lib = () => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("codex", "Codex")],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], servers: [],
});
const rtkView = (version) => ({
  path: `${HOME}/AppData/Local/Microsoft/WinGet/Links/rtk.exe`, version, latest: "0.51.0", upgrade: UPGRADE, url: "https://www.rtk-ai.app",
  agents: [{ id: "codex", name: "Codex", icon: "", on: true }],
});

function server(lang, state) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib() });
    if (url.pathname === "/api/library/rtk") { state.reads++; return route.fulfill({ json: rtkView(state.version) }); }
    if (url.pathname === "/api/library/rtk/upgrade") {
      await state.gate;
      if (state.fail) return route.fulfill({ status: 400, json: { error: WHY } });
      state.version = "0.51.0";
      return route.fulfill({ json: rtkView(state.version) });
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
  en: { button: "Upgrade", running: `Running ${UPGRADE} — this can take a few minutes.`, failed: `The upgrade failed: ${WHY}`, pill: "RTK's upgrade failed — the RTK card says why", done: "RTK is now 0.51.0" },
  zh: { button: "升级", running: `正在运行 ${UPGRADE}，可能需要几分钟。`, failed: `升级失败：${WHY}`, pill: "RTK 升级失败，原因见 RTK 卡片", done: "RTK 已升级到 0.51.0" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an RTK upgrade says how it went", async (t) => {
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
        let open;
        const state = { version: "0.43.0", fail: true, reads: 0, gate: new Promise((r) => { open = r; }) };
        const ctx = await browser.newContext({ viewport: { width: 980, height: 800 } });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "rtk"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, state));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        const button = v.locator(".lib-cardhead button");
        await button.waitFor();
        assert.equal(await button.textContent(), w.button);

        // while it runs: what it runs, on the card
        await button.click();
        const running = v.locator(".lib-card p", { hasText: UPGRADE });
        await running.waitFor();
        assert.equal(await running.textContent(), w.running);

        // it fails: why, on the card, kept past the pill; the card read again
        const reads = state.reads;
        open();
        const err = v.locator(".lib-rtk-err");
        await err.waitFor();
        assert.equal(await err.textContent(), w.failed);
        assert.equal(await page.locator("#status").textContent(), w.pill);
        for (let i = 0; i < 50 && state.reads === reads; i++) await page.waitForTimeout(20);
        assert(state.reads > reads, "the card wasn't read again after the failure");
        assert.equal(await running.count(), 0);

        // again, and it works: the error goes, the new version is shown
        state.fail = false;
        await v.locator(".lib-cardhead button").click();
        await page.waitForFunction((want) => document.querySelector("#status").textContent === want, w.done).catch(() => {});
        assert.equal(await page.locator("#status").textContent(), w.done);
        assert.equal(await err.count(), 0);
        assert.equal(await v.locator(".lib-cardhead .note").first().textContent(), "v0.51.0");
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
