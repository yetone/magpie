// Run with Node's test runner and Playwright on the module path; see README.md.
// Library's sidebar list draws its One agent tabs with the agent's own
// logo, the one its row on Agents has (icon()), not a glyph: Claude
// plugins with Claude Code's colour mark, Pi packages with Pi's. Each sits
// in the glyphs' 14px column, level with its name; Claude's keeps its
// colours, and Pi's, a mask, rests in --muted and takes the torch
// (navlamp.js) as a glyph does. Both engines, light and dark, English and
// Chinese. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/aimer";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const lib = {
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("pi", "Pi")],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], servers: [],
};

function serve(lang, theme) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:${JSON.stringify(theme)},web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { url: "http://127.0.0.1:3425", running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/library") return json(lib);
    if (url.pathname === "/api/plugins") return json({ bun: true, bunVersion: "1.3.0", plugins: [], movable: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// a colour as sRGB, read through a canvas so oklab and color-mix compare
const rgbOf = (page, sel, prop) => page.$eval(sel, (e, prop) => {
  const c = document.createElement("canvas").getContext("2d", { willReadFrequently: true });
  const rgb = (v) => { c.clearRect(0, 0, 1, 1); c.fillStyle = "#010203"; c.fillStyle = v; c.fillRect(0, 0, 1, 1); return [...c.getImageData(0, 0, 1, 1).data.slice(0, 3)]; };
  return { v: rgb(getComputedStyle(e)[prop]), muted: rgb(getComputedStyle(document.documentElement).getPropertyValue("--muted").trim()) };
}, prop);
const far = (a, b) => Math.hypot(a[0] - b[0], a[1] - b[1], a[2] - b[2]);
// read until ok() holds, or give the last read back after 10s: a transition
// under load can outlast any fixed wait, so the test waits on the state
const settle = async (read, ok) => {
  const end = Date.now() + 10000;
  for (;;) { const v = await read(); if (ok(v) || Date.now() > end) return v; await new Promise((r) => setTimeout(r, 50)); }
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: Library's One agent rows show Claude Code's and Pi's own logos`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) for (const theme of ["light", "dark"]) await t.test(`${lang} ${theme}`, async () => {
      const context = await browser.newContext({ viewport: { width: 1100, height: 900 }, colorScheme: theme });
      t.after(() => context.close());
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, theme));
      await page.goto("http://magpie.test/");
      await page.waitForSelector("#nav button.on");
      await page.locator('#nav button[data-view="library"]').click();
      await page.waitForFunction(() => window.NAV_SUB_IC && document.querySelectorAll('.nav-sub[data-view="library"]:not(.leaving) .nav-s').length >= 10);
      await page.waitForFunction(() => [...document.querySelectorAll(".nav-sub, .nav-sub *")].every((e) => e.getAnimations().every((a) => a.playState !== "running")));

      const rows = await page.evaluate(() => {
        const name = (seg) => document.querySelector(`.lib-tabs [data-seg="${seg}"]`)?.textContent.trim().split(" · ")[0];
        return { plugins: name("plugins"), pi: name("pi"), rows: [...document.querySelectorAll('.nav-sub[data-view="library"]:not(.leaving) .nav-s')].map((s) => {
          const ic = s.querySelector(":scope > svg, :scope > .nav-s-ic > .ic"), b = ic?.getBoundingClientRect(), nb = s.querySelector(":scope > span").getBoundingClientRect();
          return { text: s.querySelector(":scope > span").textContent, spans: s.querySelectorAll(":scope > span").length, logo: ic?.dataset.icon || "", glyph: ic?.tagName === "svg",
            img: !!ic?.querySelector("img"), mask: !!ic?.querySelector(".mask"), left: b?.left, w: b?.width, h: b?.height, mid: b && b.top + b.height / 2, nmid: nb.top + nb.height / 2,
            mw: ic?.querySelector(".mask")?.getBoundingClientRect().width, mh: ic?.querySelector(".mask")?.getBoundingClientRect().height };
        }) };
      });
      const claude = rows.rows.find((r) => r.text === rows.plugins), pi = rows.rows.find((r) => r.text === rows.pi);
      assert.ok(claude && pi, `both rows are listed: ${JSON.stringify(rows)}`);
      assert.equal(claude.logo, "claudecode-color", "Claude plugins shows Claude Code's logo");
      assert.ok(claude.mask && !claude.img && !claude.glyph, "Claude Code's logo is a mask in one ink, not its colour picture");
      assert.equal(pi.logo, "pi", "Pi packages shows Pi's logo");
      assert.ok(pi.mask && !pi.img && !pi.glyph, "Pi's logo is its mask");
      const glyphs = rows.rows.filter((r) => r.glyph);
      assert.ok(glyphs.length >= 8, "the other rows keep their glyphs");
      for (const r of [claude, pi]) {
        assert.equal(Math.round(r.w), 14, `${r.text}'s logo is 14px wide`);
        assert.equal(Math.round(r.h), 14, `${r.text}'s logo is 14px high`);
        assert.ok(Math.abs(r.left - glyphs[0].left) < 0.5, `${r.text}'s logo stands in the glyphs' column`);
        assert.ok(Math.abs(r.mid - r.nmid) <= 1.5, `${r.text}'s logo is level with its name`);
        assert.equal(r.spans, 1, `${r.text}'s row has its name as its one span, which the torch lights`);
        // drawn as high as a glyph's strokes reach (2.5 to 13.5 of 16), not across the whole box
        assert.ok(r.mw <= 11.5 && r.mh <= 11.5 && r.mw >= 10.5, `${r.text}'s logo is the glyphs' size: ${r.mw}x${r.mh}`);
      }
      // both rest in --muted, the glyphs' ink, with no colour of their own
      for (const logo of ["claudecode-color", "pi"]) {
        const c = await rgbOf(page, `.nav-sub[data-view="library"] .nav-s .ic[data-icon="${logo}"] .mask`, "backgroundColor");
        assert.ok(far(c.v, c.muted) < 6, `${logo} rests in --muted: ${JSON.stringify(c)}`);
      }

      // Pi's mask rests in --muted, as a glyph does, and takes the torch
      const piRow = `.nav-sub[data-view="library"] .nav-s:has(> .nav-s-ic > .ic[data-icon="pi"])`, piIc = `${piRow} > .nav-s-ic`;
      const rest = await rgbOf(page, `${piIc} .mask`, "backgroundColor");
      assert.ok(far(rest.v, rest.muted) < 6, `Pi's logo rests in --muted: ${JSON.stringify(rest)}`);
      await page.mouse.move(40, 300);
      await page.waitForFunction(() => document.documentElement.classList.contains("navlamp"));
      const at = await page.$eval(piIc, (e) => { const b = e.getBoundingClientRect(); return { x: b.left + b.width / 2, y: b.top + b.height / 2 }; });
      await page.mouse.move(at.x, at.y);
      await page.waitForFunction((s) => { const e = document.querySelector(s); return e.classList.contains("lit") && +e.style.getPropertyValue("--si") > 0.99; }, piIc);
      const on = await settle(() => rgbOf(page, `${piIc} .mask`, "backgroundColor"), (c) => far(c.v, c.muted) > 30);
      assert.ok(far(on.v, on.muted) > 30, `Pi's logo is lit under the pointer: ${JSON.stringify(on)}`);

      // chosen, Claude's row's logo is whole, in the row's ink
      await page.locator(`.nav-sub[data-view="library"] .nav-s:has(> .nav-s-ic > .ic[data-icon="claudecode-color"])`).click();
      await page.waitForSelector(`.nav-sub[data-view="library"] .nav-s.cur > .nav-s-ic > .ic[data-icon="claudecode-color"] .mask`);
      const cur = await settle(() => page.$eval(`.nav-s.cur > .nav-s-ic .mask`, (i) => ({ w: i.getBoundingClientRect().width, o: getComputedStyle(i).opacity, bg: getComputedStyle(i).backgroundColor })),
        (c) => c.o === "1" && Math.round(c.w) === 11);
      assert.ok(cur.o === "1" && Math.round(cur.w) === 11, `the chosen row's logo is whole: ${JSON.stringify(cur)}`);
      assert.deepEqual(errors, []);
    });
  });
}
