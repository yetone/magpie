// Run with Node's test runner and Playwright on the module path; see README.md.
// A reply in flight shows, live, how much of it has arrived and roughly
// its tokens (#4951, Canda15): the request's row has the bytes, "≈ n
// tokens" once a rate is learned, and how long ago content last came,
// changed in place as the trace says more (the row isn't built again), and
// the account answering it says the same. Quiet for 15s, the account's dot
// stops pulsing and "answering…" goes. Once the request is done none of it
// stays. English, Chinese (simplified and traditional), Japanese and
// German, at 1100px and 420px; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const M = "glm-5";
const order = [{ id: "solo#s1", provider: "solo", name: "Solo", who: "solo-1", kind: "key", model: M }];
const ago = (s) => new Date(Date.now() - s * 1e3).toISOString();
// the request: in flight with the live numbers given, or done
const route = (live, done = false) => ({
  id: 2, seq: 2, time: ago(30), agent: "zcode", model: M, provider: "solo", done, ...(done ? { status: 200, ms: 30000, out: 900 } : {}),
  order, tries: [{ id: "solo#s1", model: M, start: ago(30), done, ...(done ? { status: 200, ms: 30000 } : {}) }],
  ...live,
});

function serve(lang, live) {
  return async (request) => {
    const url = new URL(request.request().url());
    const json = (data) => request.fulfill({ json: data });
    if (url.pathname === "/boot.js") return request.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return request.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "zcode", name: "ZCode", path: "/test/config.json", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) {
        if (live.queued.length) return json(live.queued.shift());
        live.pending.push(request);
        return;
      }
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: [route({ bytes: 2048, contentAt: Date.now() - 1000 })] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await request.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: a reply in flight shows what has arrived, and nothing once it is done`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await browser.newPage({ viewport: { width, height: 900 } });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (error) => errors.push(error.message));
        const live = { pending: [], queued: [], seq: 1 };
        await page.route("**/*", serve(lang, live));
        const send = (...routes) => {
          const d = { mine: true, now: new Date().toISOString(), seq: ++live.seq, totals: { requests: live.seq }, routes };
          const r = live.pending.shift();
          if (r) r.fulfill({ json: d }); else live.queued.push(d);
        };
        const say = (key, vars = {}) => page.evaluate(([lang, key, vars]) => {
          const s = (lang !== "en" && I18N[lang]?.[key]) || key;
          return s.replace(/\{(\w+)\}/g, (_, k) => vars[k] ?? `{${k}}`);
        }, [lang, key, vars]);
        const cells = () => page.evaluate(() => [...document.querySelectorAll(".rt-req .rt-metric.recv")].map((c) => c.textContent.trim()));
        const account = page.locator(".rt-accts li", { has: page.locator(".who", { hasText: /^solo-1$/ }) });

        await page.goto("http://magpie.test/?view=routing");
        await page.locator(".rt-req .rt-metric.recv").first().waitFor();

        // 2 KiB in, content a second ago, no rate learned: no tokens said
        let got = await cells();
        assert.equal(got.length, 2, JSON.stringify(got));
        assert.equal(got[0], "2.0 KiB");
        assert.ok(got[1].includes(await say("{n} s", { n: 1 })) || got[1].includes(await say("{n} s", { n: 2 })), got[1]);
        const answering = await say("answering…");
        await page.waitForFunction(() => /KiB/.test(document.querySelector(".rt-accts li.now em")?.textContent || ""));
        let em = (await account.locator("em").textContent()).trim();
        assert.ok(em.startsWith(answering) && em.includes("2.0 KiB"), em);

        // more arrives, a rate is learned: the same cells change in place
        await page.evaluate(() => { window.firstCell = document.querySelector(".rt-req .rt-metric.recv"); window.firstRow = document.querySelector(".rt-req"); });
        send(route({ bytes: 3 * 1048576, estTok: 1234, contentAt: Date.now() }));
        await page.waitForFunction(() => document.querySelectorAll(".rt-req .rt-metric.recv").length === 3);
        got = await cells();
        assert.equal(got[0], "3.0 MiB");
        assert.equal(got[1], await say("≈ {n} tokens", { n: "1.2k" }));
        assert.ok(await page.evaluate(() => document.querySelector(".rt-req") === window.firstRow), "the row was built again");
        send(route({ bytes: 3 * 1048576 + 600000, estTok: 1500, contentAt: Date.now() }));
        await page.waitForFunction(() => document.querySelector(".rt-req .rt-metric.recv .v")?.textContent === "3.6 MiB");
        assert.ok(await page.evaluate(() => document.querySelector(".rt-req .rt-metric.recv") === window.firstCell || document.querySelector(".rt-req") === window.firstRow));
        // each cell says what it is when hovered
        assert.equal(await page.locator(".rt-req .rt-metric.recv").first().getAttribute("title"), await say("How much of this reply has reached the client, framing included"));
        // the page body never scrolls sideways
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));

        // quiet for 20s: the dot stops, and the account says the numbers only
        send(route({ bytes: 3 * 1048576 + 600000, estTok: 1500, contentAt: Date.now() - 20000 }));
        await account.and(page.locator("li.quiet")).waitFor();
        assert.equal(await account.locator(".dot").evaluate((d) => getComputedStyle(d, "::after").animationName), "none");
        em = (await account.locator("em").textContent()).trim();
        assert.ok(!em.includes(answering) && em.includes("3.6 MiB"), em);
        assert.ok((await cells())[2].includes(await say("{n} s", { n: 20 })) || (await cells())[2].includes(await say("{n} s", { n: 21 })), (await cells())[2]);
        // the page's second moves "ago" on without a trace answer
        const before = (await cells())[2];
        await page.waitForFunction((b) => document.querySelectorAll(".rt-req .rt-metric.recv")[2]?.textContent.trim() !== b, before, { timeout: 4000 });

        // done: no live cells, nothing quiet, the account answered it
        send(route({ bytes: 3 * 1048576 + 700000 }, true));
        await page.waitForFunction(() => !document.querySelector(".rt-metric.recv"));
        assert.equal(await page.locator(".rt-accts li.quiet").count(), 0);
        em = (await account.locator("em").textContent()).trim();
        assert.ok(!/MiB/.test(em), em);
        assert.deepEqual(errors, []);
      });
    }
  }
}
