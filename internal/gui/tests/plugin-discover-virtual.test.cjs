// Run with Node's test runner and Playwright on the module path; see README.md.
// Discover with a market of 500 plugins stays light: a kind picked (369
// subscriptions) or a search that matches hundreds draws only the cards
// near the view, while the list keeps its whole height. Scrolled to the
// bottom the last card is there and its Install works; a card with focus
// stays in the page while the reader scrolls away; a narrow search still
// lists every match. Every language, both engines, wide and narrow; the API
// is faked. PERF=1 prints how long a kind takes to draw.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));

// 370 subscriptions and the rest, the most downloaded first: pv-0 … pv-369
const KINDS = [["pv", 370, null], ["mw", 50, "middleware"], ["ag", 48, "agent"], ["tl", 8, "tools"], ["pg", 27, "page"]];
const listings = [];
for (const [k, n, kind] of KINDS) for (let i = 0; i < n; i++) {
  const l = { package: `@x/${k}-${i}`, name: `${k}-${i}`, community: i % 3 === 0, summary: { en: `Plugin ${k} number ${i}, a summary long enough to take two lines of a card at its narrowest width` }, npm: { version: "0.1." + i, weekly: 100000 - i } };
  if (kind) l.kind = kind; else l.providers = [`${k}${i}`];
  listings.push(l);
}
const npm = Object.fromEntries(listings.map((l) => [l.package, l.npm]));
const state = { bun: true, bunVersion: "1.3.0", plugins: [] };
const added = [];

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
    if (url.pathname === "/api/plugins/listings") return json({ listings });
    if (url.pathname === "/api/plugins/github") return json({ listings: [] });
    if (url.pathname === "/api/plugins/npm") return json({ npm });
    if (url.pathname === "/api/plugins/search") return json({ hits: [] });
    if (url.pathname === "/api/plugins/add") { added.push(route.request().postDataJSON()?.spec); return json({}); }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = { en: "Plugins", zh: "插件", "zh-TW": "外掛", ja: "プラグイン", de: "Erweiterungen" };
// a page of cards and the rows around it, never the whole market
const MOST = 72;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Discover draws the cards near the view, not all 500", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of (process.env.LANGS ? process.env.LANGS.split(",") : Object.keys(L))) for (const width of [1180, 420]) {
      await t.test(lang + " " + width, async () => {
        const page = await (await browser.newContext({ viewport: { width, height: 820 } })).newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/?view=plugins");
        const view = page.locator("#view-plugins");
        await view.locator(".pm-kinds .opt").nth(1).waitFor();
        // npm's numbers in, so nothing redraws under the measure
        await view.locator(`.pm-card[data-pkg="@x/pv-0"] .pm-act.get`).waitFor();

        // Subscriptions, timed from the click to the cards drawn
        const ms = await page.evaluate(() => {
          const t0 = performance.now();
          document.querySelectorAll("#view-plugins .pm-kinds .opt")[1].click();
          document.body.offsetHeight;
          return performance.now() - t0;
        });
        const sec = view.locator('.pm-sec[data-kind="pv"]');
        await sec.waitFor();
        await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
        const cards = await sec.locator(".pm-card").count();
        const nodes = await page.evaluate(() => document.querySelectorAll("#view-plugins *").length);
        if (process.env.PERF) console.log(`${engine} ${lang} ${width}: Subscriptions drawn in ${ms.toFixed(1)}ms, ${cards} cards, ${nodes} nodes`);
        assert.ok(cards > 0 && cards <= MOST, `${cards} of 370 cards in the page`);
        // the list keeps its whole height: 370 cards' rows
        const geo = await sec.locator(".pm-grid").evaluate((g) => {
          const cols = getComputedStyle(g).gridTemplateColumns.split(" ").length;
          return { cols, h: g.getBoundingClientRect().height, card: g.querySelector(".pm-card").getBoundingClientRect().height };
        });
        const rows = Math.ceil(370 / geo.cols);
        assert.ok(geo.h >= rows * geo.card, `the list is ${geo.h}px for ${rows} rows of ${geo.card}px`);

        // a card with focus stays while the reader scrolls away from it
        const first = sec.locator('.pm-card[data-pkg="@x/pv-0"]');
        await first.focus();
        const inView = () => page.evaluate(() => {
          const p = document.querySelector("#view-plugins").getBoundingClientRect();
          return [...document.querySelectorAll('#view-plugins .pm-sec[data-kind="pv"] .pm-card')]
            .filter((c) => { const r = c.getBoundingClientRect(); return r.bottom > p.top && r.top < p.bottom; }).map((c) => c.dataset.pkg);
        });
        // wheeled as a reader does (the app puts back a scroll that isn't
        // the reader's), each step landed before the next; at every stop
        // the view has cards in it, never an empty stretch
        const box = await view.boundingBox();
        await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
        const scrollTo = async (f) => {
          for (let i = 0; i < 200; i++) {
            const [top, max] = await view.evaluate((v) => [v.scrollTop, v.scrollHeight - v.clientHeight]);
            const want = Math.round(max * f);
            if (Math.abs(want - top) < 2 || (f === 1 && top >= max - 1)) break;
            await page.mouse.wheel(0, Math.max(-2500, Math.min(2500, want - top)));
            for (let k = 0; k < 50 && (await view.evaluate((v) => v.scrollTop)) === top; k++) await page.waitForTimeout(20);
            await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
            assert.ok((await inView()).length > 0, "an empty stretch at " + (await view.evaluate((v) => v.scrollTop)) + "px");
          }
          await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
        };
        await scrollTo(0.5);
        const mid = await inView();
        assert.ok(mid.length > 0, "cards in the view halfway down");
        assert.ok(await page.evaluate(() => document.activeElement?.dataset?.pkg === "@x/pv-0" && document.activeElement.isConnected), "the focused card is kept");
        assert.ok((await sec.locator(".pm-card").count()) <= MOST + 1);

        // the bottom: the last card, and its Install works
        await scrollTo(1);
        const end = await inView();
        assert.ok(end.includes("@x/pv-369"), "the last card at the bottom: " + end.join(" "));
        added.length = 0;
        await sec.locator('.pm-card[data-pkg="@x/pv-369"] .pm-act.get').click();
        for (let i = 0; i < 100 && !added.length; i++) await page.waitForTimeout(20);
        assert.deepEqual(added, ["@x/pv-369"]);

        // a search: a few matches are all there; hundreds are drawn near the view
        await scrollTo(0);
        const q = view.locator(".pm-find input");
        await q.fill("pv-12");
        const hits = view.locator(".pm-sec").filter({ has: page.locator(".pm-sechead h3", { hasText: new RegExp("^" + L[lang] + "$") }) });
        await hits.locator('.pm-card[data-pkg="@x/pv-129"]').waitFor();
        assert.equal(await hits.locator(".pm-card").count(), 11);
        await q.fill("pv");
        await hits.locator('.pm-card[data-pkg="@x/pv-0"]').waitFor();
        await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
        const many = await hits.locator(".pm-card").count();
        assert.ok(many > 0 && many <= MOST, `${many} of 370 matches in the page`);

        const over = await page.evaluate(() => document.scrollingElement.scrollWidth - document.scrollingElement.clientWidth);
        assert.ok(over <= 0, "page scrolls sideways by " + over);
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
