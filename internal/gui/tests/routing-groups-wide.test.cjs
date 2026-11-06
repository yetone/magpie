// Run with Node's test runner and Playwright on the module path; see README.md.
// Routing groups with long names and many members (noahhrreion on Discord):
// the Routing page went wider than the window, with a scrollbar under it and
// each row's routing, Edit and switch far off to the right. The groups' list
// clips its overflow (overflow: clip, so the editor's Save bar can stick), and
// a box that clips is not a scroll container, so the section's grid gave its
// one column the whole width of the longest member line. The column is held
// to the section's width: the member lines are cut with an ellipsis inside
// their row (wrapped, on a phone) and nothing on the page is wider than the window. Chromium and
// WebKit, the desktop window down to its least width (560px, windowMinW)
// and the browser page down to 360px, en/zh/zh-TW/ja/de. view-no-sideways
// checks every page with no groups at all, so it never saw this.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(path.join(__dirname, "../assets"));
const long = "a-really-long-model-name-that-keeps-on-going-flash-preview-2026";
const models = Array.from({ length: 12 }, (_, i) => ({ id: `p${i}/${long}-${i}`, name: `${long}-${i}`, providerName: "Provider With A Long Name " + i, icon: "generic" }));
const info = models.map((m) => ({ id: m.id, ready: true }));
const base = { members: models.map((m) => m.id), ready: true, memberInfo: info, offers: [], shared: [] };
const groups = { models, pools: [], groups: [
  { ...base, id: "g1", name: "A group with a rather long name that goes on " + long, routing: "smart" },
  { ...base, id: "g2", name: "weights", routing: "weight", weights: Object.fromEntries(models.map((m, i) => [m.id, i + 1])) },
  { ...base, id: "g3", name: "turns", routing: "turn", rules: [{ use: models[0].id, tokens: 100000 }] },
] };

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
  test(`${engine}: long routing groups leave the Routing page the window's width`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const web of [false, true]) {
      for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
        const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, web));
        await page.goto("http://magpie.test/?view=groups");
        await page.locator(".rt-group").nth(2).waitFor();
        const settle = () => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(() => setTimeout(r, 100)))));
        for (const width of web ? [1440, 1100, 700, 440, 360] : [1440, 1100, 900, 700, 560]) {
          const at = `${web ? "web" : "desktop"} ${lang} ${width}px`;
          await page.setViewportSize({ width, height: 900 });
          await settle();
          const m = await page.evaluate(() => {
            const v = document.querySelector("#view-groups"), de = document.documentElement;
            const rows = [...document.querySelectorAll(".rt-groups > .rt-group")].map((g) => {
              const b = g.getBoundingClientRect(), list = g.parentElement.getBoundingClientRect();
              const kids = [...g.querySelectorAll(".mem, .tags, .switch, button")].map((e) => e.getBoundingClientRect()).filter((k) => k.width > 0);
              return { right: b.right, listRight: list.right, out: kids.filter((k) => k.right > b.right + 1).length };
            });
            const mem = document.querySelector(".rt-group .mem");
            // cut with an ellipsis, or (the browser page on a phone) wrapped onto more lines
            const cs = mem && getComputedStyle(mem), lines = mem && mem.getBoundingClientRect().height / (parseFloat(cs.lineHeight) || parseFloat(cs.fontSize) * 1.3);
            return { view: [v.scrollWidth, v.clientWidth], doc: [de.scrollWidth, de.clientWidth], rows, cut: mem && (mem.scrollWidth > mem.clientWidth || lines > 1.5) };
          });
          assert(m.view[0] <= m.view[1] + 1, `${at}: the Routing view is no wider than the window: scrollWidth ${m.view[0]} > ${m.view[1]}`);
          assert(m.doc[0] <= m.doc[1] + 1, `${at}: the document is no wider than the window: ${m.doc}`);
          assert.equal(m.rows.length, 3, `${at}: every group drawn`);
          for (const r of m.rows) {
            assert(r.right <= r.listRight + 1 && r.right <= width + 1, `${at}: a group row inside its list and the window: ${JSON.stringify(r)}`);
            assert.equal(r.out, 0, `${at}: the row's members, routing, Edit and switch inside it`);
          }
          assert(m.cut, `${at}: the long member line is cut or wrapped inside its row`);
        }
        assert.deepEqual(errors, [], `${web ? "web" : "desktop"} ${lang}: no page error`);
        await page.close();
      }
    }
  });
}
