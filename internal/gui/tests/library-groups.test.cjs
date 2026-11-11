// Library's tabs in the sidebar come in sections, as a source list's: what every
// agent shares, what one agent has, magpie's own. A section none of whose tabs
// shows has no name, and a list of one section has none at all.
// Chromium and WebKit, every language; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/aimer";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
let AGENTS = [agent("codex", "Codex"), agent("pi", "Pi")];
const lib = {
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: AGENTS,
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [],
  skills: Array.from({ length: 30 }, (_, i) => ({ name: `skill-${i + 1}`, description: "Grill a plan", kind: "folder", agents: ["codex"], source: `${HOME}/skills/skill-${i + 1}` })),
  servers: Array.from({ length: 30 }, (_, i) => ({ name: `server-${i + 1}`, transport: "stdio", command: "npx", args: ["fs-mcp"], agents: ["codex"] })),
};

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(lib);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { url: "http://127.0.0.1:3425", running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}


const names = {
  en: ["Every agent", "One agent", "magpie"],
  zh: ["所有智能体", "特定智能体", "magpie"],
  "zh-TW": ["所有智慧體", "特定智慧體", "magpie"],
  ja: ["すべてのエージェント", "特定のエージェント", "magpie"],
  de: ["Jeder Agent", "Ein Agent", "magpie"],
};
const heads = (page) => page.locator("#side .nav-sub:not(.leaving) .nav-sl").allTextContents();
// the list's children in order: a name is "#name", a tab its data-of
const order = (page) => page.locator("#side .nav-sub:not(.leaving)").evaluate((box) =>
  [...box.children].filter((c) => c.matches(".nav-sl, .nav-s")).map((c) => (c.matches(".nav-sl") ? "#" + c.textContent : c.dataset.of || c.textContent)));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: Library's sidebar list is in sections`, async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const open = async (lang, width) => {
      const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
      await context.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); localStorage.setItem("magpie.navLayout", "side"); } catch {} });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang));
      await page.goto("http://magpie.test/");
      await page.waitForSelector("#nav button.on");
      await page.locator('#nav button[data-view="library"]').click();
      await page.locator("#side .nav-sub:not(.leaving) .nav-s").first().waitFor();
      return { page, context, errors };
    };

    for (const [lang, [a, b, c]] of Object.entries(names)) await t.test(lang, async () => {
      AGENTS = [agent("codex", "Codex"), agent("pi", "Pi")];
      const { page, context, errors } = await open(lang, 1100);
      t.after(() => context.close());
      await page.waitForFunction(() => document.querySelector("#side .nav-sub .nav-sl"));
      const rows = await order(page);
      assert.equal(rows.length, 14, rows.join(","));
      assert.deepEqual(rows.filter((x) => x[0] === "#"), ["#" + a, "#" + b, "#" + c]);
      // the order inside each: the shared seven, then Claude's and Pi's, then magpie's
      assert.equal(rows[0], "#" + a);
      assert.equal(rows[8], "#" + b);
      assert.equal(rows[11], "#" + c);
      // a name is no tab: picking by keyboard skips it, the rows are still the tabs
      assert.equal(await page.locator("#side .nav-sub .nav-s").count(), 11);
      // the page's own strip is as it was: no names in it
      assert.equal(await page.locator("#view-library .lib-tabs .nav-sl").count(), 0);
      // the name is quiet: the muted ink, no left border, no ground
      const css = await page.locator("#side .nav-sub .nav-sl").first().evaluate((e) => { const s = getComputedStyle(e); return [s.borderLeftWidth, s.backgroundImage, s.backgroundColor]; });
      assert.deepEqual(css, ["0px", "none", "rgba(0, 0, 0, 0)"]);
      // a tab still opens its page
      await page.locator("#side .nav-sub .nav-s").nth(3).click();
      await page.waitForFunction(() => document.querySelector("#view-library .lib-tabs .on")?.dataset.seg === "skills");
      assert.deepEqual(errors, []);
    });

    await t.test("a section with nothing showing has no name", async () => {
      AGENTS = [agent("codex", "Codex"), agent("pi", "Pi")];
      const { page, context } = await open("en", 1100);
      t.after(() => context.close());
      await page.waitForFunction(() => document.querySelector("#side .nav-sub .nav-sl"));
      // Claude plugins and Pi packages hidden: "One agent" has nothing to list
      await page.evaluate(() => { for (const b of document.querySelectorAll("#view-library .lib-tabs button")) if (["plugins", "pi"].includes(b.dataset.seg)) b.hidden = true; navSubs(); });
      await page.waitForFunction(() => !document.querySelector("#side .nav-sub .nav-s[data-of]") || document.querySelectorAll("#side .nav-sub .nav-s").length === 9);
      assert.deepEqual(await heads(page), ["Every agent", "magpie"]);
      // one section left: no names at all
      await page.evaluate(() => { for (const b of document.querySelectorAll("#view-library .lib-tabs button")) if (["rtk", "bili"].includes(b.dataset.seg)) b.hidden = true; navSubs(); });
      await page.waitForFunction(() => document.querySelectorAll("#side .nav-sub .nav-s").length === 7);
      assert.deepEqual(await heads(page), []);
    });

    await t.test("narrow and dark", async () => {
      AGENTS = [agent("codex", "Codex"), agent("pi", "Pi")];
      const { page, context } = await open("zh", 420);
      t.after(() => context.close());
      await page.evaluate(() => document.documentElement.setAttribute("data-theme", "dark"));
      const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      assert.ok(over <= 0, "no sideways scroll: " + over);
    });
  });
}
