// Run with Node's test runner and Playwright on the module path; see README.md.
// A plugin's provider panel is a row of its own providers' editors (11-21,
// the owner: "这是什么严重bug？"). Tencent Marvis's first-line panel was
// drawn in every provider's editor, Kimi Coding Plan's among them, and in
// none of Marvis's own; set in the editor's grid as one item, it fell in
// the names' column, so its title wrapped, the package's name was cut and
// the panel's button was clipped in a 210px frame. Here a plugin that
// gives the provider "marvis" has a panel like Marvis's: Kimi's editor has
// none, and Marvis's has it as a row, its title on one line in the names'
// column and the panel in the fields', nothing in it cut. Both engines,
// 1280 and 600 wide, every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const bridge = path.resolve(__dirname, "../pagebridge.js");
const TOK = "7".repeat(32);

// a row like Marvis's panel/first-line.html: a label, the value, a button
const PANEL_HTML = `<!doctype html><meta charset="utf-8"><script src="_magpie/bridge.js"></script>
<style>body{margin:0} .fl-row{display:flex;align-items:center;gap:10px;padding:6px 10px} .fl-value{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}</style>
<section class="fl"><div class="fl-row"><span class="fl-label">首行文本</span><span class="fl-value">You are Marvis, Tencent's AI assistant.</span><button id="open" type="button">设置…</button></div></section>`;

const panels = [{ spec: "magpie-marvis@latest", name: "magpie-marvis", key: "plugin-marvis-0", slot: "provider", title: "首行文本 First line text",
  titles: { zh: "首行文本", en: "First line text" }, reads: [], url: `/_magpie/plugin/${TOK}/panel/first-line.html`, providers: ["marvis"] }];

const base = { responses: "", anthropic: "", catalog: "", agents: [], fallback: [], headers: {}, keyList: [], key: {}, proxy: "", maxConcurrency: null };
const payload = { providers: [
  { ...base, id: "kimi-code-cn", name: "Kimi Coding Plan", icon: "generic", host: "api.kimi.com", chat: "https://api.kimi.com/coding/v1", key: { set: true }, ready: true,
    models: [{ id: "kimi-k2", name: "Kimi K2", on: true }] },
  { ...base, id: "marvis", name: "腾讯 Marvis TENCENT", icon: "generic", chat: "plugin://marvis/v1", models: [{ id: "m1", name: "M1", on: true }],
    account: { agent: "marvis", agentName: "腾讯 Marvis TENCENT", agentIcon: "generic", user: "a@fake", logins: [{ user: "a@fake", active: true, on: true }] } },
], presets: [], excluded: [], gateway: { running: true, window: true },
plugins: [{ id: "marvis", pid: "marvis", name: "腾讯 Marvis TENCENT", icon: "generic", spec: "magpie-marvis@latest", signedIn: true, models: 1, methods: [{ type: "api", label: "API key" }] }] };

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(payload);
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/plugin-pages") return json([]);
    if (url.pathname === "/api/plugin-panels") return json(panels);
    if (url.pathname.startsWith("/api/")) return json({});
    const m = url.pathname.match(/^\/_magpie\/plugin\/([0-9a-f]{32})\/(.+)$/);
    if (m) {
      const body = m[2] === "_magpie/bridge.js" ? await fs.readFile(bridge) : m[2] === "panel/first-line.html" ? PANEL_HTML : null;
      if (body == null) return route.fulfill({ status: 404, body: "" });
      return route.fulfill({ status: 200, headers: { "Content-Security-Policy": "sandbox allow-scripts; connect-src 'none'", "Content-Type": m[2].endsWith(".js") ? "text/javascript" : "text/html" }, body });
    }
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const TITLE = { en: "First line text", zh: "首行文本", "zh-TW": "首行文本", ja: "首行文本 First line text", de: "首行文本 First line text" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a plugin's provider panel is a row of its own providers' editors", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const width of [1280, 600]) for (const lang of Object.keys(TITLE)) {
      await t.test(`${width} ${lang}`, async () => {
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        const page = await ctx.newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=providers");
        await page.waitForFunction(() => providers?.providers?.length === 2 && typeof magpie?.slot === "function");
        // the panels known before an editor opens, as in the app
        await page.waitForTimeout(300);

        // Kimi's editor: not Marvis's provider, no panel
        await page.evaluate(() => openModal(renderEditor(providers.providers.find((p) => p.id === "kimi-code-cn"))));
        await page.locator("#modal .editor").waitFor();
        await page.waitForTimeout(200);
        assert.equal(await page.locator('#modal .plugin-panel[data-panel="plugin-marvis-0"]').count(), 0, "Marvis's panel in Kimi's editor");
        await page.evaluate(() => closeModal());

        // Marvis's own: the panel, as a row
        await page.evaluate(() => openModal(renderEditor(providers.providers.find((p) => p.id === "marvis"))));
        const sec = page.locator('#modal .plugin-panel[data-panel="plugin-marvis-0"]');
        await sec.waitFor();
        await page.waitForFunction(() => { const f = document.querySelector('#modal .plugin-panel[data-panel="plugin-marvis-0"] iframe'); return f && f.getBoundingClientRect().height > 20; });
        const g = await sec.evaluate((s) => {
          const box = (x) => { const r = x.getBoundingClientRect(); return { l: r.left, r: r.right, t: r.top, h: r.height, w: r.width }; };
          const head = s.querySelector(".row-head"), label = head.querySelector(".label"), body = s.querySelector(".plugin-panel-body");
          const grid = s.closest(".ebody") && getComputedStyle(s.closest(".ebody")).display === "grid" ? s.closest(".ebody") : s.closest(".editor");
          // another row's name, to line the panel's title up with
          const other = [...grid.children].find((k) => k.tagName === "LABEL" && k.offsetParent);
          return { head: box(head), label: box(label), text: label.textContent, lineH: parseFloat(getComputedStyle(label).lineHeight) || 17,
            note: head.querySelector(".note") ? getComputedStyle(head.querySelector(".note")).display : "none",
            body: box(body), grid: box(grid), other: other && box(other) };
        });
        const where = `${engine} ${width} ${lang}: ${JSON.stringify(g)}`;
        assert.equal(g.text, TITLE[lang], where);
        // a short title on one line; a long one (the plugin's title in a
        // language it gave none for) wraps as any other name does
        if (TITLE[lang].length <= 16) assert.ok(g.label.h <= g.other.h * 1.5, "the title wraps: " + where);
        assert.equal(g.note, "none", "the package's name under the title: " + where);
        assert.ok(g.head.r <= g.body.l, "the title in the names' column: " + where);
        assert.ok(g.other && Math.abs(g.head.r - g.other.r) <= 2, "the title lines up with the other names: " + where);
        assert.ok(g.body.w >= g.grid.w * 0.5, "the panel in the fields' column, full width: " + where);
        assert.ok(g.head.t >= g.body.t - 1 && g.head.t <= g.body.t + 20, "the title beside the panel: " + where);

        // nothing in the panel cut
        const frame = page.frames().find((f) => f.url().includes(TOK));
        const cut = await frame.evaluate(() => {
          const b = document.getElementById("open").getBoundingClientRect();
          return { spill: document.documentElement.scrollWidth - document.documentElement.clientWidth, right: b.right, bottom: b.bottom, w: innerWidth, h: innerHeight };
        });
        assert.ok(cut.spill <= 0 && cut.right <= cut.w && cut.bottom <= cut.h, "the panel's button is cut: " + JSON.stringify(cut) + " " + where);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), "the page scrolls sideways: " + where);
        if (process.env.ARTIFACT_DIR) await page.locator("#modal .editor").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `plugin-provider-panel-${engine}-${width}-${lang}.png`) });
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
