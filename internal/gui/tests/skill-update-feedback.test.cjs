// Run with Node's test runner and Playwright on the module path; see README.md.
// zhexuanliuoutl1 on X: updating a skill from GitHub (hugohe3/ppt-master) said
// nothing while it ran. The row's Update button now spins until the fetch is
// done, and the toast says what came of it: updated, already up to date, or
// the error. In English and Chinese, in Chromium and WebKit. No backend: the
// API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/lionel";
const REPO = "anthropics/skills";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills` });
const sk = (name, extra) => ({ name, description: name + " skill", kind: "github", source: `https://github.com/${REPO}/tree/HEAD/skills/${name}`, agents: ["claude"], ...extra });
const mine = (name) => ({ name, description: name + " of mine", kind: "folder", source: `${HOME}/skills/${name}`, agents: ["claude"] });
const lib = (st) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex")],
  instructions: { agents: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], newSkills: [],
  checkDue: false,
  skills: [
    sk("pdf", { edited: true, check: st.checked ? { name: "pdf", status: "update", commit: "c2", message: "pdf: forms" } : undefined }),
    sk("docx", { check: st.checked ? { name: "docx", status: "update", commit: "c2", message: "docx: tables" } : undefined }),
    sk("xlsx", { check: st.checked ? { name: "xlsx", status: "current", commit: "c1" } : undefined }),
    ...Array.from({ length: 10 }, (_, i) => mine("note-" + i)),
  ],
});

function server(lang, posts, st, hold) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib(st) });
    if (url.pathname.startsWith("/api/library/skills/")) {
      const what = url.pathname.split("/").pop();
      posts.push({ what, body: req.postDataJSON() });
      if (what === "check") return route.fulfill({ json: { ...lib(st), result: { changed: [] } } });
      await hold.promise;
      const mode = st.modes.shift();
      if (mode === "err") return route.fulfill({ status: 500, json: { error: "couldn't reach GitHub: connection reset" } });
      return route.fulfill({ json: { ...lib(st), result: { changed: ["claude"], ...(mode === "current" ? { current: [req.postDataJSON().name] } : {}) } } });
    }
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// a mouse press where the button is: a dialog still coming in moves it, so
// the press waits for the dialog to stop, or it lands beside the button
const press = async (page, loc) => {
  await page.waitForFunction(() => !document.getAnimations().some((a) => a.playState === "running" && a.effect?.target?.closest?.("#modal")));
  const b = await loc.boundingBox();
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
};

// every scrolled box's offset, to tell a click moved none of them
const scrolls = (page) => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => (e.id || e.className) + ":" + e.scrollTop).concat("doc:" + document.scrollingElement.scrollTop).join(" "));


const words = {
  en: { updated: "docx is updated", current: "docx is already up to date", err: "couldn't reach GitHub: connection reset" },
  zh: { updated: "docx 已更新", current: "docx 已是最新", err: "couldn't reach GitHub: connection reset" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Update on a skill row spins while it runs and says what came of it", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = words[lang], posts = [], st = { modes: ["updated", "current", "err"] };
        let release;
        const hold = { promise: new Promise((r) => { release = r; }) };
        const ctx = await browser.newContext({ viewport: { width: 860, height: 900 } });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posts, st, hold));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        const docx = v.locator(".lib-skill", { has: page.locator(".name", { hasText: /^docx/ }) });
        await docx.waitFor();
        const btn = docx.locator(".lib-rowacts button.lib-icon").first();
        const status = async () => (await page.locator("#status").textContent()).trim();

        await press(page, btn);
        // held: the button turns, the row's actions stay in view
        await page.waitForFunction(() => document.querySelector(".lib-skill .lib-rowacts.busy button.lib-icon.busy svg")?.getAnimations().some((a) => a.playState === "running"));
        await page.mouse.move(0, 0);
        // (its fade-out, if it were going, is under .14s: a state, not a wait)
        await page.waitForFunction(() => getComputedStyle(document.querySelector(".lib-rowacts.busy")).opacity === "1");
        await page.waitForTimeout(400);
        assert.equal(await docx.locator(".lib-rowacts").evaluate((e) => getComputedStyle(e).opacity), "1", "actions hidden while it runs");
        release();
        await page.waitForFunction((x) => document.querySelector("#status").textContent.trim() === x, w.updated);
        assert.equal(await page.locator(".lib-icon.busy").count(), 0, "still busy after it ended");

        await press(page, btn);
        await page.waitForFunction((x) => document.querySelector("#status").textContent.trim() === x, w.current);
        await press(page, btn);
        await page.waitForFunction(() => document.querySelector("#status").classList.contains("err"));
        assert.equal(await status(), w.err);
        assert.equal(await page.locator(".lib-icon.busy").count(), 0, "still busy after the error");
        assert.deepEqual(posts.map((p) => p.what), ["update", "update", "update"]);
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
