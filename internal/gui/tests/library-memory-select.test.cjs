// Run with Node's test runner and Playwright on the module path; see README.md.
// The Library's Memory tab picks several memories and acts on them at once
// (RealZeroP27 on X): Select puts a box on each row, a click picks one and
// a Shift-click every one from the last picked, ↑ ↓ move between the boxes
// and ⌘A / Ctrl+A picks them all, and the bar under the list keeps the ones
// to review, archives the ones in use, and deletes on a second click, each
// in one request. Escape ends it. Every language, wide and narrow,
// Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/mem";
const library = {
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [{ id: "codex", name: "Codex", icon: "", instructions: `${HOME}/.codex/AGENTS.md` }],
  instructions: { shared: "", sets: [{ id: "default", name: "", active: true, text: "" }], agents: [] },
  foundServers: [], projects: [], foundSkills: [], skills: [], servers: [],
};
const at = (d) => `2026-11-0${d}T10:00:00Z`;
function memories() {
  const guessed = ["Deploys go out on Fridays", "Prefers pnpm", "Answers in Chinese", "Runs tests on mote", "Ships on Tuesdays"];
  return {
    memories: [
      { id: "a1", text: "Use pnpm, never npm, in this repo", kind: "rule", scope: "global", origin: "user", status: "active", created: at(1), updated: at(2) },
      { id: "a2", text: "Never push to main", kind: "rule", scope: "global", origin: "user", status: "active", created: at(1), updated: at(1) },
      // newest first in the list: c1 … c5
      ...guessed.map((text, i) => ({ id: "c" + (i + 1), text, kind: "fact", scope: "global", origin: "inferred", status: "candidate", sources: [{ agent: "codex", at: at(3) }], created: at(3), updated: at(9 - i) })),
    ],
    projects: {}, digest: true, agents: [],
  };
}

function server(lang, state, sent) {
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: library });
    if (url.pathname.startsWith("/api/memory/distill")) return route.fulfill({ json: { on: false, model: "", auto: "", running: false, models: [] } });
    if (url.pathname.startsWith("/api/memory")) {
      if (route.request().method() === "POST") {
        const body = route.request().postDataJSON();
        sent.push({ path: url.pathname, body });
        if (url.pathname === "/api/memory/remove") state.memories = state.memories.filter((m) => !body.ids.includes(m.id));
        else if (url.pathname === "/api/memory/status") for (const m of state.memories) if (body.ids.includes(m.id)) m.status = body.status;
      }
      return route.fulfill({ json: state });
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

const view = "#view-library";
const review = { en: "To review", zh: "待确认", "zh-TW": "待確認", ja: "確認待ち", de: "Zu prüfen" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Memory tab keeps, archives and deletes the memories picked at once", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of Object.keys(review)) for (const width of [980, 420]) {
      await t.test(`${lang} ${width}`, async () => {
        const ctx = await browser.newContext({ viewport: { width, height: 860 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "memory"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        const sent = [];
        const state = memories();
        await page.route("**/*", server(lang, state, sent));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const rows = page.locator(`${view} .lib-mems .lib-mem`);
        await rows.first().waitFor();
        const count = (n) => page.waitForFunction((n) => document.querySelectorAll("#view-library .lib-mems .lib-mem").length === n, n);
        const picked = () => page.evaluate(() => [...document.querySelectorAll("#view-library .lib-mems .lib-mem.picked")].map((r) => r.dataset.id));
        const fits = async () => {
          assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), "the page scrolls sideways");
          const body = await page.locator(`${view} .lib-body`).boundingBox();
          for (const b of await page.locator(`${view} .lib-membar > *, ${view} .lib-mempickbar > *`).all()) {
            const r = await b.boundingBox();
            if (r) assert.ok(r.x + r.width <= body.x + body.width + 1, "a control is cut off");
          }
        };

        await page.locator(`${view} .lib-memstatus .opt`, { hasText: review[lang] }).click();
        await count(5);
        // no boxes until Select
        assert.equal(await page.locator(`${view} .lib-mems .lib-pickbox`).count(), 0);
        await page.locator(`${view} .lib-memselect`).click();
        await page.locator(`${view} .lib-mempickbar`).waitFor();
        assert.equal(await page.locator(`${view} .lib-mems .lib-pickbox`).count(), 5);
        assert.equal(await page.locator(`${view} select`).count(), 0, "a native select");
        assert.equal(await page.locator(`${view} .lib-mempickbar .lib-memto`).count(), 0, "an action with nothing picked");
        await fits();

        // a click picks one and opens nothing; a Shift-click every one from it
        await rows.nth(0).click();
        assert.equal(await page.locator(`${view} .lib-memdetail`).count(), 0, "a picked row opened");
        assert.deepEqual(await picked(), ["c1"]);
        await rows.nth(3).click({ modifiers: ["Shift"] });
        assert.deepEqual(await picked(), ["c1", "c2", "c3", "c4"]);
        assert.match(await page.locator(`${view} .lib-mempickbar .lib-pickn`).textContent(), /4/);
        // a Shift-click selects no text
        assert.equal(await page.evaluate(() => String(getSelection())), "");
        // the keys: ↓ to the last box, Space picks it, ⌘A / Ctrl+A unpicks every one and picks them again
        await page.keyboard.press("ArrowDown");
        assert.equal(await page.evaluate(() => document.activeElement.closest(".lib-mem")?.dataset.id), "c5");
        await page.keyboard.press("Space");
        assert.deepEqual(await picked(), ["c1", "c2", "c3", "c4", "c5"]);
        assert.ok(await page.locator(`${view} .lib-mempickall`).isChecked());
        await page.keyboard.press("ControlOrMeta+a");
        assert.deepEqual(await picked(), []);
        await page.keyboard.press("ControlOrMeta+a");
        assert.equal((await picked()).length, 5);
        // Shift+↑ unpicks as it goes back from an unpicked one
        await rows.nth(4).click();
        assert.equal((await picked()).length, 4);
        await page.keyboard.press("Shift+ArrowUp");
        assert.deepEqual(await picked(), ["c1", "c2", "c3"]);
        await fits();

        // kept, the three in one request
        await page.locator(`${view} .lib-mempickbar .lib-memto`).click();
        await count(2);
        assert.deepEqual(sent.filter((x) => x.path === "/api/memory/status").map((x) => x.body), [{ ids: ["c1", "c2", "c3"], status: "active" }]);
        assert.equal(await page.locator(`${view} .lib-mems .lib-pickbox`).count(), 2, "Select ended with memories left");
        assert.deepEqual(await picked(), []);

        // deleted on a second click
        await rows.nth(0).click();
        await rows.nth(1).click({ modifiers: ["Shift"] });
        await page.locator(`${view} .lib-mempickbar .lib-memdel`).click();
        assert.equal(sent.filter((x) => x.path === "/api/memory/remove").length, 0, "deleted on the first click");
        assert.match(await page.locator(`${view} .lib-mempickbar .lib-memdel`).textContent(), /2/);
        await fits();
        await page.locator(`${view} .lib-mempickbar .lib-memdel`).click();
        await count(0);
        assert.deepEqual(sent.findLast((x) => x.path === "/api/memory/remove").body, { ids: ["c4", "c5"] });
        // an empty list ends Select
        assert.equal(await page.locator(`${view} .lib-mempickbar`).count(), 0);

        // in use: archived at once; Escape ends Select
        await page.locator(`${view} .lib-memstatus .opt`).first().click();
        await count(5);
        await page.locator(`${view} .lib-memselect`).click();
        await page.locator(`${view} .lib-mempickbar`).waitFor();
        await page.locator(`${view} .lib-mems .lib-mem[data-id="a1"]`).click();
        await page.locator(`${view} .lib-mems .lib-mem[data-id="a2"]`).click();
        await page.locator(`${view} .lib-mempickbar .lib-memto`).click();
        await count(3);
        assert.deepEqual(sent.findLast((x) => x.path === "/api/memory/status").body, { ids: ["a1", "a2"], status: "archived" });
        await rows.first().click();
        await page.keyboard.press("Escape");
        await page.locator(`${view} .lib-mempickbar`).waitFor({ state: "detached" });
        assert.equal(await page.locator(`${view} .lib-mems .lib-pickbox`).count(), 0);
        // and a row opens again
        await rows.first().click();
        await page.locator(`${view} .lib-memdetail`).waitFor();
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
