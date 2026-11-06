// Run with Node's test runner and Playwright on the module path; see README.md.
// Scroll to top and Scroll to bottom (scrollends.js) are placed again when
// the page under them changes, not only on the next scroll (Jiabubu on
// Discord): with the capsule in sight, a click that opened a Routing group
// left it over the editor's member rows and held Cancel and Add, and it
// stepped aside only once the page was scrolled. Here, with no scroll after
// it, the capsule must be where the reader's next scroll would put it:
// - a Routing group opened by a click on its card, then closed with Cancel;
// - an Agents page made shorter under it, so its foot comes up and the
//   capsule steps aside (any page's content changing: Providers, Library
//   and Settings have the same capsule).
// Chromium and WebKit, the desktop window and the browser page, wide and
// narrow, en/zh/zh-TW/ja/de.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
// the reporter's rows: "低-ultra · 922K · 跟随分组推理 · 标准速度"
const models = ["低-ultra", "中-pro", "高-max", "快-mini"].map((name, i) => ({ id: `p/m${i}`, name, providerName: "P", icon: "generic", context: 922000 }));
const ids = models.map((m) => m.id);
const groups = {
  models,
  // a pool in the list, as on a real page: an empty one's list is hidden
  // again on every draw, which happened to place the capsule again
  pools: [{ provider: "workbuddy-ai", name: "WorkBuddy AI", icon: "generic", kind: "account", who: ["a", "b", "c"], routing: "order" }],
  groups: Array.from({ length: 30 }, (_, i) => ({ id: `g${i}`, name: `group ${i}`, routing: "order", members: ids, ready: true, memberInfo: ids.map((id) => ({ id, ready: true })), offers: [], shared: [] })),
};
const AGENTS = Array.from({ length: 40 }, (_, i) => ({
  id: "a" + i, name: "Agent " + i, path: "/t/a" + i + ".json", icon: "generic", wired: true,
  fields: [{ key: "model", label: "model", value: "m", options: [{ value: "m", label: "M", ref: "a/m" }] }],
}));

function serve(lang, web) {
  const state = { agents: AGENTS, profiles: [], settings: { lang, theme: "light" } };
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

// a few frames and a little more: the capsule is placed a frame after what
// moved it
const settle = (page) => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(() => setTimeout(r, 300)))));
const capsule = (page, view) => page.evaluate((view) => {
  const b = document.querySelector(".view-ends"), r = b.getBoundingClientRect(), v = document.querySelector(view);
  const box = (e) => { const x = e.getBoundingClientRect(); return { text: e.textContent.trim(), top: x.top, bottom: x.bottom, left: x.left, right: x.right }; };
  const bar = document.querySelector(".rt-gedit > .bar");
  const held = bar && Math.abs(bar.getBoundingClientRect().bottom - v.getBoundingClientRect().bottom) <= 1;
  return {
    on: b.classList.contains("on"), top: r.top, bottom: r.bottom, left: r.left, right: r.right,
    scrollTop: v.scrollTop,
    bar: held ? box(bar) : null,
    buttons: [...v.querySelectorAll(".rt-gedit button")].filter((x) => x.offsetParent).map(box),
  };
}, view);
// the reader's own scroll, one notch down and back: the capsule as a scroll
// places it
async function afterScroll(page, view) {
  await page.mouse.wheel(0, 40);
  await settle(page);
  await page.mouse.wheel(0, -40);
  await settle(page);
  return capsule(page, view);
}
function same(at, got, want) {
  assert.equal(got.on, want.on, `${at}: the capsule ${want.on ? "in sight" : "stepped aside"} as a scroll has it, with no scroll: ${JSON.stringify([got, want].map((x) => [x.on, x.top, x.bottom, x.scrollTop]))}`);
  if (want.on) assert(Math.abs(got.bottom - want.bottom) <= 2 && Math.abs(got.right - want.right) <= 2, `${at}: the capsule where a scroll puts it, with no scroll: ${got.top}-${got.bottom}, a scroll ${want.top}-${want.bottom}`);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: the capsule is placed again when the page under it changes, with no scroll`, { timeout: 300000 }, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const web of [false, true]) {
        for (const width of web ? [1100, 440, 360] : [1100, 700, 560]) {
          const at = `${web ? "web" : "desktop"} ${width}px`;
          const page = await browser.newPage({ viewport: { width, height: 640 }, reducedMotion: "reduce" });
          page.setDefaultTimeout(8000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => { if (!/^ResizeObserver loop/.test(e.message)) errors.push(e.message); });
          await page.route("**/*", serve(lang, web));

          // Routing groups: a screen down, the capsule in sight (with the
          // page to themselves, the groups need more rows to be a screen
          // and more down)
          await page.goto("http://magpie.test/?view=groups");
          await page.locator(".rt-group").nth(15).waitFor();
          const v = "#view-groups";
          await page.mouse.move(width / 2, 300);
          const down = await page.evaluate(() => {
            const v = document.querySelector("#view-groups"), g = document.querySelectorAll(".rt-group")[20];
            return Math.round(g.getBoundingClientRect().top - v.getBoundingClientRect().top - v.clientHeight / 2);
          });
          await page.mouse.wheel(0, down);
          await page.waitForFunction(() => document.querySelector(".view-ends.on"));
          await settle(page);
          // a click on the card lowest in sight opens its editor in place,
          // running on below the window, its Cancel and Add held at the foot
          const n = await page.evaluate(() => {
            let k = 0;
            document.querySelectorAll(".rt-group").forEach((g, i) => { if (g.getBoundingClientRect().bottom < innerHeight - 60) k = i; });
            return k;
          });
          await page.locator(".rt-group").nth(n).click({ position: { x: 40, y: 12 } });
          await page.locator(".rt-gedit .fbrow").first().waitFor();
          await settle(page);
          const opened = await capsule(page, v);
          if (opened.on && opened.bar) assert(opened.bottom <= opened.bar.top + 1, `${at}: opened, the capsule above the held Cancel and Add, with no scroll: ${opened.top}-${opened.bottom}, bar from ${opened.bar.top}`);
          // the reporter's goal: nothing of the editor's under it, Remove,
          // Up and Down on the member rows, Cancel and Add on the bar
          if (opened.on) for (const b of opened.buttons) {
            const over = b.left < opened.right && b.right > opened.left && b.top < opened.bottom && b.bottom > opened.top;
            assert(!over, `${at}: opened, the capsule over the editor's ${b.text}, with no scroll: ${JSON.stringify([b, opened.top, opened.bottom, opened.left, opened.right])}`);
          }
          same(`${at} opened`, opened, await afterScroll(page, v));

          // Cancel: the editor goes, and the capsule with the bar it rose over
          await page.locator(".rt-gedit > .bar button.text:not(.danger)").first().click();
          await page.locator(".rt-gedit").waitFor({ state: "detached" });
          await settle(page);
          const closed = await capsule(page, v);
          same(`${at} closed`, closed, await afterScroll(page, v));

          // Agents: the capsule in sight a little above the foot, then a
          // row's worth of the list taken out, so the foot comes up under it:
          // over the corner it steps aside there, as a scroll would have it.
          // What goes is a block at the list's end, made for the test: the
          // foot must stay below the window, or the browser moves the page
          // up itself, which is a scroll
          await page.goto("http://magpie.test/?view=agents");
          await page.locator("#view-agents .ag-lead").first().waitFor();
          await page.waitForFunction(() => { const v = document.getElementById("view-agents"); return v.scrollHeight > v.clientHeight * 3; });
          await page.evaluate(() => {
            const d = document.createElement("div");
            d.id = "goes";
            d.style.height = "400px";
            document.getElementById("agents").append(d);
          });
          await page.mouse.move(width / 2, 300);
          await page.mouse.wheel(0, await page.evaluate(() => { const v = document.getElementById("view-agents"); return v.scrollHeight - v.clientHeight - v.scrollTop - 430; }));
          await page.waitForFunction(() => document.querySelector(".view-ends.on"));
          await settle(page);
          const left = await page.evaluate(() => {
            const v = document.getElementById("view-agents");
            document.getElementById("goes").remove();
            return v.scrollHeight - v.clientHeight - v.scrollTop;
          });
          assert(left > 0 && left < 60, `${at}: the Agents page's foot just under the window: ${left}`);
          await settle(page);
          const shorter = await capsule(page, "#view-agents");
          same(`${at} agents shorter`, shorter, await afterScroll(page, "#view-agents"));

          assert.deepEqual(errors, [], `${at}: no page error`);
          await page.close();
        }
      }
    });
  }
}
