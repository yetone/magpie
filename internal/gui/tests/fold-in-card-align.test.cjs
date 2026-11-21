// Run with Node's test runner and Playwright on the module path; see README.md.
// An Advanced fold inside a settings card (Privacy's Models folder, under
// the local model's rows) is a line of the card: its chevron's ink starts
// where the rows' labels' ink starts, it is ruled off like a row, and it is
// the card's last line, after Rules, never between two rows (the owner,
// 11-21: 这个高级没有左对齐，这个位置看起来好让人难受). Measured on the
// rendered pixels at 4x: the chevron's ink against the Confidence
// threshold label's, in all five languages, at 1280px and 600px in the
// app and 600px in a browser, light and dark, Chromium and WebKit. And a
// late answer for a draw already replaced leaves the fold on screen alone.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, process.env.MAGPIE_ASSETS || "../assets");
const model = { supported: true, installed: true, on: true, running: true, model: "astrlink-guard", defaultScore: 0.8, minScore: 0.8, size: 39e6, folder: "/Users/someone/Library/Caches/magpie/pii" };

function serve(lang, theme, web) {
  const settings = { lang, theme, redact: true, redactPersonal: false };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme, web })};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/privacy-model") return json({ ...model, models: [model] });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

// the left of each box's ink in the card, in CSS px from the card's left
async function inkLefts(page, card, parts) {
  const cb = await card.boundingBox();
  const boxes = [];
  for (const p of parts) boxes.push(await p.boundingBox());
  const png = (await page.screenshot({ clip: cb })).toString("base64");
  return page.evaluate(async ({ png, cb, boxes }) => {
    const img = new Image();
    img.src = "data:image/png;base64," + png;
    await img.decode();
    const c = document.createElement("canvas");
    c.width = img.width; c.height = img.height;
    const x = c.getContext("2d");
    x.drawImage(img, 0, 0);
    const k = img.width / cb.width, d = x.getImageData(0, 0, c.width, c.height).data;
    return boxes.map((b) => {
      const x0 = Math.max(0, Math.round((b.x - cb.x) * k)), x1 = Math.min(c.width, Math.round((b.x + b.width - cb.x) * k));
      const y0 = Math.max(0, Math.round((b.y - cb.y) * k)), y1 = Math.min(c.height, Math.round((b.y + b.height - cb.y) * k));
      // the card's own colour, a little inside its left edge on this line
      const bi = (Math.round((y0 + y1) / 2) * c.width + Math.round(4 * k)) * 4;
      const bg = [d[bi], d[bi + 1], d[bi + 2]];
      for (let xx = x0; xx < x1; xx++) for (let y = y0; y < y1; y++) {
        const i = (y * c.width + xx) * 4;
        if (Math.abs(d[i] - bg[0]) + Math.abs(d[i + 1] - bg[1]) + Math.abs(d[i + 2] - bg[2]) > 90) return xx / k;
      }
      return -1;
    });
  }, { png, cb, boxes });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an Advanced fold in a card lines up with the card's labels", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    // the app at both widths, and a browser's narrow page, whose rows are 12px in
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const [width, web] of [[1280, false], [600, false], [600, true]]) for (const theme of ["light", "dark"]) {
      await t.test(`${lang} ${width}${web ? " web" : ""} ${theme}`, async (st) => {
        const ctx = await browser.newContext({ viewport: { width, height: 1400 }, deviceScaleFactor: 4, reducedMotion: "reduce", colorScheme: theme });
        st.after(() => ctx.close());
        const page = await ctx.newPage();
        page.setDefaultTimeout(15000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, theme, web));
        await page.goto("http://magpie.test/?view=settings&tab=privacy");
        const sum = page.locator("#pmFolderRow details.prefs-more > summary");
        await sum.waitFor();
        await page.mouse.move(0, 0);
        const card = page.locator("#pmFolderRow").locator("xpath=..");
        const label = page.locator("#pmScoreRow .name");
        const [chev, text] = await inkLefts(page, card, [sum, label]);
        const shape = await page.evaluate(() => {
          const box = document.getElementById("pmFolderRow"), s = box.querySelector("summary");
          return { last: box.parentNode.lastElementChild === box, rule: parseFloat(getComputedStyle(box).borderTopWidth), height: s.getBoundingClientRect().height };
        });
        if (process.env.SHOW) console.log(engine, lang, width, web, theme, chev, text, JSON.stringify(shape));
        assert(chev >= 0 && text >= 0, "nothing drawn");
        assert(Math.abs(chev - text) <= 0.75, `the chevron's ink starts at ${chev.toFixed(2)}px, the labels' at ${text.toFixed(2)}px`);
        assert(shape.last, "Advanced is the card's last line");
        assert.equal(shape.rule, 1, "Advanced is ruled off like a row");
        assert(shape.height >= 36, `the fold's line is ${shape.height}px tall`);
        // a draw replaced before its model was read (settings read again)
        // answers late, with its rows gone: the fold stays where it is
        const kept = await page.evaluate(() => {
          window.pmFolder(document.createElement("div"), document.createElement("div"), { supported: true, folder: "/x" }, () => {});
          return !!document.getElementById("pmFolderRow");
        });
        assert(kept, "a late answer for rows gone took the fold away");
        assert.deepEqual(errors, []);
      });
    }
  });
}
