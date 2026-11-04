// Run with Node's test runner and Playwright on the module path; see README.md.
// The tray panel opens the main window from one place, never two (yogurt36
// on Discord): the edge nearest the tray (isaac_bai on Discord, with
// Windows' taskbar at the bottom). Hung below its icon (the Mac's menu bar,
// a top bar) it is the header's icon; standing above it (Windows' bottom
// taskbar) it is "Open the main window" left of Quit in the foot, and the
// header has no icon. Before the app says where it placed the panel
// (panelside.go), the side goes by the system: the foot on Windows.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function handler(lang, calls) {
  const state = { agents: [{ id: "codex", name: "codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/window/main" || url.pathname === "/api/window/quit") { calls.push(url.pathname); return route.fulfill({ status: 204 }); }
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const NAMES = { en: "Open the main window", zh: "打开主窗口", "zh-TW": "開啟主視窗", ja: "メインウィンドウを開く", de: "Hauptfenster öffnen" };
const QUITS = { en: "Quit", zh: "退出", "zh-TW": "退出", ja: "終了", de: "Beenden" };

// the controls that open the main window, as shown: [where, its name]
async function openers(page) {
  return page.evaluate(() => {
    const shown = (e) => e && e.offsetParent !== null && getComputedStyle(e).visibility !== "hidden";
    const out = [];
    const head = document.querySelector(".top #open");
    if (shown(head)) out.push(["head", head.title]);
    const foot = document.querySelector(".foot #openMain");
    if (shown(foot)) out.push(["foot", foot.textContent.trim()]);
    return out;
  });
}

async function open(engine, lang, platform, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width: 360, height: 520 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(8000);
  page.setDefaultNavigationTimeout(60000); // the first load, slow on a busy machine (#4470)
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  if (platform) await page.addInitScript((p) => Object.defineProperty(Navigator.prototype, "platform", { get: () => p }), platform);
  const calls = [];
  await page.route("**/*", handler(lang, calls));
  await page.goto("http://magpie.test/?mode=panel");
  await page.locator("#agents .row.agent").first().waitFor();
  return { page, calls, errors };
}

async function clicked(page, calls, sel) {
  calls.length = 0;
  await page.locator(sel).click();
  for (let i = 0; i < 100 && !calls.length; i++) await page.waitForTimeout(50);
  assert.deepEqual(calls, ["/api/window/main"], `${sel} opens the window`);
}

// the foot's buttons, as shown, left to right, none of them cut or overlapping
async function footRow(page) {
  return page.evaluate(() => {
    const foot = document.querySelector(".foot").getBoundingClientRect();
    const bs = [...document.querySelectorAll(".foot button")].filter((b) => b.offsetParent !== null);
    const rs = bs.map((b) => b.getBoundingClientRect());
    const fits = rs.every((r) => r.left >= foot.left - 0.5 && r.right <= foot.right + 0.5) && rs.every((r, i) => !i || r.left >= rs[i - 1].right - 0.5);
    const whole = bs.every((b) => b.scrollWidth <= b.clientWidth + 1);
    return { ids: bs.map((b) => b.id), fits, whole };
  });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    const name = NAMES[lang];

    test(`${engine} ${lang}: a panel below its icon opens the window from the header only`, async (t) => {
      const { page, calls, errors } = await open(engine, lang, "MacIntel", t);
      assert.deepEqual(await openers(page), [["head", name]], "one opener, the header's icon");
      assert.equal(await page.locator(".top").getByRole("button", { name, exact: true }).count(), 1, "found by its name");
      await clicked(page, calls, ".top #open");
      assert.deepEqual((await footRow(page)).ids, ["profBtn", "quit"], "the foot's buttons");
      assert.equal((await page.locator(".foot #quit").textContent()).trim(), QUITS[lang]);
      // the app placed it above the tray (a bottom bar on Linux): the foot's
      await page.evaluate(() => window.magpiePanelOpenAt("foot"));
      assert.deepEqual(await openers(page), [["foot", name]], "moved to the foot as the app says");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a panel above Windows' taskbar opens the window from the foot only, left of Quit`, async (t) => {
      const { page, calls, errors } = await open(engine, lang, "Win32", t);
      assert.deepEqual(await openers(page), [["foot", name]], "one opener, the foot's button");
      assert.equal(await page.locator(".top #open").isVisible(), false, "no header icon");
      const row = await footRow(page);
      assert.deepEqual(row.ids, ["profBtn", "openMain", "quit"], "left of Quit");
      assert(row.fits, "the foot's buttons fit, apart");
      assert(row.whole, "their words whole");
      assert.equal((await page.locator(".foot #quit").textContent()).trim(), QUITS[lang]);
      await clicked(page, calls, ".foot #openMain");
      // a taskbar moved to the top: the app says head
      await page.evaluate(() => window.magpiePanelOpenAt("head"));
      assert.deepEqual(await openers(page), [["head", name]], "back to the header as the app says");
      await page.evaluate(() => window.magpiePanelOpenAt("sideways"));
      assert.deepEqual(await openers(page), [["head", name]], "an unknown side changes nothing");
      assert.deepEqual(errors, []);
    });
  }
}
