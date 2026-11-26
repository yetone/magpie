// Run with Node's test runner and Playwright on the module path; see README.md.
// No control is drawn over another (the owner, 11-26: 「这个按钮被右边的按钮
// 给覆盖住了一部分，也要彻底避免这个问题的发生。」). A Usage card's ⋯ was
// placed absolutely in an 18px room at its head's end, and the fold before
// it, "Show the other accounts in brief", reached 7px into that room by a
// negative margin, so the ⋯ covered its end. usage-card-menu-room checked
// the fold's words, which were clear; its box and its hover weren't. Now on
// every page this visits, at a wide and a narrow width, in both engines and
// every language: no two controls a pointer can reach cross (opacity 0
// counts: it takes the click where it stands), and each one's middle is its
// own to click (control-room.cjs).
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { controlClashes } = require("./control-room.cjs");
const { server, visitPages } = require("./app-pages.cjs");

const LANGS = process.env.LANGS ? process.env.LANGS.split(",") : ["en", "zh", "zh-TW", "ja", "de"];
const WIDTHS = process.env.WIDTHS ? process.env.WIDTHS.split(",").map(Number) : [1180, 420];
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": no control is drawn over another", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of LANGS) {
      for (const width of WIDTHS) {
        await t.test(`${lang} ${width}px`, async (st) => {
          // tall, so a page's controls are in sight without scrolling
          const ctx = await browser.newContext({ viewport: { width, height: 2400 }, reducedMotion: "reduce" });
          st.after(() => ctx.close());
          const page = await ctx.newPage();
          page.setDefaultTimeout(8000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang));
          const bad = new Set();
          let count = 0, folds = 0;
          await visitPages(page, async (view) => {
            const r = await page.evaluate(`(${controlClashes.toString()})(document.body)`);
            count += r.count;
            if (view.startsWith("usage")) folds += await page.locator("#subscriptionUsage .subscription-head .quota-more").count();
            for (const c of r.crossed) bad.add(`${view}: ${c.a} and ${c.b} cross by ${c.w.toFixed(1)}×${c.h.toFixed(1)}px`);
            for (const c of r.covered) bad.add(`${view}: ${c.a} has ${c.by} over its middle`);
          });
          assert.ok(folds >= 2, "the Usage cards' folds were never drawn");
          assert.ok(count >= 100, `only ${count} controls were found`);
          assert.deepEqual([...bad], [], `${bad.size} controls over another`);
          assert.deepEqual(errors, []);
        });
      }
    }
  });
}
