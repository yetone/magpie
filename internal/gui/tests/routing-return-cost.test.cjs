// Run with Node's test runner and Playwright on the module path; see README.md.
// Back on Routing from another page, with a day of a few hundred requests
// listed, the page doesn't stall (xisdumpling, #4652: 180–298 requests on
// Windows, the page and the sidebar's highlight stopped for a moment each
// time Routing was picked again), a request picked. What coming back costs is counted, not
// timed: while Routing is out of sight its boxes keep the layout they had
// (a hidden box measures 0 wide, and it took the narrow layout, to go wide
// again a frame after it came back), the stage keeps its nodes when nothing
// on it changed, the routing groups and the hidden Providers page aren't
// drawn again, and the list draws the rows in and near its sight, not all
// of them. In Chromium the layouts are counted too (LayoutCount): 24 each
// time before, 8 after, of which the frame's own and the sidebar's. In
// Chromium and WebKit.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const fs = require("node:fs/promises");
const path = require("node:path");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now(), iso = (ms) => new Date(ms).toISOString();
const N = 250; // between the reporter's 180 and 298
const seat = (n) => ({ id: `acct-${n}`, provider: `prov${n}`, name: `Provider ${n}`, who: `user${n}@example.com`, kind: "account", model: `model-${n}`, known: true, plan: "Pro" });
const agents = ["codex", "claude"];
const prompt = (n) => ({ window: 272000, tokens: 90000 + n * 800, counted: true, turns: n % 9,
  parts: [{ kind: "system", tokens: 9000, items: [{ name: "prompt", tokens: 9000 }] }, { kind: "chat", tokens: 81000 + n * 800, items: [{ name: "turn", tag: "turns", n: n % 9, tokens: 81000 + n * 800 }] }] });
function req(id) {
  const t0 = now - (N + 10 - id) * 30000, order = [seat(id % 3), seat((id + 1) % 3)];
  return { id, seq: id, time: iso(t0), agent: agents[id % 2], session: "s" + (id % 4), model: order[0].model, provider: order[0].provider, order,
    tries: [{ id: order[0].id, model: order[0].model, start: iso(t0), done: true, status: 200, ms: 1000 + (id % 7) * 900, ttft: 300 + id % 5 * 70 }],
    done: true, status: 200, ms: 1000 + (id % 7) * 900, ttft: 300 + id % 5 * 70, tokens: 2000 + id * 37, cost: 0.01 * (id % 9), priced: true,
    usage: [{ in: 3000 + id, cache_read: 40000 + id * 11, out: 400 + id }], prompt: prompt(id) };
}
const routes = Array.from({ length: N }, (_, i) => req(i + 1));
const DAY = new Date(now - 864e5 * 2).toISOString().slice(0, 10); // a day the history keeps, picked
const models = [{ id: "a/sol", name: "sol", providerName: "A", icon: "generic" }, { id: "b/flash", name: "flash", providerName: "B", icon: "generic" }];
const member = { members: ["a/sol", "b/flash"], ready: true, memberInfo: [{ id: "a/sol", ready: true }, { id: "b/flash", ready: true }], offers: [], shared: [] };
const groups = { models, pools: [], groups: [{ ...member, id: "hand", name: "Hand", routing: "manual", pick: "a/sol", picked: "a/sol" }, { ...member, id: "auto", name: "Auto", routing: "order" }] };
const providers = { presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" },
  providers: Array.from({ length: 6 }, (_, i) => ({ id: "p" + i, name: "Provider " + i, icon: "generic", models: [], agents: [], key: { set: true, masked: "sk-…ab1" + i } })) };

function serve(feed) {
  const state = { agents: agents.map((id) => ({ id, name: id, path: "/test/" + id, fields: [] })), profiles: [], settings: { lang: "en", theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url()); const json = (d) => route.fulfill({ json: d }).catch(() => {});
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: 'window.bootPrefs = {lang:"en",theme:"light",web:true};' });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      const trace = (rs, seq) => json({ mine: true, now: new Date().toISOString(), seq, totals: { requests: N, rerouted: 0, errors: 0 }, routes: rs });
      if (!url.searchParams.get("wait")) return trace(routes.slice(-3), N);
      while (!feed.closed) await new Promise((r) => setTimeout(r, 50)); // the long poll: nothing new
      return trace([], N);
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [{ day: DAY, requests: N }], routes: url.searchParams.get("day") === DAY ? routes : [] });
    if (url.pathname === "/api/groups") return json(groups);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const ct = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType: ct });
  };
}

// what the page does from here on, counted
const watch = () => {
  const w = window.__w = { flips: [], providers: 0, groups: 0, story: 0 };
  const marks = /\b(max\d+|min\d+|meta-under)\b/g;
  const set = (s) => (s || "").match(marks)?.sort().join(" ") || "";
  new MutationObserver((ms) => { for (const m of ms) if (set(m.oldValue) !== set(m.target.getAttribute("class"))) w.flips.push(m.target.className); })
    .observe(document.querySelector("#view-routing"), { subtree: true, attributes: true, attributeFilter: ["class"], attributeOldValue: true });
  new MutationObserver((ms) => { w.providers += ms.length; }).observe(document.querySelector("#view-providers"), { subtree: true, childList: true });
  new MutationObserver((ms) => { w.groups += ms.length; }).observe(document.querySelector("#view-groups .rt-groups"), { subtree: true, childList: true });
  // the picked request's story: its head, its steps and its context card
  for (const s of [".rt-log-head", ".rt-steps", ".ctx-card"]) new MutationObserver((ms) => { w.story += ms.length; }).observe(document.querySelector("#view-routing " + s), { subtree: true, childList: true, characterData: true });
  // the stage's nodes as they are now
  for (const e of document.querySelectorAll("#view-routing .rt-srcs > *, #view-routing .rt-accts li")) e.__was = true;
};
const counted = () => {
  const stage = [...document.querySelectorAll("#view-routing .rt-srcs > *, #view-routing .rt-accts li")];
  return { ...window.__w, stage: stage.length, stageKept: stage.filter((e) => e.__was).length,
    rows: document.querySelectorAll("#view-routing .rt-reqs > .rt-req").length,
    providerRows: document.querySelector("#providers").children.length,
    nodes: document.querySelectorAll("#view-routing *").length };
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: back on Routing with ${N} requests listed, the page isn't laid out and drawn again whole`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const context = await browser.newContext({ viewport: { width: 1400, height: 900 } });
    const page = await context.newPage();
    page.setDefaultTimeout(10000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    const feed = { closed: false };
    t.after(() => { feed.closed = true; });
    await page.route("**/*", serve(feed));
    const cdp = engine === "chromium" ? await context.newCDPSession(page) : null;
    if (cdp) await cdp.send("Performance.enable");
    const layouts = async () => {
      if (!cdp) return null;
      const { metrics } = await cdp.send("Performance.getMetrics");
      const m = Object.fromEntries(metrics.map((x) => [x.name, x.value]));
      return { layouts: m.LayoutCount, styles: m.RecalcStyleCount, ms: m.LayoutDuration * 1000 };
    };
    // settled: no frame of the page's for a while asks for anything new
    const settle = () => page.waitForTimeout(1200);

    await page.goto("http://magpie.test/?view=routing");
    await page.locator(".rt-req").first().waitFor();
    await page.locator(".rt-hub").waitFor();
    await page.locator(".rt-days .rt-day:not(.rt-day-more)").nth(1).click();
    await page.waitForFunction((n) => document.querySelectorAll("#view-routing .rt-req").length > 3 && document.querySelector(".rt-day.on")?.textContent.includes(String(n)), N);
    // a request picked: its story and its context card are shown too
    await page.locator("#view-routing .rt-reqs > .rt-req").nth(2).click();
    await page.locator("#view-routing .ctx-card").waitFor();
    await settle();
    const wide = await page.evaluate(() => document.querySelector("#view-routing .rt-col").className);
    assert(!/max760/.test(wide), `the list's column is wide at 1400px: ${wide}`);

    const nav = (v) => page.locator(`#nav button[data-view="${v}"]`).click();
    await nav("providers");
    await page.locator("#view-providers").waitFor();
    await settle();
    await page.evaluate(watch);
    const l0 = await layouts();
    await nav("routing");
    await settle();
    const l1 = await layouts();
    const back = await page.evaluate(counted);

    // and out once more, then back, all of it counted from leaving
    await page.evaluate(watch);
    await nav("providers");
    await settle();
    const away = await page.evaluate(counted);
    await page.evaluate(() => { window.__w.groups = 0; window.__w.providers = 0; });
    const l2 = await layouts();
    await nav("routing");
    await settle();
    const l3 = await layouts();
    const again = await page.evaluate(counted);
    // the time is logged, not judged: a busy machine's, not the page's
    const lay = cdp ? { back: l1.layouts - l0.layouts, styles: l1.styles - l0.styles, again: l3.layouts - l2.layouts, againMs: +(l3.ms - l2.ms).toFixed(1) } : null;
    console.log(`${engine} #4652:`, JSON.stringify({ lay, back, away: { flips: away.flips.length, providers: away.providers }, again: { ...again, flips: again.flips.length } }));

    assert.deepEqual(away.flips, [], "out of sight, Routing's boxes keep the layout they had");
    assert.deepEqual(again.flips, [], "back in sight, no box goes narrow and wide again");
    assert.equal(again.providers, 0, "the hidden Providers page isn't drawn again while Routing is shown");
    assert.equal(again.groups, 0, "the routing groups, the same as before, aren't drawn again");
    assert.equal(again.story, 0, "the picked request's story and context card, the same, aren't drawn again");
    assert(again.stage > 0, "the stage has its agents and accounts");
    assert.equal(again.stageKept, again.stage, "the stage keeps its nodes when nothing on it changed");
    assert(again.rows > 0 && again.rows < N / 2, `the list draws the rows in and near its sight, not all ${N}: ${again.rows}`);
    if (lay) assert(lay.again <= 12, `coming back lays the page out a few times at most, not once for each box: ${lay.again}`);
    assert.deepEqual(errors, []);
  });
}
