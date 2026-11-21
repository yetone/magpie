// Run with Node's test runner and Playwright on the module path; see README.md.
// A partner's tile in the Add sheet is its logo beside two lines, its name
// and its tagline. The logo's top meets the top of the name's letters, not
// the top of the name's line box, which sits a few pixels above them (the
// owner, 11-21: 左边的 icon 并没有与右边的文字在靠顶部对齐). Measured on the
// rendered pixels at 4x: the logo's ink against the name's ink, a Latin name
// (the owner's case, Kimi Code) and a Chinese one, in all five languages, at 900px and 440px, Chromium and
// WebKit. No backend; the logo is a full-bleed square like the partners'.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, process.env.MAGPIE_ASSETS || "../assets");
const LOGO = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="14" fill="#111"/></svg>';
const notes = { en: "One plan, every agent shares it", zh: "一份订阅，所有 agent 共享额度", "zh-TW": "一份訂閱，所有 agent 共用額度", ja: "ひとつのプランを全エージェントで", de: "Ein Abo für jeden Agenten" };
const partners = [
  { id: "kimi-p", name: "Kimi Code", icon: "file:logo.svg", kind: "partner", sponsored: true, notes, chat: "https://api.kimi.example.com/v1" },
  { id: "yun-p", name: "云雀中转", icon: "file:logo.svg", kind: "partner", sponsored: true, notes, chat: "https://api.yun.example.com/v1" },
];
const presets = [{ id: "openai", name: "OpenAI", icon: "openai", kind: "vendor", chat: "https://api.openai.example.com/v1" }];
const providers = [{ id: "openai", name: "OpenAI", icon: "openai", preset: "openai", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }];

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname.startsWith("/api/icons/")) return route.fulfill({ contentType: "image/svg+xml", body: LOGO });
    if (url.pathname === "/api/partner") return route.fulfill({ status: 204, body: "" });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: false })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers, presets: [...partners, ...presets], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// the top of the logo's ink and of the name's ink in a tile, in CSS px
async function inkTops(page, tile) {
  const tb = await tile.boundingBox();
  const ib = await tile.locator(".ic").boundingBox();
  const nb = await tile.locator(".n").boundingBox();
  const png = (await page.screenshot({ clip: tb })).toString("base64");
  return page.evaluate(async ({ png, tb, parts }) => {
    const img = new Image();
    img.src = "data:image/png;base64," + png;
    await img.decode();
    const c = document.createElement("canvas");
    c.width = img.width; c.height = img.height;
    const x = c.getContext("2d");
    x.drawImage(img, 0, 0);
    const k = img.width / tb.width, d = x.getImageData(0, 0, c.width, c.height).data;
    const bg = [d[0], d[1], d[2]];
    return parts.map((b) => {
      const x0 = Math.max(0, Math.round((b.x - tb.x) * k)), x1 = Math.min(c.width, Math.round((b.x + b.width - tb.x) * k));
      const y1 = Math.min(c.height, Math.round((b.y + b.height - tb.y) * k));
      for (let y = 0; y < y1; y++) for (let xx = x0; xx < x1; xx++) {
        const i = (y * c.width + xx) * 4;
        if (Math.abs(d[i] - bg[0]) + Math.abs(d[i + 1] - bg[1]) + Math.abs(d[i + 2] - bg[2]) > 120) return y / k;
      }
      return -1;
    });
  }, { png, tb, parts: [ib, nb] });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a partner's logo meets the top of its name", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [900, 440]) {
      await t.test(lang + " " + width, async (st) => {
        const ctx = await browser.newContext({ viewport: { width, height: 800 }, deviceScaleFactor: 4, reducedMotion: "reduce" });
        st.after(() => ctx.close());
        const page = await ctx.newPage();
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator("#addProvider").click();
        const tiles = page.locator("#addSheet .tile.two");
        await tiles.nth(1).waitFor();
        await page.mouse.move(0, 0);
        await page.evaluate(() => Promise.all([...document.querySelectorAll("#addSheet .tile.two img")].map((i) => i.decode())));
        const seen = [];
        for (const name of ["Kimi Code", "云雀中转"]) seen.push([name, ...await inkTops(page, tiles.filter({ hasText: name }))]);
        if (process.env.SHOW) console.log(engine, lang, width, JSON.stringify(seen));
        // A Latin name's capitals within half a pixel; a CJK name's letters
        // stand 1 to 1.5px taller than capitals, so within a pixel, plus
        // the 0.25px a 4x screenshot measures in
        for (const [name, logo, text] of seen) {
          assert(logo >= 0 && text >= 0, name + ": nothing drawn");
          const room = /[\u3000-\u9fff]/.test(name) ? 1.25 : 0.5;
          assert(Math.abs(logo - text) <= room, `${name}: the logo's top is at ${logo.toFixed(2)}px, its name's letters start at ${text.toFixed(2)}px`);
        }
        assert.deepEqual(errors, []);
      });
    }
  });
}
