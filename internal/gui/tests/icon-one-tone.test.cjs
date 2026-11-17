// Run with Node's test runner and Playwright on the module path; see README.md.
// chen2652 on Discord: the Open Design plugin's logo was almost invisible in
// dark mode. Its picture (open-design.ai/favicon.svg, saved here as it is)
// is one tone, #26251E, and was drawn as an <img> on #232327. A picture in
// one near-black or near-white tone is now drawn as a mono logo is, a mask
// in the text colour. Every logo icons/ draws as an <img>, and Open Design's
// picture on the Plugins page, must show on both themes' ground: the ink
// that stands apart from the ground is measured on a screenshot, in Chromium
// and WebKit. voyage-color (#012E33) was the bundled one that failed.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const OD = "@magpie-community/opencode-opendesign-auth";
const odSVG = path.join(__dirname, "fixtures/opendesign-favicon.svg");

function server(lang, theme) {
  const state = { bun: true, bunVersion: "1.3.0", picker: false, movable: [], plugins: [
    { spec: OD, version: "0.1.8", icon: "file:od.svg", providers: ["Open Design"], moved: [] },
  ] };
  const subs = [{ id: "opendesign-plugin", pid: "opendesign", name: "Open Design", icon: "file:od.svg", spec: OD, methods: [], signedIn: false, models: 0 }];
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: subs });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugins") return json(state);
    if (url.pathname === "/api/plugins/listings") return json({ listings: [] });
    if (url.pathname === "/api/plugins/market") return json({ listings: [], state });
    if (url.pathname === "/api/icons/od.svg") return route.fulfill({ contentType: "image/svg+xml", body: await fs.readFile(odSVG) });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// The share of each box's pixels that stand apart from the ground (a
// colour distance over 48 of 441), read from one screenshot of the page.
async function inked(page, sel) {
  const shot = (await page.screenshot()).toString("base64");
  return page.evaluate(async ([shot, sel]) => {
    const img = new Image();
    img.src = "data:image/png;base64," + shot;
    await img.decode();
    const c = document.createElement("canvas");
    c.width = img.naturalWidth; c.height = img.naturalHeight;
    const x = c.getContext("2d", { willReadFrequently: true });
    x.drawImage(img, 0, 0);
    const k = img.naturalWidth / innerWidth, out = {};
    for (const e of document.querySelectorAll(sel)) {
      const b = e.getBoundingClientRect();
      const L = Math.round(b.left * k), T = Math.round(b.top * k), W = Math.round(b.width * k), H = Math.round(b.height * k);
      const d = x.getImageData(L, T, W, H).data;
      // the ground is the box's corner, outside the logo's ink in every icon
      const g = x.getImageData(L - 2, T - 2, 1, 1).data;
      let n = 0;
      for (let i = 0; i < d.length; i += 4) if (Math.hypot(d[i] - g[0], d[i + 1] - g[1], d[i + 2] - g[2]) > 48) n++;
      out[e.dataset.name || e.dataset.icon] = n / (W * H);
    }
    return out;
  }, [shot, sel]);
}

async function imgIcons() {
  const app = await fs.readFile(path.join(assets, "app.js"), "utf8");
  const png = new Set(JSON.parse(app.match(/const pngIcons = new Set\((\[[^\]]*\])\)/)[1]));
  const manifest = JSON.parse(await fs.readFile(path.join(assets, "manifest.json"), "utf8"));
  const own = new Set(manifest.icons.map((i) => i.src.replace(/^icons\//, "")));
  return (await fs.readdir(path.join(assets, "icons")))
    .filter((f) => !own.has(f) && (f.endsWith("-color.svg") || (f.endsWith(".png") && png.has(f.slice(0, -4)))))
    .map((f) => f.replace(/\.(svg|png)$/, ""));
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": every logo drawn as a picture shows on the dark and the light ground", async (t) => {
    const names = await imgIcons();
    assert.ok(names.length > 80, `found only ${names.length} picture logos`);
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch());
    t.after(() => browser.close());
    for (const theme of ["dark", "light"]) {
      await t.test(theme, async () => {
        const page = await (await browser.newContext({ viewport: { width: 1000, height: 900 } })).newPage();
        await page.route("**/*", server("en", theme));
        await page.goto("http://magpie.test/?view=plugins");
        await page.waitForFunction(() => typeof icon === "function");
        // a grid of the logos on the theme's card, at the size a row's head draws them
        await page.evaluate((names) => {
          const g = document.createElement("div");
          g.id = "grid";
          g.style.cssText = "position:fixed;inset:0;z-index:99999;background:var(--card);color:var(--fg);display:flex;flex-wrap:wrap;gap:6px;padding:6px;align-content:flex-start";
          for (const n of names) { const e = icon(n); e.style.width = e.style.height = "28px"; g.append(e); }
          document.body.append(g);
        }, names);
        await page.waitForFunction(() => [...document.querySelectorAll("#grid img")].every((i) => i.complete) && [...document.querySelectorAll("#grid .mask")].length === [...document.querySelectorAll("#grid .ic")].length - document.querySelectorAll("#grid img").length);
        await page.waitForTimeout(100);
        const ink = await inked(page, "#grid .ic");
        const faint = Object.entries(ink).filter(([, v]) => v < 0.04).map(([n, v]) => `${n} ${(v * 100).toFixed(1)}%`);
        assert.deepEqual(faint, [], `logos that don't show on the ${theme} ground`);
        await page.context().close();
      });
    }
  });

  test(engine + ": Open Design's one-tone picture is drawn in the text colour on the Plugins page", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch());
    t.after(() => browser.close());
    for (const theme of ["dark", "light"]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [980, 420]) {
      await t.test(`${theme} ${lang} ${width}`, async () => {
        const page = await (await browser.newContext({ viewport: { width, height: 820 } })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, theme));
        await page.goto("http://magpie.test/?view=plugins");
        const view = page.locator("#view-plugins");
        const installed = await page.evaluate((l) => l === "en" ? "Installed" : I18N[l]["Installed"], lang);
        await page.locator(":is(.lib-tabs .opt, .nav-s):visible", { hasText: installed }).click();
        const row = view.locator(".pm-row").filter({ has: page.locator(".name", { hasText: "opendesign" }) });
        await row.locator(".pm-logo .ic .mask").waitFor();
        assert.equal(await row.locator(".pm-logo img").count(), 0, "the picture gave way to its mask");
        const [paint, fg] = await row.locator(".pm-logo .ic .mask").evaluate((m) => [getComputedStyle(m).backgroundColor, getComputedStyle(m.parentNode).color]);
        assert.equal(paint, fg, "drawn in the text colour, as a mono logo is");
        await page.waitForTimeout(100);
        await row.locator(".pm-logo .ic").evaluate((e) => (e.dataset.name = "od"));
        const ink = await inked(page, "#view-plugins .pm-row .pm-logo .ic[data-name]");
        assert.ok(ink.od > 0.1, `Open Design's logo shows on the ${theme} ground: ${(ink.od * 100).toFixed(1)}% inked`);
        if (process.env.ARTIFACT_DIR && lang === "en") await row.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `od-${engine}-${theme}-${width}.png`) });
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
