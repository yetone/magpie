// Run with Node's test runner and Playwright on the module path; see README.md.
// #1348 (ttmouse): with dozens of skills in the library the market
// (Discover) sat many screens down, under the list. The Skills and MCP
// tabs' strip at the top, which stays as the page scrolls, has a Discover
// button that takes the reader to it, its search box ready to type in. The
// other tabs don't show it. It fits a narrow window and browser, has no left border,
// and only its click moves the page. In English and Chinese. No backend:
// the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/tester";
const agent = (id, name, icon) => ({ id, name, icon, skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const skills = Array.from({ length: 60 }, (_, i) => ({ name: `skill-${String(i).padStart(2, "0")}`, kind: "folder", description: `Skill number ${i}`, agents: ["claude"], source: `${HOME}/skills/skill-${i}` }));
const servers = Array.from({ length: 40 }, (_, i) => ({ name: `server-${String(i).padStart(2, "0")}`, transport: "stdio", command: "npx", args: ["-y", `server-${i}`], agents: ["claude"] }));

function serve(lang, web) {
  const view = {
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents: [agent("claude", "Claude Code", "claudecode-color")],
    instructions: { agents: [], sets: [] }, servers, foundServers: [], projects: [], foundSkills: [], skills, problems: [],
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/library") return json(view);
    if (url.pathname === "/api/library/market/servers") return json({ items: [{ id: "fetch", name: "fetch", title: "Fetch", description: "Read the web.", transport: "stdio", runs: "uvx", featured: true }] });
    if (url.pathname === "/api/library/market/skills") return json({ items: [{ id: "acme/skills/pdf", name: "pdf", source: "acme/skills", skillId: "pdf", installs: 1200, description: "PDFs" }] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

const words = { en: { discover: "Discover", tabs: ["Instructions", "MCP servers", "Skills"] }, zh: { discover: "发现", tabs: ["指令", "MCP 服务器", "技能"] } };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const [web, width] of [[true, 1000], [true, 560], [false, 1000], [false, 560]]) {
      test(`${engine} ${lang} ${web ? "browser" : "window"} ${width}px: Discover at the top goes to the market under a long list`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const context = await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        page.on("console", (m) => { if (/scroll not asked for/.test(m.text())) errors.push(m.text()); });
        await page.route("**/*", serve(lang, web));
        await page.addInitScript(() => { localStorage.setItem("magpie.libTab", "skills"); });
        t.after(() => browser.close());
        await page.goto("http://magpie.test/?view=library");
        const view = page.locator("#view-library");
        const head = view.locator(".lib-head");
        await view.locator('.mk[data-market="skills"] .mk-card').first().waitFor();
        const go = head.getByRole("button", { name: words[lang].discover, exact: true });

        for (const kind of ["skills", "mcp"]) {
          if (kind === "mcp") {
            await view.locator(".lib-tabs .opt", { hasText: words[lang].tabs[1] }).click();
            await view.locator('.mk[data-market="mcp"] .mk-card').first().waitFor();
          }
          await t.test(`${kind}: the market is far down, and the button takes the reader there`, async () => {
            await view.evaluate((e) => { e.scrollTop = 0; });
            const mk = view.locator(`.mk[data-market="${kind}"]`);
            const below = await mk.evaluate((m) => m.getBoundingClientRect().top - m.closest(".view").getBoundingClientRect().bottom);
            assert(below > 700, `the market is only ${below}px below the view`);
            await go.waitFor();
            // the strip and its button fit the window; nothing has a left border
            const fit = await head.evaluate((h) => {
              const b = h.querySelector(".lib-discover").getBoundingClientRect(), v = h.closest(".view");
              return { right: b.right, width: v.getBoundingClientRect().right, wrapped: h.querySelector(".lib-discover span:last-child").getClientRects().length > 1 || h.querySelector(".lib-discover").scrollWidth > h.querySelector(".lib-discover").clientWidth, side: v.scrollWidth - v.clientWidth, stripe: ((c) => c.borderLeftWidth !== c.borderRightWidth || c.borderLeftColor !== c.borderRightColor)(getComputedStyle(h.querySelector(".lib-discover"))) };
            });
            assert(fit.right <= fit.width, `the button runs out of the view: ${JSON.stringify(fit)}`);
            assert.equal(fit.side, 0, "the view scrolls sideways");
            assert(!fit.wrapped, "the button's label wraps");
            assert(!fit.stripe, "the button has a left border of its own");
            await go.click();
            // its heading and search box in sight under the strip: at the
            // top, or as high as the end of the page lets it go
            const inSight = (k) => {
              const m = document.querySelector(`.mk[data-market="${k}"]`), v = m.closest(".view"), h = v.querySelector(".lib-head");
              const top = m.getBoundingClientRect().top, end = v.scrollTop + v.clientHeight >= v.scrollHeight - 1;
              return top >= h.getBoundingClientRect().bottom - 1 && (top < v.getBoundingClientRect().top + 120 || end && top < v.getBoundingClientRect().bottom - 120);
            };
            await page.waitForFunction(inSight, kind);
            assert.equal(await page.evaluate(() => document.activeElement?.dataset?.lib), "market-" + kind, "the market's search box isn't focused");
            // and stays there once the page has settled
            await page.waitForTimeout(500);
            assert(await page.evaluate(inSight, kind), "the market went out of sight again");
          });
        }

        await t.test("the Instructions tab has no Discover button", async () => {
          await view.locator(".lib-tabs .opt", { hasText: words[lang].tabs[0] }).click();
          await view.locator(".lib-body").first().waitFor();
          assert.equal(await head.locator(".lib-discover").count(), 0);
        });
        assert.deepEqual(errors, []);
      });
    }
  }
}
