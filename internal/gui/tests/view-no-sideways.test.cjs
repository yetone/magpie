// Run with Node's test runner and Playwright on the module path; see README.md.
// #1236 (pineog): on macOS 15 with scroll bars shown (a mouse, or "Always"),
// Providers, Library and Plugins had a horizontal scrollbar that moved the
// page 12-16px sideways, at every text size and window width. Each of those
// pages has a strip that reaches over the view's side padding (Library's
// and Plugins' sticky tabs, Providers' Add bar) by a negative margin, so
// its box ran 12-16px past the content's end edge. macOS 15's WebKit
// (before WebKit 290159@main / bug 218579, which shipped with macOS 26)
// takes a flex scroller's scrollable width as the furthest in-flow child's
// right edge plus max(0, its margin-end), then adds the padding-end: the
// negative margin is left out and the page is that much wider than the view.
// Chromium and today's WebKit count the margin, so neither scrolls; the
// first check here is that older WebKit's sum, worked out from the layout,
// for every page, and fails on the old strips. The second is the engine's
// own scrollWidth, with the scrollbars shown. The strips' boxes now keep
// to the content box and their ::before reaches over the padding.
// Every page, en/zh/ja/de, the desktop window (body.mac) at 560px (its least
// width, windowMinW) and 1000px and the browser page at 360/440/1000px, in
// Chromium and WebKit. No backend: the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/a";
const lib = {
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [{ id: "codex", name: "Codex", icon: "", skills: `${HOME}/.codex/skills`, mcp: `${HOME}/.codex/mcp.json` }],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], servers: [],
};
const rtk = { path: "", version: "", agents: [{ id: "claude", name: "Claude Code", icon: "", on: false }, { id: "codex", name: "Codex", icon: "", on: false }, { id: "pi", name: "Pi", icon: "", on: false }] };

function server(lang, web) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (d) => route.fulfill({ json: d });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/library") return json(lib);
    if (url.pathname === "/api/library/rtk") return json(rtk);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// how far the shown view runs past its own width: in macOS 15's WebKit
// (in-flow children's right edge + max(0, margin-end), then the padding-end)
// and in the engine running the test
const sideways = () => {
  const v = [...document.querySelectorAll("main.view")].find((m) => !m.hidden && m.getClientRects().length);
  if (!v) return null;
  const cs = getComputedStyle(v), box = v.getBoundingClientRect();
  const left = box.left + parseFloat(cs.borderLeftWidth) - v.scrollLeft;
  let right = 0, by = "";
  for (const c of v.children) {
    const ccs = getComputedStyle(c);
    if (ccs.position === "absolute" || ccs.position === "fixed" || !c.getClientRects().length) continue;
    const r = c.getBoundingClientRect().right - left + Math.max(0, parseFloat(ccs.marginRight));
    if (r > right) { right = r; by = c.tagName.toLowerCase() + (c.className ? "." + String(c.className).trim().split(/\s+/).join(".") : ""); }
  }
  return {
    id: v.id, flex: /flex|grid/.test(cs.display),
    old: Math.round((right + parseFloat(cs.paddingRight) - v.clientWidth) * 10) / 10, by,
    now: v.scrollWidth - v.clientWidth,
  };
};

const VIEWS = ["agents", "providers", "gateway", "routing", "usage", "sessions", "library", "plugins", "settings"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": no page scrolls sideways (#1236)", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    // the scrollbars drawn and taking their room, as with a mouse on a Mac
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium", ignoreDefaultArgs: ["--hide-scrollbars"] }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh", "ja", "de"]) {
      for (const web of [false, true]) {
        for (const width of web ? [360, 440, 1000] : [560, 1000]) {
          await t.test(`${lang} ${web ? "browser" : "window"} ${width}px`, async () => {
            const ctx = await browser.newContext({ viewport: { width, height: 720 } });
            await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "rtk"); } catch {} });
            const page = await ctx.newPage();
            page.setDefaultTimeout(5000);
            page.on("pageerror", (e) => errors.push(e.message));
            await page.route("**/*", server(lang, web));
            await page.goto("http://magpie.test/");
            await page.locator("#view-agents").waitFor();
            const bad = [];
            for (const v of VIEWS) {
              if (v === "settings") await page.evaluate(() => document.querySelector("#prefs").click());
              else await page.evaluate((v) => document.querySelector(`#nav [data-view="${v}"]`).click(), v);
              await page.waitForFunction((v) => { const m = document.getElementById("view-" + v); return m && !m.hidden && m.children.length; }, v);
              await page.waitForTimeout(250);
              const s = await page.evaluate(sideways);
              assert.equal(s && s.id, "view-" + v, "the page is shown: " + v);
              if (s.flex && s.old > 0.5) bad.push(`${v}: ${s.old}px past the view in macOS 15's WebKit, by ${s.by}`);
              if (s.now > 0) bad.push(`${v}: scrolls ${s.now}px sideways here`);
            }
            await ctx.close();
            assert.deepEqual(bad, []);
          });
        }
      }
    }
  });
}
