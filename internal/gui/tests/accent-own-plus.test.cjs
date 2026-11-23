// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings → General → Accent color: the last swatch, a colour of the
// reader's own, is an empty ring with a plus until one is picked (the
// owner, 11-23: 「这个加号竟然不在圆心」). The plus was a typed "+",
// placed where its font's metrics put it, so its ink sat off the circle's
// centre. Measured by pixel scan, its ink's centre is the circle's centre
// to half a CSS pixel, and its arms are as long across as down, in both
// engines, light and dark, in en and zh, at 1x, 2x and 3x screens and at
// 1.5x (the 150% text size, a zoom of the window, on a 1x screen).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, theme) {
  const settings = { lang, theme, searchVendors: [], searchAPIs: [] };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme, web: false, look: "," })};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the plus's ink inside the ring: its centre and its width and height, in
// CSS px from the ring's centre. The ring is left out of the plus by
// reading only within 6px of the centre (its inner edge is 7.5px out).
async function plusInk(page, sw) {
  const b = await sw.boundingBox();
  const clip = { x: b.x - 2, y: b.y - 2, width: b.width + 4, height: b.height + 4 };
  const png = (await page.screenshot({ clip })).toString("base64");
  return page.evaluate(async ({ png, clip, b }) => {
    const img = new Image();
    img.src = "data:image/png;base64," + png;
    await img.decode();
    const c = document.createElement("canvas");
    c.width = img.width; c.height = img.height;
    const x = c.getContext("2d");
    x.drawImage(img, 0, 0);
    const k = img.width / clip.width, d = x.getImageData(0, 0, c.width, c.height).data;
    const at = (px, py) => { const i = (Math.floor(py) * c.width + Math.floor(px)) * 4; return [d[i], d[i + 1], d[i + 2]]; };
    const off = (p, q) => Math.abs(p[0] - q[0]) + Math.abs(p[1] - q[1]) + Math.abs(p[2] - q[2]);
    // the circle's centre is its ring's: the ink 6.5 to 10.5px out from the
    // box's centre, against the ground outside it (a corner of the clip).
    // Measured in the same image as the plus, so the clip's rounding moves
    // both alike.
    const bx = (b.x + b.width / 2 - clip.x) * k, by = (b.y + b.height / 2 - clip.y) * k;
    const out = at(1, 1);
    let rw = 0, rx = 0, ry = 0;
    for (let py = 0; py < c.height; py++) for (let px = 0; px < c.width; px++) {
      const dx = px + 0.5 - bx, dy = py + 0.5 - by, r = Math.hypot(dx, dy) / k;
      if (r < 6.5 || r > 10.5) continue;
      const v = off(at(px, py), out);
      rw += v; rx += v * (px + 0.5); ry += v * (py + 0.5);
    }
    if (!rw) return null;
    const cx = rx / rw, cy = ry / rw;
    // the ground inside the ring, on a diagonal clear of the plus's arms
    const bg = at(cx + 3.5 * k, cy + 3.5 * k);
    let w = 0, sx = 0, sy = 0, minX = 1e9, maxX = -1, minY = 1e9, maxY = -1;
    for (let py = 0; py < c.height; py++) for (let px = 0; px < c.width; px++) {
      const dx = px + 0.5 - cx, dy = py + 0.5 - cy;
      if (dx * dx + dy * dy > (6 * k) ** 2) continue;
      const diff = off(at(px, py), bg);
      if (diff < 24) continue;
      w += diff; sx += diff * dx; sy += diff * dy;
      minX = Math.min(minX, dx); maxX = Math.max(maxX, dx); minY = Math.min(minY, dy); maxY = Math.max(maxY, dy);
    }
    if (!w) return null;
    return { x: sx / w / k, y: sy / w / k, w: (maxX - minX + 1) / k, h: (maxY - minY + 1) / k };
  }, { png, clip, b });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the own colour's plus sits at its circle's centre", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) for (const theme of ["light", "dark"]) for (const scale of [1, 1.5, 2, 3]) {
      await t.test(`${lang} ${theme} ${scale}x`, async (st) => {
        const ctx = await browser.newContext({ viewport: { width: 1000, height: 800 }, deviceScaleFactor: scale, reducedMotion: "reduce", colorScheme: theme });
        st.after(() => ctx.close());
        const page = await ctx.newPage();
        page.setDefaultTimeout(15000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, theme));
        await page.goto("http://magpie.test/?view=settings");
        const sw = page.locator("#accentPick .swatch.own");
        await sw.waitFor();
        await page.mouse.move(0, 0);
        // the own swatch is the presets' size and round
        const sizes = await page.evaluate(() => [...document.querySelectorAll("#accentPick .swatch")].map((e) => { const r = e.getBoundingClientRect(); return [r.width, r.height, getComputedStyle(e).borderRadius]; }));
        for (const s of sizes) assert.deepEqual(s, sizes[0], "every swatch is one size and round: " + JSON.stringify(sizes));
        const ink = await plusInk(page, sw);
        assert(ink, "a plus is drawn in the ring");
        assert(Math.abs(ink.x) <= 0.5 && Math.abs(ink.y) <= 0.5, `the plus's ink is centred: ${JSON.stringify(ink)}`);
        assert(Math.abs(ink.w - ink.h) <= 0.75, `the plus is as wide as it is tall: ${JSON.stringify(ink)}`);
        assert.deepEqual(errors, []);
      });
    }
  });
}
