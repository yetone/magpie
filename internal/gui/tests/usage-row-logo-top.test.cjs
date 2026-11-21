// A Usage breakdown row (by agent, model, account) is a logo beside its name
// and the line under it, which wraps to two to four lines in the 180px
// column. The logo's top meets the top of the name's letters, as the
// partner tiles' does (partner-tile-top), not the middle of the whole block:
// centred, it floated beside the second line of a long row (the owner,
// 11-21: 这里的 Logo 应该与右边的文字区域靠顶部对齐). Measured on the rendered
// pixels at 4x: the logo's ink against the name's ink, a Latin name and a
// Chinese one, with a one-line and a wrapped line under them, in all five
// languages, at 1280px and 600px, Chromium and WebKit. The logo is a
// full-bleed square like the real ones.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, process.env.MAGPIE_ASSETS || "../assets");
const LOGO = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="14" fill="#111"/></svg>';

function fixture(lang) {
  const base = { input: 23100, output: 859, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0.004, errors: 0 };
  const short = { ...base, calls: 7 };
  // the owner's Alma row: credits, a retry, TTFT and a speed wrap it
  const long = { ...base, calls: 6, credited: 6, credits: 0.95, retried: 1, timed: 5, ttft_ms: 5 * 1700, decode_n: 5, decode_out: 5 * 164, decode_ms: 5 * 1000, tps: 164 };
  const agents = [{ id: "memory", name: "Memory", icon: "file:logo.svg", ...short }, { id: "alma", name: "Alma", icon: "file:logo.svg", ...long }];
  const models = [{ id: "m1", name: "claude-haiku-4.5", sub: "Kiro", icon: "file:logo.svg", ...short }, { id: "m2", name: "云雀模型", sub: "WorkBuddy", icon: "file:logo.svg", ...long }];
  const accounts = [{ id: "a1", name: "云雀账号", sub: "Kiro", icon: "file:logo.svg", ...short }, { id: "a2", name: "yetone", sub: "WorkBuddy", icon: "file:logo.svg", ...long }];
  const totals = { calls: 13, errors: 0, input: 46200, output: 1718, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0.008, unpriced: 0 };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname.startsWith("/api/icons/")) return route.fulfill({ contentType: "image/svg+xml", body: LOGO });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: false })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ ...totals, accounts, callerKeys: [], agents, models, series: [], bucket: "day", path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// the top of the logo's ink and of the name's ink in a row, in CSS px
async function inkTops(page, row) {
  await row.scrollIntoViewIfNeeded();
  const rb = await row.boundingBox();
  const ib = await row.locator(":scope > .ic").boundingBox();
  const nb = await row.locator(".who .name").boundingBox();
  const sb = await row.locator(".who .sub").boundingBox();
  const png = (await page.screenshot({ clip: rb })).toString("base64");
  const [logoY, nameY] = await page.evaluate(async ({ png, rb, parts }) => {
    const img = new Image();
    img.src = "data:image/png;base64," + png;
    await img.decode();
    const c = document.createElement("canvas");
    c.width = img.width; c.height = img.height;
    const x = c.getContext("2d");
    x.drawImage(img, 0, 0);
    const k = img.width / rb.width, d = x.getImageData(0, 0, c.width, c.height).data;
    const bg = [d[(2 * c.width + 2) * 4], d[(2 * c.width + 2) * 4 + 1], d[(2 * c.width + 2) * 4 + 2]];
    return parts.map((b) => {
      const x0 = Math.max(0, Math.round((b.x - rb.x) * k)), x1 = Math.min(c.width, Math.round((b.x + b.width - rb.x) * k));
      const y1 = Math.min(c.height, Math.round((b.y + b.height - rb.y) * k));
      for (let y = 0; y < y1; y++) for (let xx = x0; xx < x1; xx++) {
        const i = (y * c.width + xx) * 4;
        if (Math.abs(d[i] - bg[0]) + Math.abs(d[i + 1] - bg[1]) + Math.abs(d[i + 2] - bg[2]) > 120) return y / k;
      }
      return -1;
    });
  }, { png, rb, parts: [ib, nb] });
  return { logoY, nameY, lines: Math.round(sb.height / 14), rowH: rb.height };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a usage row's logo meets the top of its name", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1280, 600]) {
      await t.test(lang + " " + width, async (st) => {
        const ctx = await browser.newContext({ viewport: { width, height: 1000 }, deviceScaleFactor: 4, reducedMotion: "reduce" });
        st.after(() => ctx.close());
        const page = await ctx.newPage();
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", fixture(lang));
        await page.goto("http://magpie.test/?view=usage");
        const rows = page.locator("#usageAgents .row.stat, #usageModels .row.stat, #usageAccounts .row.stat");
        await page.locator("#usageAccounts .row.stat").nth(1).waitFor();
        await page.mouse.move(0, 0);
        await page.evaluate(() => Promise.all([...document.querySelectorAll(".row.stat > img.ic")].map((i) => i.decode().catch(() => {}))));
        const seen = [];
        for (let i = 0; i < await rows.count(); i++) {
          const row = rows.nth(i);
          seen.push({ name: (await row.locator(".who .name").textContent()).trim(), ...await inkTops(page, row) });
        }
        if (process.env.SHOW) console.log(engine, lang, width, JSON.stringify(seen.map((s) => [s.name, s.lines, +s.logoY.toFixed(2), +s.nameY.toFixed(2), +(s.nameY - s.logoY).toFixed(2)])));
        assert(seen.some((s) => s.lines >= 2), "a row's line under its name wraps");
        // A name starting with a capital (the owner's Memory, Alma) within
        // 0.75px of its capitals: the logo lands on a whole pixel and the
        // text needn't, so half a pixel, plus the 0.25px a 4x screenshot
        // measures in. A CJK name's letters stand a pixel or so taller
        // than capitals, so within 1.25px. A name in lower case (an
        // account's "yetone") starts at its x-height or ascenders: the
        // logo is at its capitals' height, so above those letters by up
        // to 2.5px and never below them.
        for (const s of seen) {
          assert(s.logoY >= 0 && s.nameY >= 0, s.name + ": nothing drawn");
          const at = `${s.name} (${s.lines} lines under it): the logo's top is at ${s.logoY.toFixed(2)}px, its name's letters start at ${s.nameY.toFixed(2)}px`;
          if (/^[A-Z]/.test(s.name)) assert(Math.abs(s.logoY - s.nameY) <= 0.75, at);
          else if (/^[\u3000-\u9fff]/.test(s.name)) assert(Math.abs(s.logoY - s.nameY) <= 1.25, at);
          else assert(s.nameY - s.logoY >= -0.5 && s.nameY - s.logoY <= 2.5, at);
        }
        assert.deepEqual(errors, []);
      });
    }
  });
}
