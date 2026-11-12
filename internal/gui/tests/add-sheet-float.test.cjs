// Run with Node's test runner and Playwright on the module path; see README.md.
// The add sheet's tools follow the reader down (the owner, 11-12: 向上滑动，
// 然后这些区域不在可视区域的时候，这些区域应该自动浮现在Header处). Beside
// the sidebar, the sheet's search, Custom provider and Import… scrolled out
// of sight show in the title bar's empty right, left of its buttons, where
// Settings' search is; scrolled back they are in their row again. They are
// the same nodes, so there is one search: what was typed and the focus go
// with it. A narrow window has no room in its bar, so the sheet's head is
// held at the top of the page while the sheet is under it. The page is
// scrolled with the wheel, as a reader does (the app puts back any other
// scroll), and nothing scrolls on a click. Every language, both engines,
// wide and narrow; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const preset = (id, name, kind) => ({ id, name, icon: "openai", kind, chat: `https://api.${id}.example.com/v1`, added: false });
// enough rows that the sheet runs far past the window
const presets = [
  ...Array.from({ length: 90 }, (_, i) => preset("vendor-" + i, "Vendor " + i, "vendor")),
  ...Array.from({ length: 30 }, (_, i) => preset("relay-" + i, "Relay " + i, "relay")),
];
const providers = [{ id: "openai", name: "OpenAI", icon: "openai", preset: "", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }];

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers, presets, excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// a wheel step over the view, landed before the next look
async function wheel(page, dy) {
  const was = await page.evaluate(() => document.getElementById("view-providers").scrollTop);
  const v = await page.locator("#view-providers").boundingBox();
  await page.mouse.move(v.x + v.width / 2, v.y + v.height / 2);
  await page.mouse.wheel(0, dy);
  return page.waitForFunction((was) => document.getElementById("view-providers").scrollTop !== was, was, { timeout: 1000 }).then(() => true, () => false);
}

// where the tools are: in the title bar, or in the sheet's head
const where = (page) => page.evaluate(() => {
  const r = (e) => e.getBoundingClientRect();
  const head = document.querySelector("#addSheet > .row-head"), find = document.querySelector("#addSheet .find");
  const g = find?.parentElement, view = document.getElementById("view-providers"), bar = r(document.querySelector(".top"));
  const f = find && r(find), hit = f && document.elementFromPoint(f.left + f.width / 2, f.top + f.height / 2);
  return {
    // in the bar: drawn over the title bar, wholly
    afloat: !!f && f.top >= bar.top - 1 && f.bottom <= bar.bottom + 1, finds: document.querySelectorAll("#addSheet .find").length,
    tools: g && r(g), find: f, head: head && r(head), view: r(view), top: r(document.querySelector(".top")),
    actions: r(document.querySelector(".actions")), crumb: r(document.getElementById("crumb")),
    clear: !!hit && (hit === find || find.contains(hit)), scroll: view.scrollTop,
    focused: document.activeElement === find, value: find?.value,
  };
});

const shown = (page) => page.locator("#addSheet .tile .n").evaluateAll((ns) => ns.filter((n) => n.getClientRects().length).map((n) => n.textContent));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the add sheet's tools follow the reader down", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 420]) {
        await t.test(`${lang} ${width}px`, async () => {
          const context = await browser.newContext({ viewport: { width, height: 700 } });
          const page = await context.newPage();
          page.setDefaultTimeout(8000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang));
          await page.goto("http://magpie.test/?view=providers");
          await page.locator("#addProvider").click();
          const sheet = page.locator("#addSheet");
          await sheet.locator(".tile").first().waitFor();
          await sheet.evaluate((el) => Promise.all(el.getAnimations({ subtree: true }).map((a) => a.finished)));
          const side = await page.evaluate(() => document.body.classList.contains("side-nav"));
          assert.equal(side, width >= 1024, "the layout");

          // in sight: in their row, not in the bar
          let w = await where(page);
          assert(!w.afloat, "in their row while in sight");
          assert(w.find.top >= w.head.top - 1 && w.find.bottom <= w.head.bottom + 1, "in the sheet's head: " + JSON.stringify([w.find, w.head]));

          // a little down, the head still in sight: still in their row
          await wheel(page, 40);
          w = await where(page);
          if (w.head.bottom > w.view.top + 2) assert(!w.afloat, "not in the bar while the row shows");

          // down with the wheel till the head's row is out of sight
          for (let i = 0; i < 60; i++) {
            const h = await page.evaluate(() => {
              const s = document.querySelector("#addSheet"), v = document.getElementById("view-providers");
              return s.getBoundingClientRect().top + 60 - v.getBoundingClientRect().top;
            });
            if (h < 0) break;
            if (!(await wheel(page, 100))) break;
          }
          await page.waitForTimeout(250);
          w = await where(page);
          assert.equal(w.finds, 1, "one search");
          if (side) {
            assert(w.afloat, "in the bar once out of sight");
            assert(w.find.top >= w.top.top - 1 && w.find.bottom <= w.top.bottom + 1, "in the title bar: " + JSON.stringify([w.find, w.top]));
            assert(w.tools.right <= w.actions.left + 0.5, "left of the bar's buttons: " + JSON.stringify([w.tools, w.actions]));
            assert(w.tools.left >= w.crumb.left, "right of where the reader is");
            // Custom provider and Import… with it (the two with a title);
            // Close stays in the sheet
            const bs = await page.locator("#addSheet .row-head button[title]").evaluateAll((es) => es.map((e) => e.getBoundingClientRect().toJSON()));
            assert.equal(bs.length, 2);
            for (const b of bs) assert(b.top >= w.top.top - 1 && b.bottom <= w.top.bottom + 1, "Custom provider and Import… in the bar too: " + JSON.stringify(b));
          } else {
            assert(!w.afloat, "no room in a narrow bar");
            assert(Math.abs(w.head.top - w.view.top) <= 4, "the head held at the page's top: " + JSON.stringify([w.head, w.view]));
          }
          assert(w.clear, "the search is clear to press");

          // a click on it moves nothing, and typing there finds
          const before = w.scroll;
          await sheet.locator(".find").click();
          await page.waitForTimeout(150);
          w = await where(page);
          assert.equal(w.scroll, before, "nothing scrolled on the click");
          assert(w.focused, "the search has the focus");
          await page.keyboard.type("Relay 2");
          await page.waitForTimeout(250);
          const names = await shown(page);
          assert(names.length > 0 && names.every((n) => n.includes("Relay 2")), "the list found: " + names.join(", "));
          w = await where(page);
          assert.equal(w.finds, 1, "still one search");
          assert(w.focused && w.value === "Relay 2", "the focus and the words kept: " + JSON.stringify([w.focused, w.value]));

          // back up: in their row again, the words and the focus with them
          for (let i = 0; i < 80 && (await page.evaluate(() => document.getElementById("view-providers").scrollTop)) > 0; i++) {
            if (!(await wheel(page, -200))) break;
          }
          await page.waitForTimeout(250);
          w = await where(page);
          assert(!w.afloat, "in their row once scrolled back");
          assert(w.find.top >= w.head.top - 1 && w.find.bottom <= w.head.bottom + 1, "back in the sheet's head");
          assert(w.focused && w.value === "Relay 2", "the words and focus kept going back");
          assert.deepEqual(errors, []);
          await context.close();
        });
      }
    }
  });
}
