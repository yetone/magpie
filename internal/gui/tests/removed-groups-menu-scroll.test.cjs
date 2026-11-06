// Run with Node's test runner and Playwright on the module path; see README.md.
// With many found groups removed, the menu that brings one back ran off the
// bottom of a short window and couldn't be scrolled (#1437, Hipple: 28
// removed, a ~717px WebView2 window). An app menu now holds its height to
// the room the window leaves and scrolls inside itself: the wheel over it
// reaches the last entry, which a click brings back. Scrolling the menu
// doesn't close it, nothing moves the page, and the page never scrolls
// sideways. In English and Chinese, wide and narrow, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/m", name: "m", providerName: "A", icon: "generic" },
  { id: "b/m", name: "m", providerName: "B", icon: "generic" },
];
const info = (ids) => ids.map((id) => ({ id, ready: true }));
const removed = Array.from({ length: 30 }, (_, i) => `auto-gone-${String(i + 1).padStart(2, "0")}`);
const all = [
  { id: "auto-m", name: "Model M", members: ["a/m", "b/m"], auto: true, ready: true, memberInfo: info(["a/m", "b/m"]) },
  ...removed.map((id) => ({ id, hidden: true, members: [] })),
];
const words = { en: "30 found groups removed", zh: "已移除 30 个自动创建的组" };

function serve(lang, posts) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    const st = () => ({ models, pools: [], deciders: [], found: true, groups: all });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(st());
    if (url.pathname === "/api/groups/show") {
      posts.push(JSON.parse(r.request().postData() || "{}"));
      return json(st());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const width of [1100, 560]) {
      test(`${engine} ${lang} ${width}px: the last of 30 removed groups is reached in the menu`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-removed-groups-menu.png`) });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=groups");
        const line = page.locator(".rt-ghidden");
        await line.waitFor();
        assert.equal((await line.textContent()).trim(), words[lang]);

        // the reader wheels the page down to the line (the click guard puts
        // back a scroll no reader made)
        const shown = () => line.locator("button").evaluate((x) => { const r = x.getBoundingClientRect(); return r.top >= 60 && r.bottom <= innerHeight - 20; });
        await page.mouse.move(width / 2, 400);
        for (let i = 0; i < 30 && !(await shown()); i++) {
          await page.mouse.wheel(0, 120);
          await page.waitForTimeout(60);
        }
        assert.ok(await shown(), "the line is wheeled into view");
        // a scroll still settling would close the menu as it opens
        await page.evaluate(() => { window.lastScroll = performance.now(); document.addEventListener("scroll", () => { window.lastScroll = performance.now(); }, true); });
        await page.waitForFunction(() => performance.now() - window.lastScroll > 400);
        const at = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop && !e.closest(".row-menu")).map((e) => [e.id || e.className, e.scrollTop]).join(";"));
        const before = await at();

        // clicked where it is, as the reader does: Playwright's own click
        // scrolls it first, and the click guard's putting that back is a
        // scroll that closes the menu
        const bb = await line.locator("button").boundingBox();
        await page.mouse.click(bb.x + bb.width / 2, bb.y + bb.height / 2);
        const menu = page.locator(".row-menu[role=menu]");
        await menu.waitFor();
        assert.equal(await menu.locator(".rm-item").count(), 30);
        const box = await menu.evaluate((m) => { const r = m.getBoundingClientRect(); return { top: r.top, bottom: r.bottom, left: r.left, right: r.right, sh: m.scrollHeight, ch: m.clientHeight }; });
        assert.ok(box.top >= 0 && box.bottom <= 700 && box.left >= 0 && box.right <= width, `the menu stays in the window: ${JSON.stringify(box)}`);
        assert.ok(box.sh > box.ch, `30 entries don't fit, so the menu scrolls: ${JSON.stringify(box)}`);

        // the wheel over the menu scrolls it, not the page, and keeps it open
        const last = menu.locator(".rm-item").last();
        assert.equal((await last.textContent()).trim(), "gone-30");
        await page.mouse.move((box.left + box.right) / 2, (box.top + box.bottom) / 2);
        for (let i = 0; i < 20; i++) {
          await page.mouse.wheel(0, 200);
          await page.waitForTimeout(30);
          if (!(await menu.count())) break;
          const seen = await last.evaluate((b) => { const r = b.getBoundingClientRect(), m = b.closest(".row-menu").getBoundingClientRect(); return r.top >= m.top - 1 && r.bottom <= m.bottom + 1; });
          if (seen) break;
        }
        assert.equal(await menu.count(), 1, "scrolling the menu doesn't close it");
        const spot = await last.evaluate((b) => { const r = b.getBoundingClientRect(), m = b.closest(".row-menu").getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2, inside: r.top >= m.top - 1 && r.bottom <= m.bottom + 1 && r.bottom <= innerHeight }; });
        assert.ok(spot.inside, "the last entry is scrolled into view inside the menu");
        assert.equal(await at(), before, "the page didn't scroll under the menu");
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), true, "no sideways scroll");

        await page.mouse.click(spot.x, spot.y);
        await menu.waitFor({ state: "detached" });
        assert.deepEqual(posts, [{ id: "auto-gone-30" }]);
        assert.equal(await at(), before, "no click moved the page");
        assert.deepEqual(errors, []);
      });
    }
  }
}
