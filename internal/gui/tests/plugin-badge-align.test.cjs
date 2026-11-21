// Run with Node's test runner and Playwright on the module path; see README.md.
// The magpie community badge's shield sits level with its words (the owner,
// 11-21: 「icon 和文字没有水平对齐」): on a card and on Today's pick the
// shield's ink is centred on the words' ink within 1px, and so is the card's
// downloads arrow. Measured in pixels of a screenshot, not from line boxes, so a font's
// leading or a CJK fallback can't hide an offset. Every language, both
// engines, at a narrow width too; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const RIVER = "@magpie-community/opencode-river-auth";
const KILO = "@magpie-community/agent-kilo";
const listings = () => [
  { package: RIVER, name: "River", providers: ["river"], community: true, summary: { en: "River pay-as-you-go inference." } },
  { package: KILO, name: "Kilo Code", kind: "agent", community: true, summary: { en: "Kilo Code on magpie's Agents page." } },
];
const npm = { [RIVER]: { version: "0.1.3", weekly: 40 }, [KILO]: { version: "0.1.0", weekly: 3 } };
const state = { bun: true, bunVersion: "1.3.0", plugins: [] };

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugins") return json(state);
    if (url.pathname === "/api/plugins/listings") return json({ listings: listings() });
    if (url.pathname === "/api/plugins/market") return json({ listings: listings(), state });
    if (url.pathname === "/api/plugins/npm") {
      const out = {};
      for (const n of (url.searchParams.get("names") || "").split(",")) if (npm[n]) out[n] = npm[n];
      return json({ npm: out });
    }
    if (url.pathname === "/api/plugins/search") return json({ hits: [] });
    if (url.pathname === "/api/plugins/github") return json({ repos: [] });
    if (url.pathname === "/api/plugins/page") return json({ readme: "# River", updated: "2026-10-30T00:00:00Z" });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.hostname !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// the rows of ink in a box of the page: pixels that differ from the box's
// own background (its top-left corner), as page y, centre of the first and
// last such row
async function inkMiddle(page, box) {
  const scale = 4;
  const shot = await page.screenshot({ clip: box, scale: "device" });
  return page.evaluate(async ({ b64, scale }) => {
    const img = new Image();
    img.src = "data:image/png;base64," + b64;
    await img.decode();
    const c = document.createElement("canvas");
    c.width = img.width; c.height = img.height;
    const g = c.getContext("2d");
    g.drawImage(img, 0, 0);
    const d = g.getImageData(0, 0, c.width, c.height).data;
    const bg = [d[0], d[1], d[2]];
    let lo = -1, hi = -1;
    for (let y = 0; y < c.height; y++) for (let x = 0; x < c.width; x++) {
      const i = (y * c.width + x) * 4;
      if (Math.abs(d[i] - bg[0]) + Math.abs(d[i + 1] - bg[1]) + Math.abs(d[i + 2] - bg[2]) > 90) { if (lo < 0) lo = y; hi = y; break; }
    }
    return lo < 0 ? null : (lo + hi + 1) / 2 / scale;
  }, { b64: shot.toString("base64"), scale });
}

// the shield's and the words' ink middles, in page px
async function offset(page, badge) {
  const parts = await badge.evaluate((v) => {
    const r = (e) => { const b = e.getBoundingClientRect(); return { x: b.left, y: b.top, width: b.width, height: b.height }; };
    const words = v.querySelector(":scope > span");
    const rg = document.createRange(); rg.selectNodeContents(words);
    const w = rg.getBoundingClientRect();
    const s = r(v.querySelector("svg"));
    // the words' box reaches a little past their line, for ink that overhangs
    return { svg: s, words: { x: w.left, y: w.top - 3, width: Math.min(w.width, v.getBoundingClientRect().right - w.left), height: w.height + 6 } };
  });
  const icon = await inkMiddle(page, parts.svg);
  const text = await inkMiddle(page, parts.words);
  assert.ok(icon !== null && text !== null, "ink found");
  return { icon: parts.svg.y + icon, text: parts.words.y + text };
}

const LANGS = ["en", "zh", "zh-TW", "ja", "de"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the community badge's shield and the downloads arrow are level with their words", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of LANGS) for (const width of [1180, 420]) {
      await t.test(lang + " " + width, async () => {
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, deviceScaleFactor: 4 })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/?view=plugins");
        const view = page.locator("#view-plugins");
        await view.locator(`.pm-card[data-pkg="${KILO}"] .pm-ver`).waitFor();
        await page.evaluate(() => document.fonts.ready);
        const where = [
          ["card", view.locator(`.pm-card[data-pkg="${RIVER}"] .pm-verified`)],
          ["today's pick", view.locator(".pm-hero .pm-verified")],
          // the card's downloads beside it: the same icon and words
          ["downloads", view.locator(`.pm-card[data-pkg="${RIVER}"] .pm-dl`)],
        ];
        const seen = [];
        for (const [name, badge] of where) {
          await badge.scrollIntoViewIfNeeded();
          const o = await offset(page, badge);
          seen.push([name, o.icon - o.text]);
        }
        if (process.env.BADGE_LOG) console.log(engine, lang, width, seen.map(([n, d]) => n + " " + d.toFixed(2)).join(", "));
        for (const [name, d] of seen) assert.ok(Math.abs(d) <= 1, `${name}: the icon's middle is ${d.toFixed(2)}px from its words' middle`);
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
