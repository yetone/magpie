// Run with Node's test runner and Playwright on the module path; see README.md.
// #1844: a tab picked in the sidebar opens at its top (app.js navSubs). A page's
// tabs share its one scroll: scrolled to the foot of Settings › General and
// Allowances picked, or of Library › MCP servers and Skills picked, the new
// tab was shown from where the old one had been left, at its foot. The tab
// already open, picked again, leaves the page where it is, and another page
// and back finds each where it was left. The page's own strip is unchanged.
// Chromium and WebKit, every language; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/aimer";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const lib = {
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("codex", "Codex"), agent("pi", "Pi")],
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

const sub = (page, name) => page.locator("#side .nav-sub:not(.leaving) .nav-s", { hasText: name });
const top = (page, v) => page.locator(v).evaluate((e) => e.scrollTop);
// down to the view's foot by the wheel, as the reader goes there
async function toFoot(page, v) {
  const box = await page.locator(v).boundingBox();
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  for (let i = 0; i < 40; i++) {
    await page.mouse.wheel(0, 300);
    await page.waitForTimeout(20);
    if (await page.locator(v).evaluate((e) => e.scrollTop + e.clientHeight >= e.scrollHeight - 1)) break;
  }
  await page.waitForTimeout(300);
  const at = await top(page, v);
  assert.ok(at > 100, `${v} scrolled down: ${at}`);
  return at;
}
// the click's hold has had its frames and the page what it loads
const settle = (page) => page.waitForTimeout(500);
const room = (page, v) => page.locator(v).evaluate((e) => e.scrollHeight - e.clientHeight);

const words = {
  en: { allowances: "Allowances", mcp: "MCP servers", skills: "Skills" },
  zh: { allowances: "额度", mcp: "MCP 服务器", skills: "技能" },
  "zh-TW": { allowances: "額度", mcp: "MCP 伺服器", skills: "技能" },
  ja: { allowances: "利用枠", mcp: "MCP サーバー", skills: "スキル" },
  de: { allowances: "Kontingente", mcp: "MCP-Server", skills: "Skills" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a tab picked in the sidebar opens at its top`, async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());

    for (const [lang, w] of Object.entries(words)) await t.test(lang, async () => {
      // 360 high: Allowances still runs past it since its warm-ups and
      // check-in went to the providers' editors
      const context = await browser.newContext({ viewport: { width: 1100, height: 360 }, reducedMotion: "reduce" });
      await context.addInitScript(() => { try { localStorage.setItem("magpie.settingsTab", "general"); localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang));
      await page.goto("http://magpie.test/");
      await page.waitForSelector("#nav button.on");
      assert.ok(await page.evaluate(() => document.body.classList.contains("side-nav")), "the sidebar is shown");

      // Settings › General at its foot, then Allowances
      const set = "#view-settings";
      await page.locator("#prefs").click();
      await sub(page, w.allowances).waitFor();
      await toFoot(page, set);
      await sub(page, w.allowances).click();
      await page.waitForFunction(() => document.querySelector("#setTab-usage").classList.contains("on"));
      await settle(page);
      assert.ok(await room(page, set) > 100, "Allowances is taller than the window");
      assert.equal(await top(page, set), 0, "Allowances opens at its top");

      // Allowances at its foot, picked again: the page stays there
      const at = await toFoot(page, set);
      await sub(page, w.allowances).click();
      await settle(page);
      assert.equal(await top(page, set), at, "the tab already open leaves the page where it is");

      // Library › MCP servers at its foot, then Skills
      const libv = "#view-library";
      await page.locator('#nav button[data-view="library"]').click();
      await sub(page, w.skills).waitFor();
      await page.locator(`${libv} .lib-row, ${libv} .row`).first().waitFor();
      await toFoot(page, libv);
      await sub(page, w.skills).click();
      await page.waitForFunction((name) => document.querySelector("#view-library .lib-tabs .on")?.textContent.startsWith(name), w.skills);
      await settle(page);
      assert.ok(await room(page, libv) > 100, "Skills is taller than the window");
      assert.equal(await top(page, libv), 0, "Skills opens at its top");

      // and back to MCP servers from the foot of Skills
      await toFoot(page, libv);
      await sub(page, w.mcp).click();
      await page.waitForFunction((name) => document.querySelector("#view-library .lib-tabs .on")?.textContent.startsWith(name), w.mcp);
      await settle(page);
      assert.equal(await top(page, libv), 0, "MCP servers opens at its top");

      // another page and back: each page is where it was left, as before
      const libAt = await toFoot(page, libv);
      await page.locator("#prefs").click();
      await settle(page);
      const setAt = await top(page, set);
      assert.ok(setAt > 100, `Settings is where it was left: ${setAt}`);
      await page.locator('#nav button[data-view="library"]').click();
      await settle(page);
      assert.equal(await top(page, libv), libAt, "the Library is where it was left");

      assert.deepEqual(errors, []);
      await context.close();
    });
  });
}

// The page's own strip (the header's tabs: the sidebar turned off, or a window
// narrower than SIDE_AT) is unchanged: a tab clicked there, like every click,
// leaves the page where it is (73af4c28, "where the reader is").
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a tab picked in the page's own strip leaves the page where it is`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const [name, width, layout] of [["sidebar off", 1100, "top"], ["narrow", 900, ""]]) for (const lang of Object.keys(words)) await t.test(`${name}, ${lang}`, async () => {
      const context = await browser.newContext({ viewport: { width, height: 480 }, reducedMotion: "reduce" });
      await context.addInitScript((layout) => { try { localStorage.setItem("magpie.settingsTab", "general"); localStorage.setItem("magpie.libTab", "mcp"); if (layout) localStorage.setItem("magpie.navLayout", layout); } catch {} }, layout);
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang));
      await page.goto("http://magpie.test/");
      await page.waitForSelector("#nav button.on");
      assert.ok(!(await page.evaluate(() => document.body.classList.contains("side-nav"))), "no sidebar");

      const libv = "#view-library";
      await page.locator('#nav button[data-view="library"]').click();
      await page.locator(`${libv} .lib-row, ${libv} .row`).first().waitFor();
      const at = await toFoot(page, libv);
      await page.locator(`${libv} .lib-tabs > button`).nth(1).click();
      await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-tabs > button")[1]?.classList.contains("on"));
      await settle(page);
      const after = await top(page, libv);
      assert.ok(after > 100, `the strip's click left the Library where it was, not at its top: ${at} then ${after}`);
      assert.deepEqual(errors, []);
      await context.close();
    });
  });
}
