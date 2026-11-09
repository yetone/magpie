// Run with Node's test runner and Playwright on the module path; see README.md.
// The Usage page's Requests is a plain list, and a click on a request opens
// that one request on a page of its own (geesehowardt7 on X: the click took
// them to Routing, where they couldn't find the request they had clicked).
// The page says where the request came from: the address the gateway saw,
// "This computer" for a loopback one, "Not recorded" for a row written before
// magpie kept it. Routing is only a link on the page. Requests ‹ and Escape
// go back to the list where it was left, and at the narrowest window nothing
// scrolls sideways. Every language; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const at = (n) => new Date(now - (n + 1) * 60e3).toISOString();
const gw = { agent: "codex", agentName: "Codex", icon: "codex-color", provider: "relay", providerName: "Relay", host: "team", req: "sol", model: "gpt-6-sol", served: "gpt-6-sol", in: 300, out: 40, ms: 900, status: 200, cost: 0.01, priced: true };

const ROWS = [
  // from this computer, with its route still in Routing
  { ...gw, t: at(0), client_ip: "127.0.0.1", route_id: 7, providerAccount: "team@example.com" },
  // from another computer on the network
  { ...gw, t: at(1), client_ip: "192.168.1.20", model: "gpt-6-luna", served: "gpt-6-luna" },
  // written before magpie kept the address
  { ...gw, t: at(2), model: "gpt-6-terra", served: "gpt-6-terra" },
  ...Array.from({ length: 40 }, (_, i) => ({ ...gw, t: at(3 + i), client_ip: i === 30 ? "::1" : "10.0.0." + (i + 1), model: "filler-" + i, served: "filler-" + i })),
];
const FAR = 33; // the ::1 row, below the fold

function server(lang, lookups) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: { rate: 7.2, at: new Date().toISOString() } });
    if (url.pathname.startsWith("/api/gateway/route")) { lookups.push(url.pathname); return json({}); }
    if (url.pathname === "/api/usage/requests") return json({ period: url.searchParams.get("period"), rows: ROWS, offset: 0, total: ROWS.length, calls: ROWS.length, errors: 0, input: 0, output: 0, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0.4, unpriced: 0, agents: [{ id: "codex", name: "Codex", icon: "codex-color" }] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: ROWS.length, errors: 0, input: 0, output: 0, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 0, cost: 0.4, bucket: "day", series: [], agents: [], models: [], path: "~/.config/magpie/usage.jsonl" });
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
  en: { ip: "Source IP", here: "This computer", none: "Not recorded", routing: "View routing" },
  zh: { ip: "来源 IP", here: "本机", none: "未记录", routing: "查看路由" },
  "zh-TW": { ip: "來源 IP", here: "本機", none: "未記錄" },
  ja: { ip: "送信元 IP", here: "このコンピュータ", none: "記録なし" },
  de: { ip: "Quell-IP", here: "Dieser Computer", none: "Nicht erfasst" },
};

// the value beside a label on the request's page
const value = (d, label) => d.locator("dt").evaluateAll((dts, label) => dts.find((dt) => dt.textContent === label)?.nextElementSibling?.textContent ?? null, label);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a request's own page", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) {
      const w = L[lang];
      await t.test(lang, async () => {
        const errors = [], lookups = [];
        const ctx = await browser.newContext({ viewport: { width: 1180, height: 640 }, reducedMotion: "reduce" });
        const p = await ctx.newPage();
        p.setDefaultTimeout(5000);
        p.setDefaultNavigationTimeout(60000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", server(lang, lookups));
        await p.goto("http://magpie.test/");
        await p.locator('[data-view="usage"]').first().click();
        await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
        const rows = p.locator("#ledWrap .led tbody tr.led-row");
        await rows.first().waitFor();

        // a click opens the request, not Routing
        let d = await reader.openRequest(p, rows.nth(0));
        assert(await p.locator("#ledgerPane").isHidden(), "the list gives way to the request");
        assert(await p.locator("#view-routing").isHidden(), "Routing isn't opened");
        assert.equal((await p.locator(".led-req-title").textContent()).trim(), "gpt-6-sol");
        assert.equal(await value(d, w.ip), w.here + " · 127.0.0.1", "a loopback address is this computer");
        if (w.routing) assert.equal(await d.getByText(w.routing, { exact: true }).count(), 1, "Routing is a link on the page");
        await reader.closeRequest(p);

        // another computer's address as it is; none for an older row
        d = await reader.openRequest(p, rows.nth(1));
        assert.equal(await value(d, w.ip), "192.168.1.20");
        await reader.closeRequest(p);
        d = await reader.openRequest(p, rows.nth(2));
        assert.equal(await value(d, w.ip), w.none);
        await reader.closeRequest(p);
        assert.deepEqual(lookups, [], "no route was asked for");

        // below the fold: back, by the button or Escape, is where the list was
        const view = p.locator("#view-usage");
        await reader.inView(p, rows.nth(FAR));
        const left = await view.evaluate((v) => v.scrollTop);
        assert(left > 0, "the row is below the fold");
        d = await reader.openRequest(p, rows.nth(FAR));
        assert.equal(await value(d, w.ip), w.here + " · ::1");
        await reader.closeRequest(p);
        await p.waitForFunction((left) => Math.abs(document.querySelector("#view-usage").scrollTop - left) <= 1, left);
        await reader.openRequest(p, rows.nth(FAR));
        await p.keyboard.press("Escape");
        await p.locator("#ledReqPane").waitFor({ state: "hidden" });
        await p.waitForFunction((left) => Math.abs(document.querySelector("#view-usage").scrollTop - left) <= 1, left);
        assert(await p.evaluate(() => { const a = document.activeElement; return !!a?.matches("tr.led-row") && a.textContent.includes("filler-30"); }), "the row has the keyboard again");

        // the narrowest window: the page fits, nothing scrolls sideways
        await p.setViewportSize({ width: 560, height: 700 });
        d = await reader.openRequest(p, rows.nth(0));
        await p.waitForTimeout(100);
        const fit = await p.evaluate(() => {
          const v = document.querySelector("#view-usage"), box = document.querySelector("#ledReqPane .led-box").getBoundingClientRect();
          return { doc: document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1, view: v.scrollWidth <= v.clientWidth + 1, box: box.right <= v.getBoundingClientRect().right + 1 };
        });
        assert.deepEqual(fit, { doc: true, view: true, box: true }, "no sideways scroll at 560");
        assert.equal(await value(d, w.ip), w.here + " · 127.0.0.1");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
