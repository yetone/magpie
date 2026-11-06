// Run with Node's test runner and Playwright on the module path; see README.md.
// A Routing group editor's Cancel and Add, held at the window's foot while
// the editor runs on below it (Jiabubu on Discord): the Scroll to top and
// Scroll to bottom capsule (scrollends.js) sat over Add, and under the bar's
// ground the view's own foot padding showed as a gap above the window's
// edge. Here the editor of a group far down the Routing groups page is scrolled so its
// bar is held: the bar's ground reaches the window's edge, and the capsule,
// once in, is above the bar and over none of its buttons. Chromium and
// WebKit, the desktop window and the browser page, wide and narrow,
// en/zh/zh-TW/ja/de.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = Array.from({ length: 4 }, (_, i) => ({ id: `p/m${i}`, name: `m${i}`, providerName: "P", icon: "generic" }));
const ids = models.map((m) => m.id);
// enough groups above for the page to be scrolled a screen down
const groups = {
  models, pools: [],
  groups: Array.from({ length: 40 }, (_, i) => ({ id: `g${i}`, name: `group ${i}`, routing: "order", members: ids, ready: true, memberInfo: ids.map((id) => ({ id, ready: true })), offers: [], shared: [] })),
};

function serve(lang, web) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/t", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}


for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: a group editor's Cancel and Add, held at the foot, have the capsule above them and no gap under`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const web of [false, true]) {
        for (const width of web ? [1100, 700, 440, 360] : [1100, 700, 560]) {
          const at = `${web ? "web" : "desktop"} ${width}px`;
          const page = await browser.newPage({ viewport: { width, height: 560 }, reducedMotion: "reduce" });
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, web));
          await page.goto("http://magpie.test/?view=groups");
          await page.locator(".rt-group").nth(15).waitFor();
          // a group far down the list, its editor opened in its place
          await page.evaluate(() => [...document.querySelectorAll(".rt-group")[20].querySelectorAll("button")].find((b) => b.textContent === t("Edit")).click());
          await page.locator(".rt-gedit").waitFor();
          // the editor's top just under the window's: the page a screen
          // down, the editor running on below, its bar held at the foot;
          // by the wheel, as a reader scrolls (a scroll by code is put back)
          const want = await page.evaluate(() => {
            const v = document.querySelector("#view-groups"), e = document.querySelector(".rt-gedit");
            return Math.max(v.scrollTop + e.getBoundingClientRect().top - v.getBoundingClientRect().top - 20, v.clientHeight + 40);
          });
          const vb = await page.locator("#view-groups").boundingBox();
          await page.mouse.move(vb.x + vb.width / 2, vb.y + vb.height / 2);
          for (let i = 0; i < 40; i++) {
            const now = await page.evaluate(() => document.querySelector("#view-groups").scrollTop);
            if (Math.abs(now - want) < 2) break;
            await page.mouse.wheel(0, want - now);
            await page.waitForTimeout(60);
          }
          // the capsule comes on the window's scroll, a frame later
          await page.waitForFunction(() => document.querySelector(".view-ends.on"));
          await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(() => setTimeout(r, 250)))));
          const m = await page.evaluate(() => {
            const box = (e) => { const b = e.getBoundingClientRect(); return { top: b.top, bottom: b.bottom, left: b.left, right: b.right }; };
            const v = document.querySelector("#view-groups"), e = document.querySelector(".rt-gedit"), bar = e.querySelector(":scope > .bar");
            return { view: box(v), ed: box(e), bar: box(bar), ends: box(document.querySelector(".view-ends")), buttons: [...bar.querySelectorAll("button")].map((b) => ({ text: b.textContent, ...box(b) })), scrolled: v.scrollTop > v.clientHeight };
          });
          assert(m.scrolled && m.ed.bottom > m.view.bottom + 40, `${at}: the page a screen down and the editor running on below the window: ${JSON.stringify([m.ed, m.view])}`);
          assert(Math.abs(m.bar.bottom - m.view.bottom) <= 1, `${at}: the bar's ground down to the window's edge, no gap: bar ${m.bar.bottom}, window ${m.view.bottom}`);
          assert(m.ends.bottom <= m.bar.top + 1, `${at}: the capsule above the bar: ${m.ends.top}-${m.ends.bottom}, bar from ${m.bar.top}`);
          for (const b of m.buttons) {
            const over = b.left < m.ends.right && b.right > m.ends.left && b.top < m.ends.bottom && b.bottom > m.ends.top;
            assert(!over, `${at}: the capsule over ${b.text}: ${JSON.stringify([b, m.ends])}`);
          }
          // Add still takes a click where it is drawn
          const add = page.locator(".rt-gedit > .bar button.primary");
          const ab = await add.boundingBox();
          const hit = await page.evaluate(([x, y]) => document.elementFromPoint(x, y)?.closest("button")?.classList.contains("primary"), [ab.x + ab.width - 4, ab.y + ab.height / 2]);
          assert(hit, `${at}: Add's right end is Add's, nothing over it`);
          assert.deepEqual(errors, [], `${at}: no page error`);
          await page.close();
        }
      }
    });
  }
}
