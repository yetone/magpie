// Run with Node's test runner and Playwright on the module path; see README.md.
// shannnnnks on X (magpie in Docker, v0.1.4059): Day card's icon in the
// sidebar was black on the dark sidebar. Its icon.svg (saved here as it is)
// draws with currentColor, and a plugin page's tab drew it as an <img>,
// where currentColor is black whatever the theme. A plugin page's mark in
// one near-black or near-white tone is now drawn as the built-in pages'
// marks are, in the tab's own ink (toneCheck, a mask), and a coloured
// picture as it is. Both themes, the desktop and the web window, every
// language, the phone's More list, Chromium and WebKit; the ink that stands
// apart from the sidebar's ground is measured on a screenshot.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const DAY = "a".repeat(32), WHITE = "b".repeat(32), GLANCE = "c".repeat(32);
const icons = {
  [DAY]: () => fs.readFile(path.join(__dirname, "fixtures/day-card-icon.svg")),
  [WHITE]: async () => `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><circle cx="8" cy="8" r="6" fill="#fff"/></svg>`,
  [GLANCE]: () => fs.readFile(path.resolve(__dirname, "../testdata/page-plugin/page/icon.svg")),
};
const entry = (tok, name, title) => ({ spec: "/x/" + name, name, key: "plugin-" + name, title, reads: [], url: `/_magpie/plugin/${tok}/index.html`, icon: `/_magpie/plugin/${tok}/icon.svg` });
const pages = [entry(DAY, "day-card", "Day card"), entry(WHITE, "white", "White mark"), entry(GLANCE, "glance", "Usage glance")];

function server(lang, theme, web) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"${theme}",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme } });
    if (url.pathname === "/api/settings") return json({ lang, theme, web });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/plugin-pages") return json(pages);
    if (url.pathname.startsWith("/api/")) return json({});
    const m = url.pathname.match(/^\/_magpie\/plugin\/([0-9a-f]{32})\/icon\.svg$/);
    if (m && icons[m[1]]) return route.fulfill({ contentType: "image/svg+xml", body: await icons[m[1]]() });
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// The share of each box's pixels that stand apart from the ground (a
// colour distance over 48 of 441), read from one screenshot of the page;
// the ground is read just left of the box, in its tab.
async function inked(page, sel) {
  const shot = (await page.screenshot()).toString("base64");
  return page.evaluate(async ([shot, sel]) => {
    const img = new Image();
    img.src = "data:image/png;base64," + shot;
    await img.decode();
    const c = document.createElement("canvas");
    c.width = img.naturalWidth; c.height = img.naturalHeight;
    const x = c.getContext("2d", { willReadFrequently: true });
    x.drawImage(img, 0, 0);
    const k = img.naturalWidth / innerWidth, out = {};
    for (const e of document.querySelectorAll(sel)) {
      const b = e.getBoundingClientRect();
      const L = Math.round(b.left * k), T = Math.round(b.top * k), W = Math.round(b.width * k), H = Math.round(b.height * k);
      const d = x.getImageData(L, T, W, H).data;
      const g = x.getImageData(L - 3, T + (H >> 1), 1, 1).data;
      let n = 0;
      for (let i = 0; i < d.length; i += 4) if (Math.hypot(d[i] - g[0], d[i + 1] - g[1], d[i + 2] - g[2]) > 48) n++;
      out[e.closest("button").dataset.view] = n / (W * H);
    }
    return out;
  }, [shot, sel]);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a plugin page's one-tone mark is drawn in the sidebar's ink`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const theme of ["dark", "light"]) for (const web of [false, true]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(`${theme} ${web ? "web" : "window"} ${lang}`, async () => {
        const context = await browser.newContext({ viewport: { width: 1100, height: 760 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(8000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, theme, web));
        await page.goto("http://magpie.test/");
        assert.ok(await page.evaluate(() => document.body.classList.contains("side-nav")), "the sidebar");
        const tab = (k) => page.locator(`#nav button[data-view="plugin-${k}"]`);
        await tab("day-card").locator(".nav-pic .mask").waitFor();
        await tab("white").locator(".nav-pic .mask").waitFor();
        await tab("glance").locator(".nav-pic img").waitFor();
        await page.waitForFunction(() => [...document.querySelectorAll("#nav .nav-pic img")].every((i) => i.complete));
        assert.equal(await tab("day-card").locator("img").count(), 0, "Day card's picture gave way to its mask");
        assert.equal(await tab("glance").locator(".mask").count(), 0, "a coloured picture is drawn as it is");
        for (const k of ["day-card", "white"]) {
          const [paint, ink, w] = await tab(k).locator(".nav-pic .mask").evaluate((m) => [getComputedStyle(m).backgroundColor, getComputedStyle(document.querySelector('#nav button[data-view="usage"] .nav-ic')).color, m.getBoundingClientRect().width]);
          assert.equal(paint, ink, `${k}: in the ink a built-in page's mark is`);
          assert.equal(w, 16, `${k}: the size the built-in pages' marks are`);
        }
        // the tab's list read again (a plugin turned on, a language picked)
        // retitles the tab and keeps its mark
        await page.evaluate(() => window.loadPluginPages());
        await page.waitForTimeout(300);
        assert.equal(await tab("day-card").locator(":scope > span").textContent(), "Day card", "the title in its own span");
        assert.equal(await tab("day-card").locator(".nav-pic .mask").count(), 1, "the mark kept on a second read");
        await page.waitForTimeout(100);
        const ink = await inked(page, "#nav button[data-plugin-page] .nav-pic");
        for (const k of ["plugin-day-card", "plugin-white", "plugin-glance"]) assert.ok(ink[k] > 0.08, `${k} shows on the ${theme} sidebar: ${(ink[k] * 100).toFixed(1)}% inked`);
        // the open page's mark in the text's own colour, as a built-in's is
        await page.evaluate(() => show("plugin-day-card"));
        await page.waitForFunction(() => document.querySelector('#nav button[data-view="plugin-day-card"]').classList.contains("on"));
        // the tab's ink eases over .22s (#nav.hl-on): wait it out
        await page.waitForFunction(() => { const m = document.querySelector('#nav button[data-view="plugin-day-card"] .mask'); return getComputedStyle(m).backgroundColor === getComputedStyle(document.body).color; }, null, { timeout: 2000 }).catch(() => {});
        const [on, fg] = await page.evaluate(() => [getComputedStyle(document.querySelector('#nav button[data-view="plugin-day-card"] .mask')).backgroundColor, getComputedStyle(document.body).color]);
        assert.equal(on, fg, "the open page's mark is the text's colour");
        if (process.env.ARTIFACT_DIR && lang === "zh") await page.locator("#nav").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `nav-ic-${engine}-${theme}-${web ? "web" : "window"}.png`) });
        assert.deepEqual(errors, []);
        await context.close();
      });
    }
    // on a phone the pages past the tab bar are in More's list, each with
    // its tab's mark: Day card's there in the list's ink too
    for (const theme of ["dark", "light"]) await t.test(`${theme} phone More`, async () => {
      const context = await browser.newContext({ viewport: { width: 390, height: 760 }, reducedMotion: "reduce", hasTouch: true, isMobile: engine === "chromium" });
      const page = await context.newPage();
      page.setDefaultTimeout(8000);
      await page.route("**/*", server("en", theme, true));
      await page.goto("http://magpie.test/");
      await page.locator('#nav button[data-view="plugin-day-card"] .nav-pic .mask').waitFor({ state: "attached" });
      await page.locator("#nav .nav-more").tap();
      const menu = page.locator(".nav-more-menu");
      await menu.waitFor({ state: "visible" });
      const item = menu.locator(".pm-item", { hasText: "Day card" });
      const [paint, ink, w] = await item.locator(".nav-pic .mask").evaluate((m) => [getComputedStyle(m).backgroundColor, getComputedStyle(m).color, m.getBoundingClientRect().width]);
      assert.equal(paint, ink, "in the list's ink");
      assert.equal(w, 20, "the size More's marks are");
      await context.close();
    });
  });
}
