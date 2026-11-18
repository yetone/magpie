// Run with Node's test runner and Playwright on the module path; see README.md.
// A memory written in several lines keeps them (_meouwu on X: "手动添加的
// 多行记忆被折叠成了一行，把我的换行都去掉了，应当保留原本的格式，收起展示的
// 时候显示首行+缩略符"). Folded, its row shows the first line and an
// ellipsis; open, the row shows every line as written and the editor holds
// them; one added in the editor is sent with its line breaks. A suggestion
// folds the same way. Every language, wide and narrow, Chromium and
// WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const HOME = "/Users/mem";
const library = {
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [{ id: "codex", name: "Codex", icon: "", instructions: `${HOME}/.codex/AGENTS.md` }],
  instructions: { shared: "", sets: [{ id: "default", name: "", active: true, text: "" }], agents: [] },
  foundServers: [], projects: [], foundSkills: [], skills: [], servers: [],
};
const at = (d) => `2026-11-0${d}T10:00:00Z`;
const STEPS = "Release steps:\n1. tag the commit\n2. push the tag by name\n   never --tags";
const memories = () => ({
  memories: [
    { id: "a1", text: STEPS, why: "", kind: "procedure", scope: "global", origin: "user", status: "active", created: at(1), updated: at(2) },
    { id: "a2", text: "Never push to main", kind: "rule", scope: "global", origin: "user", status: "active", created: at(1), updated: at(1) },
  ],
  suggestions: [{ key: "skill:a1", to: "skill", ids: ["a1"], text: STEPS, uses: 4 }],
  projects: {}, digest: true, agents: [],
});

function server(lang, sent) {
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: library });
    if (url.pathname === "/api/memory/semantic") return route.fulfill({ json: { on: false } });
    if (url.pathname === "/api/memory/distill") return route.fulfill({ json: { on: false, models: [] } });
    if (url.pathname === "/api/memory") {
      if (req.method() === "POST") sent.push(JSON.parse(req.postData() || "{}"));
      return route.fulfill({ json: memories() });
    }
    if (url.pathname.startsWith("/api/memory")) return route.fulfill({ json: memories() });
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
const langs = ["en", "zh", "zh-TW", "ja", "de"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a memory of several lines keeps them", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of langs) for (const width of [980, 420]) {
      await t.test(`${lang} ${width}`, async () => {
        const ctx = await browser.newContext({ viewport: { width, height: 860 } });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "memory"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        const sent = [];
        await page.route("**/*", server(lang, sent));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const row = page.locator(`${view} .lib-mems .lib-mem[data-id="a1"]`);
        await row.waitFor();
        const name = row.locator(".who .name");
        const lineH = await name.evaluate((e) => parseFloat(getComputedStyle(e).lineHeight) || parseFloat(getComputedStyle(e).fontSize) * 1.3);

        // folded: the first line and an ellipsis, on one line
        assert.equal(await name.textContent(), "Release steps: …");
        assert((await name.evaluate((e) => e.getBoundingClientRect().height)) < lineH * 1.5, "the folded row is more than a line");
        // a suggestion folds the same way
        assert.equal(await page.locator(`${view} .lib-memsugg [data-sugg="skill:a1"] .name`).textContent(), "Release steps: …");

        // open: every line as written, in the row and in the editor
        await row.evaluate((e) => e.click()); // at 420px the header can sit over it while the page settles
        const ta = page.locator(`${view} textarea[data-lib="mem:a1"]`);
        await ta.waitFor();
        assert.equal(await name.textContent(), STEPS);
        assert.equal(await name.evaluate((e) => getComputedStyle(e).whiteSpace), "pre-wrap");
        assert((await name.evaluate((e) => e.getBoundingClientRect().height)) >= lineH * 3.5, "the open row ran its lines together");
        assert.equal(await ta.inputValue(), STEPS);

        // a line added in the editor is saved with its break
        await ta.focus();
        await ta.evaluate((e) => e.setSelectionRange(e.value.length, e.value.length));
        await page.keyboard.press("Enter");
        await page.keyboard.type("3. wait for the assets");
        await page.locator(`${view} .lib-memdetail .primary`).click();
        await page.waitForFunction(() => !document.querySelector("#view-library .lib-memdetail"));
        assert.equal(sent.at(-1).text, STEPS + "\n3. wait for the assets");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
