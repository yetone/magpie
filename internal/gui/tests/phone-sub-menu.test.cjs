// Run with Node's test runner and Playwright on the module path; see README.md.
// magpie web on a phone (gandli on X: 目前 magpie 手机移动端的导航设计有点
// 复杂，同时存在底部和顶部二级导航，用起来不太顺手). Under the header the
// Library's dozen tabs wrapped into three lines and Settings' eight into two,
// a second bar over the tab bar at the foot. Past the five an iPhone's
// segmented control holds, a page's own tabs are now one menu at its top,
// "Instructions ⌄", as Usage's days are: one line, every tab in its list, a
// pick opening that tab. Usage's four tabs and Plugins' three stay a
// segmented control, and a desktop browser keeps every strip as it was. In
// every language, at 390px and at 320px.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const HOME = "/Users/me";
// an agent on this computer: the Library's every tab, a dozen; with none, its three
const claude = { id: "claude", name: "Claude Code", icon: "claudecode-color", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude/mcp.json` };
const lib = (agents) => ({ dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME, agents, instructions: { agents: [], sets: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], skills: [], problems: [], hooks: [] });

function serve(lang, agentless) {
  const providers = { providers: [], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light", redact: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/settings") return json({ lang, theme: "light", web: true });
    if (url.pathname === "/api/library") return json(lib(agentless ? [] : [claude]));
    // RTK and billion-context, not installed: the list's last tabs
    if (/^\/api\/library\/(rtk|bili)$/.test(url.pathname)) return json({ path: "", version: "", url: "", agents: [{ id: "claude", name: "Claude Code", icon: "", on: false }] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/plugins/market") return json({ listings: [], state: { bun: true, bunVersion: "1.3.0", plugins: [], picker: false } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

async function open(browser, lang, view, width = 390, agentless = false) {
  const phone = width < 700;
  const context = await browser.newContext({ viewport: { width, height: phone ? 844 : 800 }, deviceScaleFactor: 2, hasTouch: phone, isMobile: phone && browser.browserType().name() === "chromium", reducedMotion: "reduce" });
  const page = await context.newPage();
  page.setDefaultTimeout(8000);
  page.setDefaultNavigationTimeout(60000);
  page.errors = [];
  page.on("pageerror", (e) => page.errors.push(e.message));
  // a desktop window with the tabs across the top, where the strips show
  if (!phone) await page.addInitScript(() => localStorage.setItem("magpie.navLayout", "top"));
  await page.route("**/*", serve(lang, agentless));
  await page.goto("http://magpie.test/?view=" + view);
  await page.waitForSelector("#view-" + view, { state: "visible" });
  return page;
}

const STRIP = { library: "#view-library .lib-tabs", settings: "#setTabs" };
// the strip's tabs, each with its words and whether it is the open one
const stripTabs = (page, q) => page.$$eval(q + " > button", (bs) => bs.filter((b) => !b.hidden).map((b) => ({ name: b.textContent.trim(), on: b.classList.contains("on") })));
const shown = (page, q) => page.$$eval(q, (es) => es.some((e) => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0 && getComputedStyle(e).display !== "none"; }));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    for (const width of [390, 320]) {
      for (const view of ["library", "settings"]) {
        test(`${engine} ${lang} ${width}px: ${view}'s tabs are one menu at its top, not a second bar`, async (t) => {
          const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
          t.after(() => browser.close());
          const page = await open(browser, lang, view, width);
          const menu = page.locator(`#view-${view} .sub-menu`);
          await menu.waitFor({ state: "visible" });
          const tabs = await stripTabs(page, STRIP[view]);
          assert.ok(tabs.length > 5, view + " has more tabs than a segmented control holds: " + tabs.length);
          assert.equal(await shown(page, STRIP[view]), false, "the wrapped strip is gone");
          // one line at the page's top, under the header, inside the window
          const mb = await menu.boundingBox(), top = await page.locator(".top").boundingBox();
          assert.ok(mb.height <= 34, "one line: " + JSON.stringify(mb));
          assert.ok(mb.y >= top.y + top.height - 1 && mb.y < top.y + top.height + 40, "just under the header: " + JSON.stringify({ mb, top }));
          assert.ok(mb.x >= 0 && mb.x + mb.width <= width + 0.5, "inside the window: " + JSON.stringify(mb));
          assert.equal((await menu.innerText()).trim(), tabs.find((x) => x.on).name, "it says the open tab");
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "nothing scrolls sideways");
          // its list is every tab, the open one ticked; a pick opens that tab
          await menu.tap();
          const pop = page.locator(".sub-menu-pop");
          await pop.waitFor({ state: "visible" });
          assert.deepEqual(await pop.locator(".pm-item .pm-name").allInnerTexts(), tabs.map((x) => x.name), "every tab, in order");
          assert.equal(await pop.locator('.pm-item[aria-checked="true"] .pm-name').innerText(), tabs.find((x) => x.on).name);
          const pb = await pop.boundingBox();
          assert.ok(pb.x >= 0 && pb.x + pb.width <= width + 0.5, "the list is inside the window: " + JSON.stringify(pb));
          const pick = tabs.length - 1;
          await pop.locator(".pm-item").nth(pick).tap();
          await page.waitForFunction(([q, i]) => [...document.querySelectorAll(q + " > button")].filter((b) => !b.hidden)[i]?.classList.contains("on"), [STRIP[view], pick]);
          await page.waitForFunction(([v, name]) => document.querySelector(`#view-${v} .sub-menu`)?.textContent.trim() === name, [view, tabs[pick].name]);
          assert.equal(await page.locator(".sub-menu-pop").count(), 0, "the list closes");
          if (view === "settings") await page.waitForSelector("#setPage-about", { state: "visible" });
          assert.equal(await page.locator(`#view-${view} .sub-menu`).count(), 1, "one menu, however often the page draws its head");
          assert.deepEqual(page.errors, []);
        });
      }
    }
    test(`${engine} ${lang}: Usage's, Plugins' and an agentless Library's few tabs stay a segmented control on a phone`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await open(browser, lang, "usage");
      await page.waitForTimeout(300);
      assert.equal(await shown(page, "#usageTab"), true);
      assert.equal(await page.locator("#view-usage .sub-menu").count(), 0);
      const plugins = await open(browser, lang, "plugins");
      await plugins.waitForSelector("#view-plugins .lib-tabs", { state: "visible" });
      await plugins.waitForTimeout(300);
      assert.equal(await plugins.locator("#view-plugins .sub-menu").count(), 0);
      const few = await open(browser, lang, "library", 390, true);
      await few.waitForFunction(() => document.querySelectorAll("#view-library .lib-tabs > button").length === 3);
      await few.waitForTimeout(300);
      assert.equal(await shown(few, "#view-library .lib-tabs"), true);
      assert.equal(await few.locator("#view-library .sub-menu").count(), 0);
      assert.deepEqual([...page.errors, ...plugins.errors, ...few.errors], []);
    });
  }

  test(`${engine}: a desktop browser keeps the Library's and Settings' tabs as they were`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const view of ["library", "settings"]) {
      const page = await open(browser, "en", view, 1280);
      await page.waitForSelector(STRIP[view], { state: "visible" });
      await page.waitForTimeout(300);
      assert.equal(await page.locator(`#view-${view} .sub-menu`).count(), 0, view + " has no menu");
      assert.equal(await shown(page, STRIP[view]), true, view + "'s strip shows");
      await page.context().close();
    }
  });

  test(`${engine}: a phone turned wide gets the strip back, and narrow again the menu`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await open(browser, "en", "settings");
    await page.locator("#view-settings .sub-menu").waitFor({ state: "visible" });
    await page.setViewportSize({ width: 1100, height: 800 });
    await page.waitForFunction(() => !document.querySelector(".sub-menu"));
    // the strip is the page's again (the sidebar may list it at this width)
    assert.equal(await page.locator("#setTabs.sub-as-menu").count(), 0);
    await page.setViewportSize({ width: 390, height: 844 });
    await page.locator("#view-settings .sub-menu").waitFor({ state: "visible" });
    assert.equal(await shown(page, "#setTabs"), false);
    assert.deepEqual(page.errors, []);
  });
}
