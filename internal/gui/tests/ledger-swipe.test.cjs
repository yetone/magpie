// Run with Node's test runner and Playwright on the module path; see README.md.
// A swipe across the Requests table on a phone (Pluviophile on Discord: 左右
// 滑动震颤, 粘滞卡顿). The table and the scrollbar kept at the window's foot
// (#799) follow each other, and the scroll a follow sets off is told a frame
// later in WebKit; a phone's finger has moved the table on by then, so the
// bar's echo wrote the table back where it was, every other frame. Here the
// table is moved between frames, as a phone's own scrolling moves it, and
// the page never writes it back; the same for the bar dragged. English and
// Chinese, Chromium and WebKit at a phone's size; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit, devices } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_LEDGER_ASSETS || path.join(__dirname, "../assets"));
const now = Date.now();
const ROWS = Array.from({ length: 40 }, (_, i) => ({
  route_id: 500 + i, t: new Date(now - (i + 1) * 60e3).toISOString(), agent: "trae", agentName: "Trae CN", icon: "generic",
  provider: "deepseek", providerName: "DeepSeek Harness", req: "group/auto-deepseek-v4-1-flash", model: "deepseek-v4-1-flash", served: "deepseek-v4-1-flash",
  in: 1200 + i, out: 80, ms: 2400, ttft_ms: 600, status: [200, 400, 499][i % 3], rid: "req_" + i, ep: "/v1/chat/completions", cost: 0.001, priced: true,
}));

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: { rate: 7.2, at: new Date().toISOString() } });
    if (url.pathname === "/api/usage/requests") return json({ period: url.searchParams.get("period"), rows: ROWS, offset: 0, total: ROWS.length, calls: ROWS.length, errors: 0, input: 1, output: 1, cost: 0.04, unpriced: 0, agents: [{ id: "trae", name: "Trae CN", icon: "generic" }] });
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

// every write the page makes to either box's scrollLeft is counted; the
// test's own moves go past the count, as a phone's scrolling does
function count() {
  const d = Object.getOwnPropertyDescriptor(Element.prototype, "scrollLeft");
  window.__writes = { ledWrap: 0, ledHScroll: 0 };
  Object.defineProperty(Element.prototype, "scrollLeft", {
    configurable: true,
    get() { return d.get.call(this); },
    set(v) { if (this.id in window.__writes) window.__writes[this.id]++; d.set.call(this, v); },
  });
  // moves one box 2.5px every 7ms, off the frame, for 60 frames, and says
  // where it stood each frame and how often the page wrote either box
  window.__swipe = (id) => new Promise((done) => {
    const box = document.getElementById(id), seen = [];
    let x = 0;
    d.set.call(box, 0);
    const at = () => d.get.call(box);
    requestAnimationFrame(() => {
      window.__writes.ledWrap = window.__writes.ledHScroll = 0;
      const move = setInterval(() => { x += 2.5; d.set.call(box, x); }, 7);
      const frame = () => {
        seen.push(at());
        if (seen.length < 60) return requestAnimationFrame(frame);
        clearInterval(move);
        done({ seen, writes: { ...window.__writes } });
      };
      requestAnimationFrame(frame);
    });
  });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a swipe across the Requests table on a phone", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const phone = { ...devices["iPhone 13"] };
    delete phone.defaultBrowserType;
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const ctx = await browser.newContext({ ...phone, reducedMotion: "reduce" });
        t.after(() => ctx.close());
        const p = await ctx.newPage();
        p.setDefaultTimeout(5000);
        p.setDefaultNavigationTimeout(60000);
        const errors = [];
        p.on("pageerror", (e) => errors.push(e.message));
        await p.addInitScript(count);
        await p.route("**/*", server(lang));
        await p.goto("http://magpie.test/?view=usage");
        await p.waitForSelector("#view-usage", { state: "visible" });
        await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
        await p.locator("#ledWrap .led tbody tr").first().waitFor();
        await p.waitForTimeout(300);
        assert(await p.locator("#ledWrap").evaluate((e) => e.scrollWidth > e.clientWidth + 100), "the table is wider than the phone");
        assert.equal(await p.locator("#ledHScroll").isHidden(), false, "the scrollbar is kept at the foot");

        // the table swiped: it is never written back, and never stands still
        // a frame while the finger moves it; the bar follows it
        const table = await p.evaluate(() => window.__swipe("ledWrap"));
        const stood = table.seen.filter((x, i) => i && x <= table.seen[i - 1]).length;
        assert.equal(table.writes.ledWrap, 0, "the page wrote the swiped table back: " + JSON.stringify(table));
        assert.equal(stood, 0, "the table stood still or went back under the finger: " + JSON.stringify(table.seen));
        await p.waitForTimeout(100);
        const [w, b] = await p.evaluate(() => [document.getElementById("ledWrap").scrollLeft, document.getElementById("ledHScroll").scrollLeft]);
        assert(w > 100 && w === b, "the bar followed the table: " + JSON.stringify({ w, b }));

        // the bar dragged: it is never written back, and the table follows it
        const bar = await p.evaluate(() => window.__swipe("ledHScroll"));
        assert.equal(bar.writes.ledHScroll, 0, "the page wrote the dragged bar back: " + JSON.stringify(bar));
        await p.waitForTimeout(100);
        const [w2, b2] = await p.evaluate(() => [document.getElementById("ledWrap").scrollLeft, document.getElementById("ledHScroll").scrollLeft]);
        assert(b2 > 100 && w2 === b2, "the table followed the bar: " + JSON.stringify({ w2, b2 }));
        assert.deepEqual(errors, []);
      });
    }
  });
}
