// Run with Node's test runner and Playwright on the module path; see README.md.
// #1862 (WardLu): Library › Skills has Collapse all / Expand all in its
// heading's ⋯ menu (library-bar-more), for a library of many groups. Collapse all folds every group the
// list shows and keeps it, as a click on each heading would; the button
// then says Expand all, which opens them again. While filtering it folds
// or opens only the groups the filter shows, till the filter changes, and
// leaves the kept folds alone. It changes nothing an agent has, so it
// sends nothing, and a click on it doesn't move the page. With one group
// (A→Z, or a filter that one group matches) there is nothing to fold all,
// and it isn't in the menu. Five languages, narrow and wide; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/me";
const agent = (id, name, icon) => ({ id, name, icon, skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const gh = (repo, name) => ({ name, kind: "github", source: `https://github.com/${repo}/tree/main/skills/${name}`, description: "d", agents: ["claude"] });
const local = (name) => ({ name, kind: "local", description: "d", agents: [] });

function server(lang, state, posts) {
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (req.method() === "POST") posts.push(url.pathname);
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(state.lib);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// the button's words, as i18n.js has them in each language
const L = {
  en: ["Collapse all", "Expand all"],
  zh: ["全部收起", "全部展开"],
  "zh-TW": ["全部收起", "全部展開"],
  ja: ["すべて折りたたむ", "すべて展開"],
  de: ["Alle einklappen", "Alle ausklappen"],
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": every skill group folded or opened at once", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) for (const width of [640, 1180]) {
      await t.test(lang + " " + width, async () => {
        const [collapse, expand] = L[lang];
        const state = {
          lib: {
            dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
            agents: [agent("claude", "Claude Code", "claudecode-color"), agent("codex", "Codex", "openai")],
            instructions: { agents: [], sets: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], problems: [],
            // nine, so the filter shows: two repositories and the user's own
            skills: [
              gh("acme/kit", "strawberry"), gh("acme/kit", "mango"), gh("acme/kit", "kiwi"),
              gh("solo/one", "blueberry"), gh("solo/one", "plum"), gh("solo/one", "pear"),
              local("fig"), local("date"), local("lime"),
            ],
          },
        };
        const posts = [];
        const ctx = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, state, posts));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const heads = page.locator("#view-library .lib-grouphead");
        await heads.first().waitFor();
        // the item in the heading's ⋯ menu, opened for each look and click
        const more = page.locator("#view-library .lib-skillshead .lib-barmore");
        const menu = page.locator(".row-menu");
        const shut = async () => { if (await menu.count()) { await page.keyboard.press("Escape"); await menu.waitFor({ state: "detached" }); } };
        const item = async () => { await shut(); await more.click(); await menu.waitFor(); return menu.locator(".rm-item.lib-foldall"); };
        const btn = {
          isVisible: async () => { const n = await (await item()).count(); await shut(); return n > 0; },
          textContent: async () => { const v = await (await item()).textContent(); await shut(); return v; },
          click: async () => (await item()).click(),
          boundingBox: async () => { const b = await (await item()).boundingBox(); await shut(); return b; },
        };
        const openOf = () => heads.evaluateAll((hs) => hs.map((h) => [h.querySelector(".name").textContent, h.classList.contains("open")]));
        const rows = () => page.locator("#view-library .lib-row.lib-skill").count();
        // how far anything on the page is scrolled down
        const scrolled = () => page.evaluate(() => Math.max(0, ...[document.scrollingElement, ...document.querySelectorAll("*")].map((e) => e?.scrollTop || 0)));
        const kept = () => page.evaluate(() => JSON.parse(localStorage.getItem("magpie.libSkillFolds") || "{}"));

        // three groups, open as few as these: the button offers to fold them
        assert.equal(await heads.count(), 3);
        assert.ok((await openOf()).every(([, o]) => o));
        assert.equal(await btn.isVisible(), true);
        assert.equal((await btn.textContent()).trim(), collapse);
        const box = await btn.boundingBox();
        assert.ok(box.x >= 0 && box.x + box.width <= width, "the item is inside the window");

        // Collapse all: every group folded, kept, and the button turns over
        assert.equal(await scrolled(), 0);
        await btn.click();
        await page.waitForFunction(() => [...document.querySelectorAll("#view-library .lib-grouphead")].every((h) => !h.classList.contains("open")));
        assert.equal(await rows(), 0);
        assert.equal((await btn.textContent()).trim(), expand);
        const k = await kept();
        assert.equal(Object.keys(k).length, 3);
        assert.ok(Object.values(k).every((v) => v === true));
        assert.equal(await scrolled(), 0, "a click doesn't move the page");

        // one opened by hand: there is something to fold again, so it says
        // Collapse all
        await heads.first().click();
        await page.waitForFunction(() => document.querySelector("#view-library .lib-grouphead")?.classList.contains("open"));
        assert.equal((await btn.textContent()).trim(), collapse);
        await btn.click();
        await page.waitForFunction(() => [...document.querySelectorAll("#view-library .lib-grouphead")].every((h) => !h.classList.contains("open")));

        // Expand all opens every one, and keeps that
        await btn.click();
        await page.waitForFunction(() => [...document.querySelectorAll("#view-library .lib-grouphead")].every((h) => h.classList.contains("open")));
        assert.equal(await rows(), 9);
        assert.equal((await btn.textContent()).trim(), collapse);
        assert.ok(Object.values(await kept()).every((v) => v === false));

        // filtering: "berry" shows two groups, and the button folds those
        // two till the filter changes, leaving the kept folds as they were
        const before = await kept();
        const f = page.locator("#view-library .lib-skillq");
        await f.fill("berry");
        await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-grouphead").length === 2);
        assert.equal((await btn.textContent()).trim(), collapse);
        await btn.click();
        await page.waitForFunction(() => [...document.querySelectorAll("#view-library .lib-grouphead")].every((h) => !h.classList.contains("open")));
        assert.equal(await heads.count(), 2);
        assert.equal((await btn.textContent()).trim(), expand);
        assert.deepEqual(await kept(), before, "a fold made while filtering isn't kept");
        // a filter one group matches: nothing to fold all
        await f.fill("mango");
        await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-grouphead").length === 1);
        assert.equal(await btn.isVisible(), false);
        // the filter gone: the groups are as kept, all open
        await f.press("Escape");
        await page.waitForFunction(() => [...document.querySelectorAll("#view-library .lib-grouphead")].length === 3);
        assert.ok((await openOf()).every(([, o]) => o));
        assert.equal(await btn.isVisible(), true);

        // A→Z is one list, with no group to fold
        await page.locator('#view-library .sortby[data-sort="libSkills"] .opt').nth(1).click();
        await page.waitForFunction(() => !document.querySelector("#view-library .lib-grouphead"));
        assert.equal(await btn.isVisible(), false);
        await page.locator('#view-library .sortby[data-sort="libSkills"] .opt').nth(0).click();
        await heads.first().waitFor();
        assert.equal(await btn.isVisible(), true);

        // only how the list looks changed: nothing was sent
        assert.deepEqual(posts, []);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "the page scrolls sideways");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
