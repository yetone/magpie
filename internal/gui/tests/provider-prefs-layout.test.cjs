// Run with Node's test runner and Playwright on the module path; see README.md.
// Xia5ai on X: in Claude's provider editor, Advanced, the short system
// prompt's Off/On switch stood stacked one above the other, off its row's
// right edge, and the card explaining "Warm up now" showed at the window's
// left edge, far from its row. Both engines, at 1100 and 420, in every language.
// - the switch's buttons share a line and end where its siblings' controls end;
// - the card of a row's explanation is under or over that row, not at the window's edge.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { provPrefsProviders, openProvPrefs } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function serve(lang) {
  const settings = { theme: "light", lang, currency: "usd", guideHidden: true, claudeUsers: ["ann@example.test"], claudeWarmup: "week" };
  const providers = provPrefsProviders(["claude"], { claude: "Claude Code" });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      test(`${engine} ${width} ${lang}: Claude's short prompt switch is one line, and a row's card sits at its row`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message) && !(engine === "webkit" && /due to access control checks/.test(e.message))) errors.push(e.message); });
        await page.addInitScript(() => Object.defineProperty(navigator, "platform", { get: () => "MacIntel" }));
        await page.route("**/*", serve(lang));
        await openProvPrefs(page, "claude");

        // the switch: its buttons on one line, ending where the Warm up row's switch ends
        const lean = page.locator("#claudeLeanRow");
        await lean.scrollIntoViewIfNeeded();
        const opts = await lean.locator(".segs .opt").evaluateAll((os) => os.map((o) => { const r = o.getBoundingClientRect(); return { top: Math.round(r.top), bottom: Math.round(r.bottom), right: Math.round(r.right) }; }));
        assert.equal(opts.length, 2);
        assert.equal(opts[0].top, opts[1].top, `the buttons are stacked: ${JSON.stringify(opts)}`);
        const sib = await page.locator("#claudeWarmList .row.pref .segs").first().evaluate((e) => Math.round(e.getBoundingClientRect().right));
        assert.ok(Math.abs(opts[1].right - sib) <= 12, `the switch ends at ${opts[1].right}, its sibling's at ${sib}`);

        // the card of Warm up now, resting the pointer on its name
        const row = page.locator("#claudeWarmNowRow");
        await row.scrollIntoViewIfNeeded();
        await page.mouse.move(1, 1);
        await row.locator(":scope > .who > .name").hover();
        const card = page.locator(".set-hint.on");
        await card.waitFor();
        const name = await row.locator(":scope > .who > .name").boundingBox(), cb = await card.boundingBox();
        assert.ok(Math.abs(cb.x - name.x) <= 24 || width < 500 && cb.x >= 15, `the card is at x=${cb.x}, its row's name at x=${name.x}`);
        assert.ok(cb.y >= name.y + name.height - 2 && cb.y - name.y < 80 || cb.y + cb.height <= name.y + 2 && name.y - (cb.y + cb.height) < 80, `the card is at y=${cb.y}, its row's name at y=${name.y}`);
        assert.ok(cb.x >= 15 && cb.x + cb.width <= width - 15, `the card runs out of the window: ${JSON.stringify(cb)}`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
