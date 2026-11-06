// Run with Node's test runner and Playwright on the module path; see README.md.
// ryanpascal on Discord: Library › Skills' heading had a dozen buttons.
// What is used now and then waits under ⋯, the app's own row menu: Check
// for updates, Update all, Group again, Collapse all, Turn all on and off,
// By agent, Manage n in magpie and Remove all. The bar keeps the order,
// Select and the ⋯, on one line at a wide window, inside a narrow one. The
// MCP servers' heading does the same. Each item is the bar's button: its
// name and title, greyed when it was, and its click. Five languages, narrow
// and wide; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/me";
const agent = (id, name, icon) => ({ id, name, icon, skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const gh = (repo, name, extra) => ({ name, kind: "github", source: `https://github.com/${repo}/tree/main/skills/${name}`, description: "d", agents: ["claude"], ...extra });
const srv = (name) => ({ name, command: "npx", args: ["-y", name], agents: ["claude"] });

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
    if (url.pathname === "/api/library/skills/check") return json({ ...state.lib, result: {} });
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

const SKILL_MENU = ["Check for updates", "Update all", "Group again", "Collapse all", "Turn all on", "Turn all off", "By agent", "Manage {n} in magpie", "Remove all"];
const SERVER_MENU = ["Turn all on", "Turn all off", "By agent", "Remove all"];
const MOVED = ".lib-check, .lib-updallnow, .lib-regroup, .lib-foldall, .lib-everyon, .lib-everyoff, .lib-byagent, .lib-handover, .lib-everyrm";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the rarer actions of a Library list wait under ⋯", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [420, 1180]) {
      await t.test(lang + " " + width, async () => {
        const state = {
          lib: {
            dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
            agents: [agent("claude", "Claude Code", "claudecode-color"), agent("codex", "Codex", "openai")],
            instructions: { agents: [], sets: [] }, servers: [srv("alpha"), srv("beta")], foundServers: [], projects: [], problems: [],
            skills: [gh("acme/tools", "grill", { shared: true }), gh("acme/tools", "lint", { shared: true }), gh("zed/kit", "pdf"), gh("zed/kit", "tdd", { agents: ["claude", "codex"] })],
            foundSkills: [],
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
        page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
        await page.route("**/*", server(lang, state, posts));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        await page.locator("#view-library .lib-row.lib-skill").first().waitFor();
        const tr = (words, vars) => page.evaluate(([w, v]) => w.map((x) => t(x, v)), [words, vars]);

        const check = async (head, menu, vars) => {
          // the bar: the order, Select and ⋯, none of the moved buttons
          assert.equal(await head.locator(MOVED).count(), 0, "a moved button is still on the bar");
          await head.locator(".lib-select").waitFor();
          const more = head.locator("button.lib-barmore");
          assert.equal(await more.getAttribute("aria-label"), (await tr(["More"]))[0]);
          const tops = await head.locator(":scope > :not(.label):not(.grow)").evaluateAll((els) => els.filter((e) => e.offsetParent).map((e) => Math.round(e.getBoundingClientRect().top)));
          if (width >= 1180) assert.equal(new Set(tops).size, 1, "the bar is one line: " + tops);
          const hb = await head.boundingBox(), mb = await more.boundingBox();
          assert.ok(mb.x + mb.width <= hb.x + hb.width + 0.5, "⋯ is inside the heading");

          await more.click();
          const box = page.locator(".row-menu");
          await box.waitFor();
          const names = await box.locator(".rm-item .rm-name").allTextContents();
          assert.deepEqual(names, await tr(menu, vars));
          const b = await box.boundingBox();
          assert.ok(b.x >= 0 && b.y >= 0 && b.x + b.width <= width + 0.5 && b.y + b.height <= 760.5, "the menu is inside the window");
          // a second click on ⋯ closes it
          await more.click();
          await box.waitFor({ state: "detached" });
          return more;
        };

        const skills = page.locator("#view-library .lib-skillshead");
        const more = await check(skills, SKILL_MENU, { n: 2 });
        // every skill has every agent: Turn all on is greyed, as its button was
        await more.click();
        await page.locator(".row-menu .rm-item.lib-check").waitFor();
        assert.equal(await page.locator(".row-menu .rm-item.lib-everyon").isDisabled(), false);
        assert.equal(await page.locator(".row-menu .rm-item.lib-everyrm").getAttribute("title"), (await tr(["Take all {n} skills out of the library"], { n: 4 }))[0]);
        // Turn all off asks first, in the page; Cancel posts nothing
        await page.locator(".row-menu .rm-item.lib-everyoff").click();
        const sheet = page.locator("#modal .lib-editor");
        await sheet.waitFor();
        await page.keyboard.press("Escape");
        await sheet.waitFor({ state: "detached" });
        // Check for updates asks, once
        await more.click();
        await page.locator(".row-menu .rm-item.lib-check").click();
        await page.waitForFunction(() => !document.querySelector("#view-library .lib-skillshead .lib-check.busy"));
        assert.deepEqual(posts, ["/api/library/skills/check"]);

        const mcp = (await tr(["MCP servers"]))[0];
        await page.locator(":is(#view-library .lib-tabs .opt, .nav-s):visible", { hasText: mcp }).click(); // the tabs, or the side nav's at a wide window
        const servers = page.locator("#view-library .lib-serverhead");
        await servers.waitFor();
        await check(servers, SERVER_MENU, {});

        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "the page scrolls sideways");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
