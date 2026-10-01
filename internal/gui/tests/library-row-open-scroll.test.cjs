// Run with Node's test runner and Playwright on the module path; see README.md.
// emo172 (#458): Library → Instructions with the Default set open to edit,
// scrolled down to the foot of the agents' list, an agent's row clicked open
// moved the page. The Library draws its whole page again on a click, so
// nothing the click held on the screen (the row, its neighbours, its
// parents) was left, and the page went where the browser took it: up or down
// by what opened under the row, or clamped as it shut. The row clicked stays
// where it is on the screen, opened and shut again, under a long set in a
// short window and under a shorter one in a tall window. Chromium and
// WebKit, English and Chinese; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/aimer";
const ids = ["claude", "codex", "gemini", "opencode", "pi", "kimi", "qwen", "droid", "crush", "goose", "amp", "cline"];
const named = (id) => id[0].toUpperCase() + id.slice(1);
const library = (lines) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: ids.map((id) => ({ id, name: named(id), icon: "", instructions: `${HOME}/.${id}/AGENTS.md` })),
  instructions: {
    shared: "",
    sets: [{ id: "default", name: "", active: true, text: Array.from({ length: lines }, (_, i) => `- rule ${i + 1}: keep it short`).join("\n") }],
    agents: ids.map((id) => ({ agent: id, name: named(id), icon: "", path: `${HOME}/.${id}/AGENTS.md`, on: true, extra: "" })),
  },
  foundServers: [], projects: [], foundSkills: [], skills: [], servers: [],
});

function server(lang, lib) {
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib });
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
// the set's lines, the window's height
const shapes = [[60, 640], [20, 860], [3, 860]];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an agent's instructions opened at the foot of the list leave the page where it is", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh"]) for (const [lines, height] of shapes) {
      await t.test(`${lang}, a set of ${lines} lines, ${height}px tall`, async () => {
        const ctx = await browser.newContext({ viewport: { width: 980, height }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "instructions"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, library(lines)));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        // the Default set open to edit
        await page.locator(`${view} .lib-set`).first().click();
        await page.locator(`${view} textarea[data-lib="set:default"]`).waitFor();
        await page.waitForTimeout(300);
        // down to the foot, by the wheel
        for (let i = 0; i < 30; i++) { await page.mouse.move(490, height / 2); await page.mouse.wheel(0, 200); await page.waitForTimeout(20); }
        await page.waitForTimeout(400);
        const v = page.locator(view);
        const before = await v.evaluate((e) => e.scrollTop);
        assert(before > 100, "the page didn't scroll down: " + before);
        for (const name of ["Cline", "Amp"]) {
          const row = page.locator(`${view} .lib-row:not(.lib-set)`).filter({ hasText: name }).first();
          const top = () => row.evaluate((e) => e.getBoundingClientRect().top);
          const at = await top(), b = await row.boundingBox();
          const said = async (what) => assert(Math.abs(await top() - at) <= 1, `${name}'s row moved ${what}, from ${at} to ${await top()} (the page at ${await v.evaluate((e) => e.scrollTop)}, was ${before})`);
          // on its name, as the reader clicks it, never the toggle
          await page.mouse.click(b.x + 80, b.y + b.height / 2);
          await page.locator(`${view} textarea[data-lib="extra:${name.toLowerCase()}"]`).waitFor();
          await page.waitForTimeout(500);
          await said("opening it");
          await page.mouse.click(b.x + 80, at + b.height / 2);
          await page.locator(`${view} textarea[data-lib="extra:${name.toLowerCase()}"]`).waitFor({ state: "detached" });
          await page.waitForTimeout(500);
          await said("shutting it");
        }
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
