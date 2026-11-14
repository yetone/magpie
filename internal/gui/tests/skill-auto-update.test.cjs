// Run with Node's test runner and Playwright on the module path; see README.md.
// 0xYaCo on X: "skill 能自动化升级吗？". The Skills tab has a switch, Update
// by themselves, under the list while a skill came from GitHub or git: it
// posts skills/auto-update and shows the state the library answers. A
// library of the user's own folders only has nothing to update and no
// switch. In every language, in Chromium and WebKit. No backend: the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/yaco";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills` });
const gh = (name) => ({ name, description: name + " skill", kind: "github", source: `https://github.com/anthropics/skills/tree/HEAD/skills/${name}`, agents: ["claude"] });
const mine = (name) => ({ name, description: name + " of mine", kind: "folder", source: `${HOME}/skills/${name}`, agents: ["claude"] });
const lib = (st) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code")],
  instructions: { agents: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], newSkills: [],
  checkDue: false, autoUpdateSkills: st.on,
  skills: st.github ? [gh("pdf"), gh("docx"), mine("notes")] : [mine("notes"), mine("todo")],
});

function server(lang, posts, st) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib(st) });
    if (url.pathname.startsWith("/api/library/skills/")) {
      const what = url.pathname.split("/").pop(), body = req.postDataJSON();
      posts.push({ what, body });
      if (what === "auto-update") st.on = body.on;
      return route.fulfill({ json: { ...lib(st), result: { changed: [] } } });
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

const words = {
  en: { name: "Update by themselves", on: "Skills update by themselves now", off: "Skills wait for you to update them now" },
  zh: { name: "自动更新", on: "技能现在会自动更新", off: "技能现在等你手动更新" },
  "zh-TW": { name: "自動更新", on: "技能現在會自動更新", off: "技能現在等你手動更新" },
  ja: { name: "自動で更新", on: "スキルは自動で更新されるようになりました", off: "スキルは手動で更新するようになりました" },
  de: { name: "Selbst aktualisieren", on: "Skills aktualisieren sich jetzt selbst", off: "Skills warten jetzt, bis du sie aktualisierst" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Skills tab's Update by themselves switch", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of Object.keys(words)) {
      await t.test(lang, async () => {
        const w = words[lang], posts = [], st = { on: false, github: true };
        const ctx = await browser.newContext({ viewport: { width: 400, height: 900 } });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posts, st));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        const row = v.locator(".lib-skillauto .lib-row");
        await row.waitFor();
        assert.equal((await row.locator(".name").textContent()).trim(), w.name);
        const sw = row.locator('[role="switch"]');
        assert.equal(await sw.getAttribute("aria-checked"), "false");
        assert.equal(await sw.getAttribute("aria-label"), w.name);
        // the row fits a narrow window
        const box = await row.boundingBox();
        assert(box.x >= 0 && box.x + box.width <= 400, `row out of the window: ${JSON.stringify(box)}`);

        await sw.click();
        await page.waitForFunction((x) => document.querySelector("#status").textContent.trim() === x, w.on);
        await page.waitForFunction(() => document.querySelector('.lib-skillauto [role="switch"]')?.getAttribute("aria-checked") === "true");
        await v.locator('.lib-skillauto [role="switch"]').click();
        await page.waitForFunction((x) => document.querySelector("#status").textContent.trim() === x, w.off);
        assert.deepEqual(posts, [{ what: "auto-update", body: { on: true } }, { what: "auto-update", body: { on: false } }]);

        // nothing from GitHub or git: nothing to update, no switch
        st.github = false;
        await page.reload();
        await page.locator('button[data-view="library"]').click();
        await v.locator(".lib-skill").first().waitFor();
        assert.equal(await v.locator(".lib-skillauto").count(), 0);
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
