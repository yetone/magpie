// Run with Node's test runner and Playwright on the module path; see README.md.
// Discover's kinds (the owner, 11-23: 切换下面的 Tab 的时候，不要直接把上面
// 的推荐榜删掉，而是丝滑地滚动到下面去). Picking a kind, or a shelf's See
// all, keeps the week's picks and glides the page until the kind bar is at
// its top: the page passes through the way there, the picks are still above,
// and a short kind gets room to reach the top. All again leaves the page
// where it is, and the reader can scroll back up to the picks. With reduced
// motion it goes there at once. Every language, wide and narrow, both
// engines; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const names = ["Alpha", "Beta", "Gamma", "Delta", "Epsilon", "Zeta", "Eta", "Theta"];
const pkg = (n) => "@magpie-community/opencode-" + n.toLowerCase() + "-auth";
// a long All (fillers), and a short kind (one tool) that, picked deep in
// All, leaves the page shorter than where it was
const fillers = Array.from({ length: 40 }, (_, i) => "Filler" + i);
const listings = () => [
  ...[...names, ...fillers].map((n) => ({ package: pkg(n), name: n, providers: [n.toLowerCase()], community: true, summary: { en: n + "." } })),
  { package: "@magpie-community/opencode-omega-tools", name: "Omega", kind: "tools", community: true, summary: { en: "Omega." } },
];
const npm = Object.fromEntries(names.map((n, i) => [pkg(n), { version: "0.1.0", weekly: 100 * (i + 1) }]));
const repos = [];
const counts = Object.fromEntries(names.map((n, i) => [pkg(n), { users: 1000 - i * 100, likes: 40 - i * 4 }]));
const state = { bun: true, bunVersion: "1.3.0", plugins: [] };
const live = true;

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/plugins") return json(state);
    if (url.pathname === "/api/plugins/listings") return json({ listings: listings() });
    if (url.pathname === "/api/plugins/market") return json({ listings: listings(), state });
    if (url.pathname === "/api/plugins/counts") return live ? json({ plugins: counts, liked: [] }) : route.fulfill({ status: 500, json: { error: "offline" } });
    if (url.pathname === "/api/plugins/npm") {
      const out = {};
      for (const n of (url.searchParams.get("names") || "").split(",")) if (npm[n]) out[n] = npm[n];
      return json({ npm: out });
    }
    if (url.pathname === "/api/plugins/github") return json({ repos, topic: "magpie-plugin" });
    if (url.pathname === "/api/plugins/search") return json({ hits: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.hostname !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const langs = ["en", "zh", "zh-TW", "ja", "de"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a kind picked keeps the week's picks and glides the kind bar to the top", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const cases = [];
    for (const lang of langs) for (const width of [1180, 420]) cases.push([lang, width, "no-preference"]);
    cases.push(["en", 1180, "reduce"]);
    for (const [lang, width, motion] of cases) for (const how of ["kind", "see all", "short"]) {
      await t.test(`${lang} ${width} ${motion} ${how}`, async () => {
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: motion });
        const page = await ctx.newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], refused = [];
        page.on("pageerror", (e) => errors.push(e.message));
        page.on("console", (m) => m.text().includes("not asked for") && refused.push(m.text()));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/?view=plugins");
        const view = page.locator("#view-plugins");
        await view.locator(".pm-recs .pm-hero").waitFor();
        await view.locator(".pm-recs .pm-rank").first().waitFor();
        await view.locator(".pm-sec[data-kind] .pm-card").first().waitFor();
        // where the bar is against the page's top (its padding), and the page's scroll
        // (narrow, against the bottom of the tabs stuck over the page, and
        // their strip 6px below them; 4px of air)
        const at = () => view.evaluate((v) => {
          const bar = v.querySelector(".pm-bar"), head = v.querySelector(":scope > .pm-head");
          const pad = parseFloat(getComputedStyle(v).paddingTop) || 0;
          const top = head && getComputedStyle(head).position === "sticky" && head.classList.contains("stuck") ? head.getBoundingClientRect().bottom + 6 + 4 : v.getBoundingClientRect().top + pad;
          return { bar: bar.getBoundingClientRect().top - top, top: v.scrollTop };
        });
        // nothing lies over the bar's tabs
        const open = () => view.evaluate((v) => [...v.querySelectorAll(".pm-kinds .opt")].every((o) => {
          const r = o.getBoundingClientRect(), x = Math.min(Math.max(r.left + r.width / 2, 1), innerWidth - 1);
          return r.right < 0 || r.left > innerWidth || o.contains(document.elementFromPoint(x, r.top + r.height / 2));
        }));
        assert.equal((await at()).top, 0);
        assert.ok((await at()).bar > 100, "the picks are above the bar");
        // the reader wheels down until the bar shows (narrow, the picks
        // stack taller than the window)
        await view.hover();
        const vh = await view.evaluate((v) => v.clientHeight);
        // (or, for See all, until the first shelf's See all shows)
        const target = how === "kind" ? ".pm-bar" : ".pm-sec[data-kind] .pm-more";
        const where = () => view.evaluate((v, sel) => v.querySelector(sel).getBoundingClientRect().top - v.getBoundingClientRect().top, target);
        for (let i = 0; i < 30 && (await where()) > vh - 120; i++) await page.mouse.wheel(0, 200);
        // (short: on down into All, far past where a lone tool's page ends)
        if (how === "short") for (let i = 0; i < 30 && (await at()).top < 1600; i++) await page.mouse.wheel(0, 300);
        // (narrow, that takes the bar above the view: back up until it shows)
        if (how === "short") for (let i = 0; i < 40 && (await where()) < vh / 3; i++) await page.mouse.wheel(0, -100);
        await page.waitForTimeout(300);
        const start = (await at()).top;
        assert.ok((await where()) > 0 && (await where()) < vh - 40, "what is pressed shows");
        // every scrollTop on the way, a frame at a time
        await view.evaluate((v) => {
          window.seen = [];
          const tick = () => { window.seen.push(v.scrollTop); if (window.seen.length < 240) requestAnimationFrame(tick); };
          requestAnimationFrame(tick);
        });
        // pressed where it is, as a reader does: Playwright's own click
        // scrolls it into view first (WebKit centres it), a scroll no
        // reader makes, which the view then puts back
        const press = async (sel) => {
          const b = await view.locator(sel).first().evaluate((e) => {
            e.closest(".pm-kinds")?.scrollTo({ left: Math.max(0, e.offsetLeft - 8) });
            const r = e.getBoundingClientRect();
            return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
          });
          await page.mouse.click(b.x, b.y);
        };
        await press(how === "kind" ? ".pm-kinds .opt[data-seg=pv]" : how === "short" ? ".pm-kinds .opt[data-seg=tl]" : ".pm-sec[data-kind] .pm-more");
        await page.waitForFunction(() => {
          const v = document.querySelector("#view-plugins"), s = window.seen;
          return s.length > 12 && s.slice(-6).every((x) => x === v.scrollTop) && s.some((x) => x !== s[0]);
        }, null, { timeout: 6000 });
        const end = await at();
        assert.ok(Math.abs(end.bar) <= 2, `the bar ends at the top (${end.bar}px off)`);
        assert.ok(await open(), "nothing covers the bar's tabs");
        assert.equal(await view.locator(".pm-recs .pm-hero").count(), 1, "the picks are kept");
        assert.equal(await view.locator(".pm-kinds .opt.on").getAttribute("data-seg") !== "", true);
        const seen = await page.evaluate(() => window.seen);
        const lo = Math.min(start, end.top), hi = Math.max(start, end.top);
        assert.ok(hi - lo > 40, "the page had a way to go");
        assert.ok(seen.every((x) => x >= lo - 1 && x <= hi + 1), `it goes straight there (${start} to ${end.top}: ${[...new Set(seen)].join(",")})`);
        const between = new Set(seen.filter((x) => x > lo + 1 && x < hi - 1)).size;
        if (motion === "reduce") assert.equal(between, 0, "reduced motion goes there at once");
        else assert.ok(between >= 3, `it glides, not jumps (${between} frames on the way)`);
        // All again: the page stays where it is
        await press(".pm-kinds .opt[data-seg='']");
        await page.waitForFunction((top) => Math.abs(document.querySelector("#view-plugins").scrollTop - top) <= 2, end.top);
        assert.ok(Math.abs((await at()).bar) <= 2, "All leaves the bar where it was");
        // and the reader can scroll back up to the picks
        await view.hover();
        for (let i = 0; i < 20 && (await at()).top > 0; i++) await page.mouse.wheel(0, -400);
        await page.waitForFunction(() => document.querySelector("#view-plugins").scrollTop === 0);
        assert.deepEqual(refused, []);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
