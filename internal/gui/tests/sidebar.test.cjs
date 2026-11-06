// Run with Node's test runner and Playwright on the module path; see README.md.
// The window's sidebar (app.js layoutNav): from 1024 points the pages are
// listed down the left in their groups, the open one's own tabs under it in
// place of the page's strip, Settings and the gateway's address at the foot
// and the header a breadcrumb; narrower, they go back into the header's row.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, reads, gateway = { url: "http://127.0.0.1:3425", running: true, window: true }) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:false,sideGateway:{url:"http://127.0.0.1:3425",running:true,boot:true}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") { reads.push(performance.now()); return json({ providers: [], presets: [], excluded: [], gateway }); }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// each language's group names, and Settings' Models tab
const words = {
  en: { groups: ["Work", "Models", "Insights", "Extend"], settings: "Settings", models: "Models" },
  zh: { groups: ["使用", "模型", "观测", "扩展"], settings: "设置", models: "模型" },
  "zh-TW": { groups: ["使用", "模型", "觀測", "擴充"], settings: "設定", models: "模型" },
  ja: { groups: ["利用", "モデル", "観測", "拡張"], settings: "設定", models: "モデル" },
  de: { groups: ["Arbeit", "Modelle", "Einblick", "Zusätze"], settings: "Einstellungen", models: "Modelle" },
};
const frame = (page) => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: the window's sidebar`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());

    for (const [lang, w] of Object.entries(words)) await t.test(lang, async () => {
      const reads = [];
      const context = await browser.newContext({ viewport: { width: 1024, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, reads));
      await page.goto("http://magpie.test/");
      await page.waitForSelector("#nav button.on");

      // at 1024 points the pages are down the left, in their groups
      assert.ok(await page.evaluate(() => document.body.classList.contains("side-nav")), "a sidebar from 1024 points");
      assert.ok(await page.locator("#side .side-scroll #nav").isVisible(), "the pages are in the sidebar");
      assert.equal(await page.locator(".top #nav").count(), 0, "and not in the header");
      assert.deepEqual(await page.locator("#nav .nav-gl:visible").allTextContents(), w.groups);
      assert.deepEqual(await page.locator("#nav button[data-view]").evaluateAll((bs) => bs.map((b) => b.dataset.view)),
        ["agents", "sessions", "library", "providers", "routing", "groups", "gateway", "usage", "masking", "plugins"]);
      assert.ok(await page.locator("#side .side-foot #prefs").isVisible(), "Settings at the sidebar's foot");
      assert.equal((await page.locator("#prefs .side-l").textContent()).trim(), w.settings);
      // no page's name or group cut, and nothing scrolls sideways
      const cut = await page.locator("#side :is(.nav-gl, button[data-view] > span, #prefs .side-l)").evaluateAll((es) => es.filter((e) => e.scrollWidth > e.clientWidth + 1).map((e) => e.textContent));
      assert.deepEqual(cut, [], "names read whole");
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), "the window does not scroll sideways");

      // the open page in the graphite of the text, not an accent colour
      const [ic, fg] = await page.evaluate(() => [getComputedStyle(document.querySelector("#nav button.on .nav-ic")).color, getComputedStyle(document.body).color]);
      assert.equal(ic, fg, "the open page's icon is the text's colour");

      // a row pressed darkens and keeps its size: no button's squeeze
      for (const sel of ['#nav button[data-view="usage"]', "#prefs"]) {
        const r = page.locator(sel);
        const bg = await r.evaluate((b) => getComputedStyle(b).backgroundColor);
        const box = await r.boundingBox();
        await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
        await page.mouse.down();
        await page.waitForTimeout(250);
        const [tf, pressed] = await r.evaluate((b) => [getComputedStyle(b).transform, getComputedStyle(b).backgroundColor]);
        await page.mouse.move(box.x + box.width + 400, box.y);
        await page.mouse.up();
        assert.equal(tf, "none", `${sel} pressed is not squeezed`);
        // a page's row has the open page's shape come to it instead
        // (sidebar-glide.test.cjs); Settings, at the foot, darkens
        if (sel === "#prefs") assert.notEqual(pressed, bg, `${sel} pressed darkens`);
      }

      // the gateway's address at the foot, drawn from boot.js ahead of the
      // read, not held back until the providers have come in
      assert.ok(await page.locator("#sideGw").isVisible(), "the gateway's line from boot.js");
      assert.match(await page.locator("#sideGw").textContent(), /127\.0\.0\.1:3425/);
      assert.ok(await page.locator("#sideGw span").evaluate((s) => s.scrollWidth <= s.clientWidth), "the address is read whole");
      assert.equal(reads.length, 1, "read once for the sidebar");

      // Settings: its tabs listed under it, its own strip hidden, and picked there
      await page.locator("#prefs").click();
      const subs = page.locator("#side .nav-sub:not(.leaving) .nav-s");
      await subs.first().waitFor();
      assert.equal(await subs.count(), 8);
      assert.equal(await page.locator("#setTabs").isVisible(), false, "the page's own strip is hidden");
      assert.deepEqual(await subs.evaluateAll((ss) => ss.map((s) => s.dataset.of)),
        ["setTab-general", "setTab-usage", "setTab-network", "setTab-models", "setTab-privacy", "setTab-otel", "setTab-sync", "setTab-about"]);
      assert.deepEqual(await subs.evaluateAll((ss) => ss.filter((s) => s.scrollWidth > s.clientWidth + 1).map((s) => s.textContent)), [], "tab names read whole");
      // the selection sets off as the row is pressed, ahead of the page,
      // and goes back when the press leaves the row
      const hlTo = () => page.evaluate(() => { const h = document.querySelector(".nav-sub:not(.leaving) .nav-hl"); return h.style.transform; });
      const rowAt = (of) => page.evaluate((of) => `translateY(${document.querySelector(`.nav-s[data-of=${of}]`).offsetTop}px)`, of);
      const startAt = await hlTo();
      const net = await page.locator(".nav-s[data-of=setTab-network]").boundingBox();
      await page.mouse.move(net.x + net.width / 2, net.y + net.height / 2);
      await page.mouse.down();
      assert.equal(await hlTo(), await rowAt("setTab-network"), "pressed, the selection is going to it");
      assert.equal(await page.locator("#setTab-network").evaluate((b) => b.classList.contains("on")), false, "before the page has the tab");
      // the selection is the one shape: the row pressed has no ground of its own
      assert.equal(await page.locator(".nav-s[data-of=setTab-network]").evaluate((b) => getComputedStyle(b).backgroundColor), "rgba(0, 0, 0, 0)", "no block under the finger ahead of the selection");
      await page.mouse.move(net.x + net.width / 2, net.y + 200);
      assert.equal(await hlTo(), startAt, "the press left the row: back");
      await page.mouse.up();
      assert.equal(await page.locator("#setTab-network").evaluate((b) => b.classList.contains("on")), false, "nothing picked");
      await page.locator(".nav-s[data-of=setTab-models]").click();
      await page.waitForFunction(() => document.querySelector("#setTab-models").classList.contains("on"));
      await frame(page);
      assert.equal(await page.locator(".nav-s.cur").getAttribute("data-of"), "setTab-models");
      assert.equal(await page.locator(".nav-s.cur").getAttribute("aria-selected"), "true");
      const crumb = await page.locator("#crumb").evaluate((c) => [...c.querySelectorAll("span, b")].map((e) => e.textContent));
      assert.deepEqual(crumb, [w.settings, w.models], "the header says where the reader is");
      // the selection sits on the tab picked
      const [hl, cur] = await page.evaluate(() => [document.querySelector(".nav-sub:not(.leaving) .nav-hl").getBoundingClientRect().top, document.querySelector(".nav-s.cur").getBoundingClientRect().top]);
      assert.ok(Math.abs(hl - cur) < 1, `the selection is on the tab: ${hl} ${cur}`);

      // another page without tabs of its own: the list folds away
      await page.locator('#nav button[data-view="agents"]').click();
      await page.waitForFunction(() => !document.querySelector(".nav-sub"));

      // a point narrower, the header's row has them again, and no sidebar
      await page.setViewportSize({ width: 1023, height: 760 });
      await page.waitForFunction(() => !document.body.classList.contains("side-nav"));
      await frame(page);
      assert.ok(await page.locator(".top #nav").isVisible(), "the pages are in the header");
      assert.equal(await page.locator("#side").isVisible(), false);
      assert.equal(await page.locator("#crumb").isVisible(), false);
      assert.equal(await page.locator("#nav .nav-gl:visible").count(), 0, "no group names in the header's row");
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), "the window does not scroll sideways");
      // Settings' tabs are its own strip again
      await page.locator("#prefs").click();
      await page.locator("#setTabs").waitFor();
      assert.equal(await page.locator(".nav-sub").count(), 0);

      assert.deepEqual(errors, []);
      await context.close();
    });

    await t.test("narrow from the start: the gateway is not read again for a sidebar", async () => {
      const reads = [];
      const context = await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      await page.route("**/*", serve("en", reads));
      await page.goto("http://magpie.test/");
      await page.waitForSelector("#nav button.on");
      await page.waitForTimeout(2000);
      // the providers list is read once after the start, for the
      // Providers page to open on it (#1519), and nothing more with no
      // sidebar to show its gateway
      assert.equal(reads.length, 1, "read once, for the Providers page");
      // made wide, the sidebar shows the gateway from that read
      await page.setViewportSize({ width: 1200, height: 700 });
      await page.locator("#sideGw").waitFor();
      await page.waitForTimeout(500);
      assert.equal(reads.length, 1, "the sidebar reads nothing more");
      await context.close();
    });

    await t.test("the line boot.js drew is read again: a gateway gone since says so", async () => {
      // boot.js is written once this process holds the gateway, before its
      // listen is known to have worked; the read after the start has the
      // last word on the line it drew (#3429)
      const reads = [];
      const context = await browser.newContext({ viewport: { width: 1200, height: 700 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      await page.route("**/*", serve("en", reads, { url: "http://127.0.0.1:3490", running: false }));
      await page.goto("http://magpie.test/");
      await page.waitForSelector("#nav button.on");
      assert.match(await page.locator("#sideGw").textContent(), /127\.0\.0\.1:3425/, "boot.js's line first");
      await page.waitForFunction(() => document.querySelector("#sideGw span")?.textContent === "Gateway stopped");
      assert.ok(await page.locator("#sideGw").evaluate((b) => b.classList.contains("off")));
      assert.match(await page.locator("#sideGw").getAttribute("title"), /3490/);
      assert.equal(reads.length, 1, "the read the Providers page is made ready with, no other");
      await context.close();
    });

    await t.test("the list opens and folds as it plays", async () => {
      const context = await browser.newContext({ viewport: { width: 1200, height: 760 } });
      const page = await context.newPage();
      await page.route("**/*", serve("en", []));
      await page.goto("http://magpie.test/");
      await page.waitForSelector("#nav button.on");
      // the unroll is counted as its animations are made, ahead of the
      // click (#2950): on a busy machine the whole play is over before
      // the one sample after the click lands, and a finished animation
      // is out of getAnimations(); an animation made is a fact that
      // stays put, so the assert reads that, not the moment sampled
      await page.evaluate(() => {
        window.navAnims = [];
        const animate = Element.prototype.animate;
        Element.prototype.animate = function (frames, options) {
          window.navAnims.push(this);
          return animate.call(this, frames, options);
        };
      });
      await page.locator("#prefs").click();
      const box = page.locator("#side .nav-sub").first();
      await box.waitFor();
      assert.ok(await box.evaluate((b) => window.navAnims.includes(b)), "the list unrolls");
      await page.locator('#nav button[data-view="agents"]').click();
      // folding: out of reach at once, gone once it has played
      assert.equal(await page.locator(".nav-sub:not(.leaving)").count(), 0);
      assert.equal(await page.locator(".nav-s").count(), 0, "its rows are no tabs while they fold");
      await page.waitForFunction(() => !document.querySelector(".nav-sub"));
      await context.close();
    });
  });
}
