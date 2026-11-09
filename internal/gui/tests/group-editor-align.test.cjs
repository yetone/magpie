// Run with Node's test runner and Playwright on the module path; see README.md.
// The group editor's labels sat at different heights beside their controls
// (the owner: "左边的文字label与右边控件的对齐方式都不一致"). Models' and
// Patterns' empty lists still took a grid row, so the add button and the
// pattern input began 6-10px under their labels; Slow to start's and
// Context's hints sat 12px under their choices where every other row's sit
// 4px. Each label's first line is level with its control's first line, and
// each hint 4px under what it is about, new or with models and patterns in,
// in every language. Wide only: narrow, a label sits above its control.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
// the rows a group's editor folds under Advanced, opened
const openAdvanced = async (page) => {
  const d = page.locator(".rt-gedit .rt-gadv");
  await d.waitFor({ state: "attached" });
  if (!(await d.evaluate((e) => e.open))) await d.locator(":scope > summary").click();
};

const assets = path.resolve(__dirname, "../assets");
const members = ["a/m1", "a/m2"];
const groups = (withOne) => ({
  models: members.map((id) => ({ id, name: id.slice(2), providerName: "A", icon: "generic" })),
  pools: [],
  groups: withOne ? [{ id: "g", name: "Glim", members, match: ["a/*"], fast: [], off: [], routing: "", ready: true, memberInfo: members.map((id) => ({ id, ready: true })) }] : [],
});
const prov = { icon: "generic", catalog: "", models: [{ id: "m1", name: "m1", on: true }, { id: "m2", name: "m2", on: true }], agents: [], fallback: [], headers: {}, routing: "order", affinity: "", ready: true, id: "a", name: "A", chat: "http://127.0.0.1:1/v1", responses: "", anthropic: "", keyList: [] };

function serve(lang, withOne) {
  const state = { agents: [{ id: "pi", name: "Pi", path: "/test/pi", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  const providers = { providers: [prov], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups(withOne));
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

// per row: the label's first line's middle less the control's (the first
// input or button it shows, or else its first line of text), and the space
// above each hint that follows something shown
const measure = () => {
  const ed = document.querySelector(".rt-gedit");
  const shown = (n) => !n.closest("[hidden]") && getComputedStyle(n).display !== "none";
  const firstLine = (root) => {
    const w = document.createTreeWalker(root, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT);
    for (let n = w.currentNode; n; n = w.nextNode()) {
      if (n.nodeType === 1) {
        if (!shown(n)) continue;
        if (/^(INPUT|BUTTON)$/.test(n.tagName)) {
          const r = n.getBoundingClientRect();
          if (r.height) return r.top + r.height / 2;
        }
      } else if (n.textContent.trim() && !n.parentElement.closest("button") && shown(n.parentElement)) {
        const rg = document.createRange();
        rg.selectNodeContents(n);
        const r = rg.getClientRects()[0];
        if (r) return r.top + r.height / 2;
      }
    }
    return null;
  };
  return [...ed.querySelectorAll(":scope > label, :scope > .rt-gadv > .inner > label")].filter(shown).map((l) => {
    const ctl = l.nextElementSibling, a = firstLine(l), c = firstLine(ctl);
    const gaps = [...ctl.querySelectorAll(".hint")].filter(shown).flatMap((h) => {
      let p = h.previousElementSibling;
      while (p && !shown(p)) p = p.previousElementSibling;
      if (!p || p.classList.contains("hint")) return [];
      return [Math.round(h.getBoundingClientRect().top - p.getBoundingClientRect().bottom)];
    });
    return { label: l.textContent, off: a == null || c == null ? null : Math.round((a - c) * 10) / 10, gaps };
  });
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const withOne of [false, true]) {
      const launch = () => engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" });
      test(`${engine} ${lang}: a ${withOne ? "group's" : "new group's"} labels are level with their controls`, async (t) => {
        const browser = await launch();
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width: 1100, height: 1000 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, withOne));
        await page.goto("http://magpie.test/?view=groups");
        const w = await page.evaluate((lang) => Object.fromEntries(["New group", "Edit"].map((k) => [k, lang === "en" ? k : I18N[lang][k]])), lang);
        if (withOne) await page.locator(".rt-group", { hasText: "Glim" }).locator("button", { hasText: w.Edit }).click();
        else await page.locator("button", { hasText: w["New group"] }).first().click();
        await page.locator(".rt-gedit").waitFor();
        await openAdvanced(page); // its rows are measured too
        if (withOne) await page.locator(".rt-gedit .rt-pats .fbrow").first().waitFor();
        const rows = await page.evaluate(measure);
        assert.ok(rows.length >= 10, `the editor's rows: ${JSON.stringify(rows)}`);
        const off = rows.filter((r) => r.off == null || Math.abs(r.off) > 2);
        assert.deepEqual(off, [], "each label is level with its control's first line");
        const far = rows.filter((r) => r.gaps.some((g) => g > 4));
        assert.deepEqual(far, [], "each hint sits 4px under what it is about");
        assert.deepEqual(errors, []);
      });
    }
  }
}
