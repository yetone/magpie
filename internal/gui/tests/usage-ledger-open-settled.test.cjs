// Run with Node's test runner and Playwright on the module path; see README.md.
// Coming back to Usage › Requests from another page (#4064, xisdumpling:
// 7 days, ~1,670 requests, frames dropped while the sidebar moved). The
// rows shown are a hundred, and each redraw or fit of them restyles and
// lays out thousands of nodes. So:
// - the same rows read again are not drawn again, and the table isn't fit
//   again at the width it had (the ResizeObserver saw it go out of sight
//   at width 0, and fit it to 0 and back);
// - rows that changed are drawn once the sidebar has settled on the page
//   (navSubEnter's tabs, navTopStep's spring), not halfway through it;
// and either way the table ends as it was fit, the page where it was.
// Chromium and WebKit; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const MODELS = ["gpt-6.1-sol", "claude-sonnet-5", "glm-5.1"];
const row = (i) => ({ route_id: 1000 + i, t: new Date(now - i * 360e3).toISOString(), agent: "codex", agentName: "Codex", icon: "codex-color",
  provider: "openai", providerName: "openai", host: "chatgpt.com", ep: "/v1/responses", req: MODELS[i % 3], model: MODELS[i % 3], served: MODELS[i % 3], effort: "medium",
  in: 3000 + i, out: 200 + i, cache_read: 110000, cache_write: i % 5 ? 0 : 2400, ms: 9000, ttft_ms: 1200, status: 200, rid: "r" + i, cost: 0.02, priced: true });
const ledger = (period, first) => ({
  period, rows: Array.from({ length: 100 }, (_, i) => row(first + i)), offset: 0, total: 1670, calls: 1670, errors: 0,
  input: 5e6, output: 6e5, cache_read: 1.8e8, cache_write: 6e5, cost: 33.4, unpriced: 0, bucket: "hour", series: [],
  by: { model: MODELS.map((id) => ({ id, calls: 500, input: 1e6, output: 1e5, cache_read: 0, cache_write: 0, cost: 1 })) },
  agents: [{ id: "codex", name: "Codex", icon: "codex-color" }],
});

function server(answer) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang: "en", theme: "light" } });
    if (url.pathname === "/api/usage/requests") return json(ledger(url.searchParams.get("period"), answer.first));
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 0, errors: 0, input: 0, output: 0, reasoning: 0, unpriced: 0, cost: 0, bucket: "day", series: [], agents: [], models: [], path: "" });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: false } });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: back on Usage › Requests, the rows are drawn again only on a change, after the sidebar settles`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    // motion on: the sidebar moves as the reporter's does
    const p = await (await browser.newContext({ viewport: { width: 1280, height: 800 } })).newPage();
    p.setDefaultTimeout(5000);
    p.setDefaultNavigationTimeout(60000);
    const errors = [];
    p.on("pageerror", (e) => errors.push(e.message));
    const answer = { first: 0 };
    await p.route("**/*", server(answer));
    await p.goto("http://magpie.test/");
    await p.locator('[data-view="usage"]').first().click();
    await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
    await p.locator("#ledWrap .led tbody tr").first().waitFor();
    await p.locator("#period .opt").nth(2).click();
    await p.waitForFunction(() => document.querySelectorAll("#ledWrap .led tbody tr.led-row").length === 100);
    await p.waitForTimeout(600);
    const fitted = await p.locator("#ledWrap table.led").getAttribute("class");
    assert.match(fitted, /\btight\b/, "a window narrow enough that the table is fit");

    // watch the table while the reader goes away and comes back
    const back = async () => {
      await p.locator('[data-view="gateway"]').first().click();
      await p.waitForTimeout(700);
      await p.evaluate(() => {
        const wrap = document.querySelector("#ledWrap");
        window.__seen = { table: wrap.querySelector("table.led"), refit: 0, drawn: [], top: document.querySelector("#view-usage").scrollTop };
        window.__seen.table.__was = true;
        window.__watch?.disconnect(); // the last time round's
        window.__watch = new MutationObserver((ms) => {
          for (const m of ms) {
            if (m.type === "attributes" && m.target.matches?.("table.led")) window.__seen.refit++;
            // a new table: was the sidebar still moving?
            if (m.type === "childList" && [...m.addedNodes].some((n) => n.matches?.("table.led"))) {
              window.__seen.drawn.push(!!(window.navTop?.raf || document.querySelector("#nav .nav-sub.moving")));
            }
          }
        });
        window.__watch.observe(wrap, { subtree: true, childList: true, attributes: true, attributeFilter: ["class"] });
      });
      await p.locator('[data-view="usage"]').first().click();
      await p.waitForTimeout(1200);
      return p.evaluate(() => {
        const table = document.querySelector("#ledWrap table.led");
        return { same: table === window.__seen.table, refit: window.__seen.refit, drawn: window.__seen.drawn, cls: table.className, first: table.querySelector("tbody tr.led-row td.model")?.textContent, moved: document.querySelector("#view-usage").scrollTop - window.__seen.top };
      });
    };

    // the same rows: nothing drawn, nothing fit again
    const same = await back();
    assert.equal(same.same, true, "the table shown is kept");
    assert.deepEqual(same.drawn, [], "and not drawn again");
    assert.equal(same.refit, 0, "nor fit again at the width it had");
    assert.equal(same.cls, fitted);
    assert.equal(same.moved, 0, "the page stays where it was");

    // a request came meanwhile: drawn, but not while the sidebar moves
    answer.first = 1;
    const changed = await back();
    assert.equal(changed.same, false, "the new rows are drawn");
    assert.deepEqual(changed.drawn, [false], "once, with the sidebar at rest");
    assert.equal(changed.first, MODELS[1], "the newest row first");
    assert.equal(changed.cls, fitted, "fit as before");
    assert.equal(changed.moved, 0, "the page stays where it was");
    assert.deepEqual(errors, []);
  });
}
