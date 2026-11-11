// Run with Node's test runner and Playwright on the module path; see README.md.
// The sidebar's button (app.js toggleSideNav): at every width the reader
// picks the sidebar or the header's tabs, the choice kept for the next time
// the window opens, and the two trade places as one motion.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve(lang) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { url: "http://127.0.0.1:3425", running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: ["Hide sidebar", "Show sidebar"],
  zh: ["隐藏侧边栏", "显示侧边栏"],
  "zh-TW": ["隱藏側邊欄", "顯示側邊欄"],
  ja: ["サイドバーを非表示", "サイドバーを表示"],
  de: ["Seitenleiste ausblenden", "Seitenleiste einblenden"],
};
const frame = (page) => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
const side = (page) => page.evaluate(() => document.body.classList.contains("side-nav"));
// the header's row: the button, the tabs and the icons apart, in the window
async function headerFits(page) {
  const [b, n, a, w, sw] = await page.evaluate(() => [
    document.querySelector("#sideToggle").getBoundingClientRect().toJSON(),
    document.querySelector(".top #nav").getBoundingClientRect().toJSON(),
    document.querySelector(".top .actions").getBoundingClientRect().toJSON(),
    document.documentElement.clientWidth, document.documentElement.scrollWidth]);
  assert.ok(b.right <= n.left, `the button clear of the tabs: ${b.right} ${n.left}`);
  assert.ok(n.right <= a.left, `the tabs clear of the icons: ${n.right} ${a.left}`);
  assert.ok(sw <= w, "the window does not scroll sideways");
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: the sidebar's button`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());

    for (const [lang, [hide, show]] of Object.entries(words)) await t.test(lang, async () => {
      const context = await browser.newContext({ viewport: { width: 1280, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      // whether the sidebar was ever laid out, from the first frame on
      await page.addInitScript(() => {
        addEventListener("DOMContentLoaded", () => {
          window.sideSeen = document.body.classList.contains("side-nav");
          new MutationObserver(() => { if (document.body.classList.contains("side-nav")) window.sideSeen = true; }).observe(document.body, { attributes: true, attributeFilter: ["class"] });
        });
      });
      await page.goto("http://magpie.test/");
      await page.waitForSelector("#nav button.on");
      const mac = await page.evaluate(() => /^Mac/.test(navigator.platform));
      const keys = mac ? "⌃⌘S" : "Ctrl+Shift+S";

      // the sidebar to start with, and the button saying it hides it
      const btn = page.locator("#sideToggle");
      assert.ok(await side(page));
      assert.ok(await btn.isVisible(), "the button in the title bar");
      assert.equal(await btn.getAttribute("title"), `${hide} (${keys})`);
      assert.equal(await btn.getAttribute("aria-pressed"), "true");
      assert.equal(await btn.locator(".st-pane").evaluate((p) => getComputedStyle(p).opacity), "0.32", "its left pane filled");

      // the header's tabs, kept
      await btn.click();
      await page.waitForFunction(() => !document.body.classList.contains("side-nav"));
      await frame(page);
      assert.ok(await page.locator(".top #nav").isVisible(), "the tabs in the header");
      assert.equal(await page.locator("#side").isVisible(), false);
      assert.ok(await btn.isVisible(), "the button stays to bring the sidebar back");
      assert.equal(await btn.getAttribute("title"), `${show} (${keys})`);
      assert.equal(await btn.getAttribute("aria-pressed"), "false");
      assert.equal(await btn.locator(".st-pane").evaluate((p) => getComputedStyle(p).opacity), "0", "its pane empty");
      await headerFits(page);
      assert.equal(await page.evaluate(() => localStorage.getItem("magpie.navLayout")), "top");

      // opened again: the header from the first frame, no sidebar on the way
      await page.reload();
      await page.waitForSelector("#nav button.on");
      await frame(page);
      assert.equal(await side(page), false);
      assert.equal(await page.evaluate(() => window.sideSeen), false, "the sidebar never laid out");
      await headerFits(page);

      // narrower than the sidebar comes at by itself: the button stays, the
      // header holds
      await page.setViewportSize({ width: 1023, height: 760 });
      await frame(page);
      assert.ok(await btn.isVisible(), "the button at every width");
      assert.equal(await side(page), false, "the header still");
      await headerFits(page);
      // and the sidebar picked in a narrow window: narrower, the page beside it
      await page.setViewportSize({ width: 640, height: 760 });
      await frame(page);
      assert.ok(await btn.isVisible());
      await btn.click();
      await page.waitForFunction(() => document.body.classList.contains("side-nav"));
      await frame(page);
      assert.equal(await page.evaluate(() => localStorage.getItem("magpie.navLayout")), "side");
      assert.equal(Math.round((await page.locator("#side").boundingBox()).width), 184, "a narrower sidebar");
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), "the window does not scroll sideways");
      const [tb, cr, ac] = await page.evaluate(() => ["#sideToggle", "#crumb", ".top .actions"].map((q) => document.querySelector(q).getBoundingClientRect().toJSON()));
      assert.ok(tb.right <= cr.left + 0.5 && cr.right <= ac.left + 0.5, `the header's row apart: ${tb.right} ${cr.left} ${cr.right} ${ac.left}`);
      // widened, the choice holds
      await page.setViewportSize({ width: 1280, height: 760 });
      await frame(page);
      assert.ok(await side(page), "the sidebar still");

      // the shortcut trades them, and keeps what it gives
      await page.keyboard.press(mac ? "Control+Meta+KeyS" : "Control+Shift+KeyS");
      await page.waitForFunction(() => !document.body.classList.contains("side-nav"));
      assert.equal(await page.evaluate(() => localStorage.getItem("magpie.navLayout")), "top");
      await page.keyboard.press(mac ? "Control+Meta+KeyS" : "Control+Shift+KeyS");
      await page.waitForFunction(() => document.body.classList.contains("side-nav"));
      assert.equal(await page.evaluate(() => localStorage.getItem("magpie.navLayout")), "side");
      assert.ok(await page.locator("#side .side-scroll #nav").isVisible());
      assert.equal(await btn.getAttribute("title"), `${hide} (${keys})`);
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth));
      // Settings' tabs still listed under it
      await page.locator("#prefs").click();
      await page.locator("#side .nav-sub:not(.leaving) .nav-s").first().waitFor();

      assert.deepEqual(errors, []);
      await context.close();
    });

    // the button is in the one place whether the sidebar shows or not, as
    // Finder's and Notes' is: it hid the sidebar from the sidebar's top and
    // came back elsewhere, after the magpie (ztf4080 on X: "收起按钮在左上角，
    // 但是收起后展开按钮变了"). Each title bar, wide and narrow, every language
    // and the sidebar at its narrowest
    for (const [os, platform] of [["Mac", "MacIntel"], ["Windows", "Win32"], ["Linux", "Linux x86_64"]]) {
      await t.test(`${os}: the button stays where it was`, async () => {
        for (const lang of Object.keys(words)) for (const [width, sideW] of [[1280, 0], [900, 0], [640, 0], [1280, 168]]) {
          const context = await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" });
          const page = await context.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.addInitScript(([p, w]) => {
            Object.defineProperty(Navigator.prototype, "platform", { get: () => p });
            localStorage.setItem("magpie.navLayout", "side");
            if (w) localStorage.setItem("magpie.sideWidth", String(w));
          }, [platform, sideW]);
          await page.route("**/*", serve(lang));
          await page.goto("http://magpie.test/");
          await page.waitForSelector("#nav button.on");
          await frame(page);
          const at = `${lang} ${width}${sideW ? ` sidebar ${sideW}` : ""}`;
          const btn = page.locator("#sideToggle");
          const place = () => btn.evaluate((b) => { const r = b.getBoundingClientRect(); return { x: r.left, y: r.top, w: r.width, h: r.height }; });
          assert.ok(await side(page), at);
          const shown = await place();
          // the sidebar's top holds it and the magpie, nothing running over its edge
          assert.ok(await btn.evaluate((b) => !!b.closest("#side .side-top")), `${at}: in the sidebar's top`);
          assert.ok(await page.evaluate(() => { const s = document.querySelector(".side-top"); return s.scrollWidth <= s.clientWidth; }), `${at}: the sidebar's top fits`);
          assert.ok(shown.x + shown.w <= (await page.locator("#side").boundingBox()).width, `${at}: inside the sidebar`);
          for (const want of [false, true]) {
            await btn.click();
            await page.waitForFunction((w) => document.body.classList.contains("side-nav") === w, want);
            await frame(page);
            const now = await place();
            for (const k of ["x", "y", "w", "h"]) assert.ok(Math.abs(now[k] - shown[k]) <= 0.5, `${at}: ${want ? "shown again" : "hidden"}, the button moved: ${JSON.stringify(shown)} -> ${JSON.stringify(now)}`);
            if (!want) await headerFits(page);
          }
          assert.deepEqual(errors, [], at);
          await context.close();
        }
      });
    }

    await t.test("the two trade places as one motion", async () => {
      const context = await browser.newContext({ viewport: { width: 1280, height: 760 } });
      const page = await context.newPage();
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve("en"));
      await page.goto("http://magpie.test/");
      await page.waitForSelector("#nav button.on");
      assert.ok(await page.evaluate(() => !!document.startViewTransition), "the webview has view transitions");
      // what plays, read as the button is clicked: the old drawn, the new laid out
      const playing = () => page.evaluate(() => new Promise((r) => {
        const at = () => document.getAnimations().map((a) => a.effect?.pseudoElement).filter(Boolean);
        requestAnimationFrame(() => requestAnimationFrame(() => r({ anims: at(), morph: document.documentElement.classList.contains("nav-morph") })));
      }));
      await page.locator("#sideToggle").click();
      let p = await playing();
      assert.ok(p.morph, "the layouts trade places");
      assert.ok(p.anims.includes("::view-transition-old(side)"), `the sidebar slides away: ${p.anims}`);
      assert.ok(p.anims.includes("::view-transition-group(nav-usage)"), "each tab flies to the header");
      assert.ok(p.anims.includes("::view-transition-group(page)"), "the page moves over");
      // no tab is left out (#5434, invzhi on X): every tab drawn in the nav
      // has a name of its own while the morph plays, the masking tab's
      // group animating like the rest
      assert.ok(p.anims.includes("::view-transition-group(nav-masking)"), `the masking tab flies too: ${p.anims}`);
      const names = await page.evaluate(() => [...document.querySelectorAll("#nav button[data-view]")]
        .filter((b) => b.getClientRects().length)
        .map((b) => [b.dataset.view, getComputedStyle(b).viewTransitionName]));
      assert.ok(names.some(([v]) => v === "masking"), "the fixture draws the masking tab");
      for (const [v, n] of names) assert.ok(n && n !== "none", `${v} has a view-transition-name while morphing`);
      assert.equal(new Set(names.map(([, n]) => n)).size, names.length, `each name differs: ${JSON.stringify(names)}`);
      // off from rest and settling, in a third of a second, as a Mac's
      // sidebar: a curve leaving at speed made a frame the webview drops at
      // the start a visible jerk (wangye1644294 on X), and every piece, the
      // sidebar's own slide too, keeps to the one timing
      const timing = await page.evaluate(() => document.getAnimations()
        .filter((a) => /^::view-transition-(group\((page|nav-usage)\)|old\(side\))$/.test(a.effect?.pseudoElement || ""))
        .map((a) => { const t = a.effect.getComputedTiming(); return { el: a.effect.pseudoElement, duration: t.duration, easing: t.easing }; }));
      assert.equal(timing.length, 3, JSON.stringify(timing));
      for (const { el, duration, easing } of timing) {
        assert.ok(duration >= 250 && duration <= 350, `${el} lasts ${duration}ms`);
        const k = await page.evaluate((el) => getComputedStyle(document.documentElement, el).animationTimingFunction, el);
        const [x1, y1] = (k.match(/cubic-bezier\(([^)]*)\)/)?.[1] || "").split(",").map(Number);
        assert.ok(x1 > 0 && y1 === 0, `${el} sets off from rest: ${k} (${easing})`);
      }
      await page.waitForFunction(() => !document.documentElement.classList.contains("nav-morph"));
      assert.equal(await side(page), false);
      await headerFits(page);

      await page.locator("#sideToggle").click();
      p = await playing();
      assert.ok(p.anims.includes("::view-transition-new(side)"), `the sidebar slides in: ${p.anims}`);
      await page.waitForFunction(() => !document.documentElement.classList.contains("nav-morph"));
      assert.ok(await side(page));
      assert.ok(await page.locator("#side .side-scroll #nav").isVisible());
      assert.deepEqual(errors, []);
      await context.close();
    });

    // WebKitGTK with compositing off crashes in a view transition, the click
    // taking the window down (depravity_wings on X): the Linux app has none,
    // the new layout at once
    await t.test("Linux: at once, no transition", async () => {
      const context = await browser.newContext({ viewport: { width: 1280, height: 760 } });
      const page = await context.newPage();
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.addInitScript(() => {
        Object.defineProperty(Navigator.prototype, "platform", { get: () => "Linux x86_64" });
        window.morphs = 0;
        const real = Document.prototype.startViewTransition;
        if (real) Document.prototype.startViewTransition = function (...a) { window.morphs++; return real.apply(this, a); };
      });
      await page.route("**/*", serve("en"));
      await page.goto("http://magpie.test/");
      await page.waitForSelector("#nav button.on");
      assert.ok(await page.evaluate(() => document.body.classList.contains("linux")));
      for (const want of [false, true, false]) {
        await page.locator("#sideToggle").click();
        assert.equal(await side(page), want, "the other layout at once");
        assert.equal(await page.evaluate(() => document.documentElement.classList.contains("nav-morph")), false);
      }
      assert.equal(await page.evaluate(() => window.morphs), 0, "no view transition started");
      assert.ok(await page.locator(".top #nav").isVisible(), "the tabs in the header, the page answering clicks");
      assert.deepEqual(errors, []);
      await context.close();
    });

    // a webview whose transition never ends anywhere else: cut short soon
    // after its half second, so the window never stays on the old picture
    await t.test("a transition that never ends is cut short", async () => {
      const context = await browser.newContext({ viewport: { width: 1280, height: 760 } });
      const page = await context.newPage();
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.addInitScript(() => {
        window.skipped = 0;
        Document.prototype.startViewTransition = function (change) {
          let done;
          const finished = new Promise((r) => { done = r; });
          Promise.resolve().then(change);
          return { finished, updateCallbackDone: Promise.resolve(), ready: Promise.resolve(), skipTransition() { window.skipped++; done(); } };
        };
      });
      await page.route("**/*", serve("en"));
      await page.goto("http://magpie.test/");
      await page.waitForSelector("#nav button.on");
      const t0 = Date.now();
      await page.locator("#sideToggle").click();
      await page.waitForFunction(() => document.documentElement.classList.contains("nav-morph"));
      await page.waitForFunction(() => !document.documentElement.classList.contains("nav-morph"), null, { timeout: 5000 });
      const took = Date.now() - t0;
      assert.ok(took >= 1000, `not cut while it could still be playing: ${took}ms`);
      assert.equal(await page.evaluate(() => window.skipped), 1, "skipped once");
      assert.equal(await side(page), false);
      // and the button works again after
      await page.locator("#sideToggle").click();
      await page.waitForFunction(() => document.body.classList.contains("side-nav"));
      assert.deepEqual(errors, []);
      await context.close();
    });

    await t.test("less motion: at once", async () => {
      const context = await browser.newContext({ viewport: { width: 1280, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      await page.route("**/*", serve("en"));
      await page.goto("http://magpie.test/");
      await page.waitForSelector("#nav button.on");
      await page.evaluate(() => { window.morphed = false; new MutationObserver(() => { if (document.documentElement.classList.contains("nav-morph")) window.morphed = true; }).observe(document.documentElement, { attributes: true }); });
      await page.locator("#sideToggle").click();
      assert.equal(await side(page), false, "the header at once");
      assert.equal(await page.evaluate(() => window.morphed), false);
      await context.close();
    });
  });
}
