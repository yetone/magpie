// Run with Node's test runner and Playwright on the module path; see README.md.
// The Usage page's Requests table is wider than a narrow window, and its own
// scrollbar is at its foot, fifty rows down (mintonight, #799): a scrollbar
// stands in for it at the bottom of the window while the table is in sight,
// moves the table sideways and follows it, and is gone when the table fits.
// A row's details say how it went and how long it took, and link to its
// routing, so neither needs the columns out of sight. English and Chinese;
// no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();

const ROWS = Array.from({ length: 40 }, (_, i) => ({
  route_id: 500 + i, t: new Date(now - (i + 1) * 60e3).toISOString(), agent: "trae", agentName: "Trae CN", icon: "generic",
  provider: "deepseek", providerName: "DeepSeek Harness", req: "group/auto-deepseek-v4-1-flash", model: "deepseek-v4-1-flash", served: "deepseek-v4-1-flash",
  in: 1200 + i, out: 80, ms: 2400, ttft_ms: 600, status: 200, rid: "req_" + i, ep: "/v1/chat/completions", cost: 0.001, priced: true,
}));

function server(lang, asked) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status) => route.fulfill({ json: data, status });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: { rate: 7.2, at: new Date().toISOString() } });
    if (url.pathname === "/api/usage/requests") return json({ period: url.searchParams.get("period"), rows: ROWS, offset: 0, total: ROWS.length, calls: ROWS.length, errors: 0, input: 1, output: 1, cost: 0.04, unpriced: 0, agents: [{ id: "trae", name: "Trae CN", icon: "generic" }] });
    if (url.pathname === "/api/gateway/route") { asked.push(url.searchParams.get("id")); return json({ error: "gone" }, 404); }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 40, errors: 0, input: 1, output: 1, reasoning: 0, unpriced: 0, cost: 0.04, bucket: "day", series: [], agents: [], models: [], path: "" });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const L = {
  en: { labels: ["Status", "Duration", "Routing"], values: ["200", "2.4 s", "View routing"] },
  zh: { labels: ["状态", "耗时", "路由"], values: ["200", "2.4 秒", "查看路由"] },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Requests table's scrollbar and a row's status", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium", ignoreDefaultArgs: ["--hide-scrollbars"] }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      const w = L[lang];
      await t.test(lang, async () => {
        const errors = [], asked = [];
        // narrower than the table even with the columns a narrow window
        // leaves out (#860), by a margin no machine's fonts take away: at
        // 700px the Chinese table was 76px wider here and 33px on another
        // machine, whose narrower fonts failed the check below (#1340)
        const ctx = await browser.newContext({ viewport: { width: 600, height: 640 }, reducedMotion: "reduce" });
        const p = await ctx.newPage();
        p.setDefaultTimeout(5000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", server(lang, asked));
        await p.goto("http://magpie.test/");
        await p.locator('[data-view="usage"]').first().click();
        await p.locator("#usageTab .opt").nth(1).click();
        await p.locator("#ledWrap .led tbody tr").first().waitFor();
        await p.waitForTimeout(200);

        const wrap = p.locator("#ledWrap"), bar = p.locator("#ledHScroll");
        assert(await wrap.evaluate((e) => e.scrollWidth > e.clientWidth + 50), "the table is wider than the window");
        // the table's top rows are in sight, its foot is not, and the bar is
        // at the bottom of the window, under the rows in sight
        await reader.inView(p, p.locator(".led tbody tr.led-row").nth(2));
        const view = await p.locator("#view-usage").boundingBox();
        const wb = await wrap.boundingBox();
        assert(wb.y + wb.height > view.y + view.height + 100, "the table's foot is out of sight");
        assert(await bar.isVisible(), "no scrollbar in sight");
        const bb = await bar.boundingBox();
        assert(bb.y + bb.height <= view.y + view.height + 1 && bb.y + bb.height >= view.y + view.height - 20, "the bar is at the window's foot: " + JSON.stringify({ bb, view }));
        assert(Math.abs(bb.width - wb.width) <= 2, "the bar is as wide as the table's box");
        // it draws a thumb: wider content than itself
        assert(await bar.evaluate((e) => e.scrollWidth > e.clientWidth && e.offsetHeight > e.clientHeight), "the bar draws a scrollbar");

        // dragged to the end, the table shows its last column, Status
        await bar.evaluate((e) => { e.scrollLeft = e.scrollWidth; });
        await p.waitForTimeout(150);
        assert.equal(await wrap.evaluate((e) => Math.round(e.scrollLeft)), await wrap.evaluate((e) => e.scrollWidth - e.clientWidth), "the table followed the bar");
        const st = await p.locator(".led tbody tr.led-row").nth(2).locator(".st").boundingBox();
        assert(st.x + st.width <= wb.x + wb.width + 1 && st.x >= wb.x, "Status is in sight");
        // the table scrolled itself (a trackpad's swipe): the bar follows
        await wrap.evaluate((e) => { e.scrollLeft = 30; });
        await p.waitForTimeout(150);
        assert.equal(await bar.evaluate((e) => Math.round(e.scrollLeft)), 30, "the bar followed the table");

        // a row's details: how it went, how long, and its routing
        await reader.click(p, p.locator(".led tbody tr.led-row").nth(2).locator("td").nth(2));
        const d = p.locator(".led tbody tr.led-detail");
        assert.deepEqual((await d.locator("dt").allTextContents()).slice(0, 3), w.labels);
        assert.deepEqual((await d.locator("dd").allTextContents()).slice(0, 3), w.values);
        // brought above the bar, which covers the window's last few pixels
        await reader.inView(p, d.locator(".led-route-link"));
        await p.mouse.wheel(0, 80);
        await p.waitForTimeout(300);
        await d.locator(".led-route-link").click();
        for (let i = 0; i < 40 && !asked.length; i++) await p.waitForTimeout(25);
        assert.deepEqual(asked, ["502"], "the link asks for the row's route");
        assert.equal(await p.locator(".led tbody tr.led-detail").count(), 1, "the link didn't close the row");

        // a window wide enough for the table has no bar
        await p.setViewportSize({ width: 2200, height: 640 });
        await p.waitForTimeout(250);
        assert(await wrap.evaluate((e) => e.scrollWidth <= e.clientWidth + 1), "the table fits");
        assert.equal(await bar.isVisible(), false, "a bar with nothing to scroll");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
