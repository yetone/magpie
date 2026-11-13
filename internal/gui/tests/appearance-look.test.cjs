// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings → General's Accent color and Background image (CHENGJIAHEGOXIS
// on X: "other colours of my own, and a picture from my computer"). In both
// engines, with the API faked, in every language, wide and at a phone's
// width:
// - the Accent color row offers magpie's own, six presets and a colour of
//   the reader's own; picking one saves it as accent and the page takes
//   the server's stylesheet for it (/api/appearance/css) with no reload;
// - a colour from the system's picker is saved, and shown as the chosen
//   swatch;
// - Background image: Choose… sends the picture as base64 JSON, a thumbnail
//   with Change and Remove shows, and Remove sends {remove: true};
// - a page whose boot.js drew the look already asks for no stylesheet;
// - the rows fit a narrow window, and their words are the language's own.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
// a 1×1 PNG
const PNG = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==", "base64");
const accentCSS = (a) => (a ? `html:not(.omarchy):root{--accent:${a};--accent-soft:#e6f4f2;--accent-fg:#ffffff;--sel:#e1f1ef}` : "");

function serve(st, log) {
  const settings = () => ({ lang: "en", theme: "light", searchVendors: [], searchAPIs: [], ...st });
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") {
      const look = (st.accent || "") + "," + (st.wallpaper || "");
      const css = accentCSS(st.accent);
      return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang: st.lang || "en", theme: "light", web: false, look })};` + (css ? `(function(){var e=document.createElement("style");e.id="lookCSS";e.textContent=${JSON.stringify(css)};document.head.appendChild(e)})();` : "") });
    }
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings() });
    if (url.pathname === "/api/settings") {
      if (r.request().method() === "POST") {
        const b = JSON.parse(r.request().postData());
        log.push(["settings", b]);
        Object.assign(st, b);
      }
      return json(settings());
    }
    if (url.pathname === "/api/appearance/css") {
      log.push(["css"]);
      return json({ css: accentCSS(st.accent), key: (st.accent || "") + "," + (st.wallpaper || "") });
    }
    if (url.pathname === "/api/appearance/wallpaper") {
      if (r.request().method() === "POST") {
        const b = JSON.parse(r.request().postData());
        log.push(["wallpaper", b]);
        st.wallpaper = b.remove ? 0 : 1700000000000 + log.length;
        return json(settings());
      }
      return r.fulfill({ body: PNG, contentType: "image/png" });
    }
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

const NAMES = { en: "Accent color", zh: "强调色", "zh-TW": "強調色", ja: "アクセントカラー", de: "Akzentfarbe" };
const PICTURE = { en: "Background image", zh: "背景图片", "zh-TW": "背景圖片", ja: "背景画像", de: "Hintergrundbild" };

async function open(browser, st, width) {
  const page = await (await browser.newContext({ viewport: { width, height: 800 } })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], log = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(st, log));
  await page.goto("http://magpie.test/?view=settings");
  await page.locator("#accentPick .swatch").first().waitFor();
  return { page, errors, log };
}

const accentNow = (page) => page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue("--accent").trim());
const chosen = (page) => page.evaluate(() => [...document.querySelectorAll("#accentPick .swatch.on")].map((b) => b.dataset.accent ?? "own:" + b.style.getPropertyValue("--sw")));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: an accent colour of the reader's own`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const st = {};
    const { page, errors, log } = await open(browser, st, 1000);
    const sw = page.locator("#accentPick .swatch");
    assert.equal(await sw.count(), 8, "magpie's own, six presets and the reader's own");
    assert.deepEqual(await chosen(page), [""], "magpie's own is chosen at first");
    assert.equal(await accentNow(page), "#4f46e5");
    assert.deepEqual(log.filter(([k]) => k === "css"), [], "magpie's own look needs no stylesheet");

    await page.locator('#accentPick .swatch[data-accent="#0d9488"]').click();
    await page.waitForFunction(() => getComputedStyle(document.documentElement).getPropertyValue("--accent").trim() === "#0d9488");
    assert.equal(log.find(([k]) => k === "settings")[1].accent, "#0d9488");
    await page.waitForFunction(() => document.querySelector("#accentPick .swatch.on")?.dataset.accent === "#0d9488");

    // the system's picker: its input reports the colour when it closes
    await page.locator("#accentPick .swatch.own input").evaluate((i) => { i.value = "#7c3aed"; i.dispatchEvent(new Event("change", { bubbles: true })); });
    await page.waitForFunction(() => getComputedStyle(document.documentElement).getPropertyValue("--accent").trim() === "#7c3aed");
    assert.equal(log.filter(([k]) => k === "settings").at(-1)[1].accent, "#7c3aed");
    await page.waitForFunction(() => document.querySelector("#accentPick .swatch.own.on"));
    assert.deepEqual(await chosen(page), ["own:#7c3aed"]);

    // back to magpie's own: nothing drawn over it
    await page.locator('#accentPick .swatch[data-accent=""]').click();
    await page.waitForFunction(() => getComputedStyle(document.documentElement).getPropertyValue("--accent").trim() === "#4f46e5");
    assert.equal(log.filter(([k]) => k === "settings").at(-1)[1].accent, "");
    assert.deepEqual(errors, []);
  });

  test(`${engine}: a look boot.js drew is not asked for again`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const { page, errors, log } = await open(browser, { accent: "#0d9488" }, 1000);
    assert.equal(await accentNow(page), "#0d9488", "drawn before the first paint");
    assert.deepEqual(await chosen(page), ["#0d9488"]);
    await page.waitForTimeout(300);
    assert.deepEqual(log.filter(([k]) => k === "css"), []);
    assert.deepEqual(errors, []);
  });

  test(`${engine}: a picture behind the pages`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const st = {};
    const { page, errors, log } = await open(browser, st, 1000);
    const row = page.locator("#wallpaperPick");
    assert.deepEqual(await row.locator("button").allTextContents(), ["Choose…"]);
    await row.locator('input[type="file"]').setInputFiles({ name: "beach.png", mimeType: "image/png", buffer: PNG });
    await row.locator(".wall-thumb").waitFor();
    const sent = log.find(([k]) => k === "wallpaper")[1];
    assert.equal(sent.data, PNG.toString("base64"));
    assert.deepEqual(await row.locator("button").allTextContents(), ["Change", "Remove"]);
    assert.match(await row.locator(".wall-thumb").getAttribute("src"), /^\/api\/appearance\/wallpaper\?v=\d+$/);
    // the look changed: the page takes the server's stylesheet again
    await page.waitForFunction(() => document.getElementById("lookCSS"));
    assert.ok(log.some(([k]) => k === "css"));

    await row.locator("button", { hasText: "Remove" }).click();
    await page.waitForFunction(() => !document.querySelector("#wallpaperPick .wall-thumb"));
    assert.deepEqual(log.filter(([k]) => k === "wallpaper").at(-1)[1], { remove: true });
    assert.deepEqual(await row.locator("button").allTextContents(), ["Choose…"]);
    assert.deepEqual(errors, []);
  });

  test(`${engine}: the rows keep their height while their controls load`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 1000, height: 800 } })).newPage();
    page.setDefaultTimeout(5000);
    const log = [];
    let release;
    const held = new Promise((r) => (release = r));
    await page.route("**/*", serve({ wallpaper: 1700000000000 }, log));
    // appearance.js is held back, so the page shows the rows before their controls
    await page.route("**/appearance.js", async (r) => { await held; await r.fallback(); });
    await page.goto("http://magpie.test/?view=settings");
    await page.locator("#colorblindRow").waitFor();
    const ids = ["accentRow", "wallpaperRow", "colorblindRow", "reduceMotionRow", "birdRow"];
    const heights = () => page.evaluate((ids) => ids.map((id) => [id, Math.round(document.getElementById(id)?.getBoundingClientRect().height)]), ids);
    await page.waitForFunction(() => document.querySelector("#view-settings")?.offsetParent !== null);
    const before = await heights();
    release();
    await page.locator("#wallpaperPick .wall-thumb").waitFor();
    await page.locator("#birdSegs .segs").waitFor();
    assert.deepEqual(await heights(), before, "a row grew once its controls came, and moved the page under the reader");
  });

  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [420, 1000]) {
      test(`${engine}: ${lang} at ${width}px, the rows fit and speak the language`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const { page, errors } = await open(browser, { lang, accent: "#7c3aed", wallpaper: 1700000000000 }, width);
        await page.locator("#wallpaperPick .wall-thumb").waitFor();
        assert.equal((await page.locator("#accentRow .name").textContent()).trim(), NAMES[lang]);
        assert.equal((await page.locator("#wallpaperRow .name").textContent()).trim(), PICTURE[lang]);
        const fit = await page.evaluate(() => {
          const out = [];
          for (const id of ["accentRow", "wallpaperRow"]) {
            const row = document.getElementById(id), r = row.getBoundingClientRect();
            if (row.scrollWidth > row.clientWidth + 1) out.push(id + " scrolls");
            for (const c of row.querySelectorAll(".swatch, button, .wall-thumb")) {
              const b = c.getBoundingClientRect();
              if (b.right > r.right + 1 || b.left < r.left - 1) out.push(id + " " + (c.dataset.accent ?? c.textContent) + " outside");
            }
          }
          // the presets' names are said in the language too
          const titles = [...document.querySelectorAll("#accentPick .swatch")].map((b) => b.title);
          return { out, titles, page: document.documentElement.scrollWidth <= innerWidth };
        });
        assert.deepEqual(fit.out, []);
        assert.ok(fit.page, "the page scrolls sideways");
        if (lang !== "en") assert.notDeepEqual(fit.titles.slice(1, 4), ["Blue", "Teal", "Green"]);
        assert.deepEqual(errors, []);
      });
    }
  }
}
