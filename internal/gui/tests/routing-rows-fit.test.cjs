// Run with Node's test runner and Playwright on the module path; see README.md.
// Every row on the Routing page fits the window (young02036 on Discord: a
// group's long line pushed the page wider than the window, with a scrollbar
// under it). routing-groups-wide holds the member lines; this holds the rest
// of each row as the window is made narrower and wider again, a group's
// editor open and shut between:
// - a group of many tags (German's long ones, or any at 700px) had pushed its
//   Edit and switch out past the row's edge, where the list cut them off;
// - a provider's Routing and Stays segments (Several accounts or keys) ran
//   out past their row in the desktop window;
// - in WebKit the live stage's "No request yet" kept the width it had in a
//   wider window once it was out of sight (content-visibility remembered
//   it), and held the whole page that wide, a scrollbar under it.
// Members are "WorkBuddy AI · Hy3 · 免费", as the reporter's were. Chromium and
// WebKit, the desktop window down to 560px and the browser page to 360px,
// en/zh/zh-TW/ja/de.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const long = "a-really-long-model-name-that-keeps-on-going-flash-preview-2026";
const models = Array.from({ length: 6 }, (_, i) => ({ id: `p${i}/${long}-${i}`, name: `${long}-${i}`, providerName: "Provider With A Long Name " + i, icon: "generic" }));
const ids = models.map((m) => m.id);
// members no model of the list names: their line is the group's own words
const hy = ["workbuddy-ai/hy3", "workbuddy-ai/hy3-turbo"];
const info = [...ids.map((id) => ({ id, ready: true })), ...hy.map((id) => ({ id, ready: true, name: "WorkBuddy AI", model: "Hy3", provider: "workbuddy-ai", free: true }))];
const base = { members: [...hy, ...ids], ready: true, memberInfo: info, offers: [], shared: [] };
const groups = {
  models,
  pools: [{ provider: "workbuddy-ai", name: "WorkBuddy AI", icon: "generic", kind: "account", who: ["a", "b", "c"], routing: "order" }],
  groups: [
    { ...base, id: "g1", name: "smart", routing: "smart" },
    { ...base, id: "g2", name: "manual", routing: "manual", pick: hy[0] },
    // every tag a row can carry
    { ...base, id: "g3", name: "order", routing: "order", affinity: "session", sink: true, unlists: true, firstToken: 30, rules: [{ use: ids[0], tokens: 100000 }, { use: ids[1], tokens: 5 }], off: [ids[2]] },
  ],
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

// what sticks out: the page past the window, and a row's controls past the row
const measure = (page) => page.evaluate(() => {
  const v = document.querySelector("#view-routing"), de = document.documentElement;
  const out = [];
  for (const row of document.querySelectorAll(".rt-groups > *, .rt-pools > *")) {
    const rb = row.getBoundingClientRect();
    for (const k of row.querySelectorAll(".tags, .tag, button, .segs, .ctl")) {
      const b = k.getBoundingClientRect();
      if (b.width && b.right > rb.right + 1) out.push(`${row.className} > ${k.className || k.tagName} "${k.textContent.slice(0, 24)}" ${Math.round(b.right - rb.right)}px past`);
    }
  }
  // what holds the page wide, for the message
  const wide = [...v.querySelectorAll("*")].filter((e) => e.getBoundingClientRect().right > innerWidth + 1 && ![...e.children].some((c) => c.getBoundingClientRect().right > innerWidth + 1)).slice(0, 4).map((e) => `${e.tagName}.${e.className} ${Math.round(e.getBoundingClientRect().width)}px`);
  return { view: [v.scrollWidth, v.clientWidth], doc: [de.scrollWidth, de.clientWidth], out, wide };
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: every Routing row fits the window, an editor open or not`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const web of [false, true]) {
        const widths = web ? [1100, 700, 440, 360] : [1100, 900, 700, 560];
        const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, web));
        await page.goto("http://magpie.test/?view=routing");
        await page.locator(".rt-group").nth(2).waitFor();
        const memLine = await page.locator(".rt-group[data-id=g1] .mem").first().textContent();
        assert(memLine.includes("WorkBuddy AI · Hy3"), `the reporter's member line: ${memLine}`);
        const settle = () => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(() => setTimeout(r, 150)))));
        // the list, then each group's editor in turn, on one page: the
        // window narrowed and widened again under each
        for (const open of [null, "g1", "g2", "g3"]) {
          if (open) {
            await page.keyboard.press("Escape");
            await page.locator(`.rt-group[data-id=${open}] .main`).click({ position: { x: 4, y: 4 } });
            await page.locator(".rt-gedit").waitFor();
          }
          for (const width of [...widths, 1440]) {
            const at = `${web ? "web" : "desktop"} ${open ? open + "'s editor" : "list"} ${width}px`;
            await page.setViewportSize({ width, height: 900 });
            await settle();
            const m = await measure(page);
            assert(m.view[0] <= m.view[1] + 1, `${at}: the Routing view no wider than the window: ${m.view} ${m.wide.join(", ")}`);
            assert(m.doc[0] <= m.doc[1] + 1, `${at}: the document no wider than the window: ${m.doc} ${m.wide.join(", ")}`);
            assert.deepEqual(m.out, [], `${at}: each row's tags, Edit, switch and segments inside it`);
          }
        }
        assert.deepEqual(errors, [], `${web ? "web" : "desktop"}: no page error`);
        await page.close();
      }
    });
  }
}
