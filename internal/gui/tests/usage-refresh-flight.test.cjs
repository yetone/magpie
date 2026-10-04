// Run with Node's test runner and Playwright on the module path; see README.md.
// A refresh that is slow must not let a second one start behind it, and a
// reader's own refresh ("Refresh now", a period or a provider picked) must
// never be swallowed by one already on its way: at most one request of a kind
// is in flight, the reader's intent is served once the one running is done, and
// an older answer never overwrites a newer one. The Usage page's refresh (its
// timer and its button, over the Overview and the Requests) and the tray
// panel's Usage tab are both checked, in Chromium and WebKit, with fixtures
// that hold a response open and answer out of order. No backend; the API is
// faked. A clock stands in for time so the timer is exercised without waiting.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const ROW = (over) => ({ t: new Date(now - 60e3).toISOString(), agent: "codex", agentName: "Codex", icon: "codex-color", provider: "relay", providerName: "Relay", host: "team", req: "sol", model: "gpt-6-sol", in: 100, out: 10, ms: 900, status: 200, cost: 0.01, priced: true, ...over });

// A gate per request: the fixture waits on it until the test lets it go, so a
// response can be held open or answered out of order at will.
function gates() {
  const held = [];
  return {
    held,
    // hold() returns the piece of the fixture that lets this request finish
    hold(key) {
      let go;
      const p = new Promise((r) => { go = r; });
      const rec = { key, go, done: false };
      held.push(rec);
      return { p, go };
    },
    // waitFor a request of this kind to be in flight
    async waitFor(key, n = 1) {
      for (let i = 0; i < 200 && held.filter((h) => h.key === key).length < n; i++) await new Promise((r) => setTimeout(r, 20));
      assert(held.filter((h) => h.key === key).length >= n, `in flight: ${key} x${n} (have ${held.map((h) => h.key).join(",")})`);
    },
    // release(key, n) lets the first n open requests of this kind answer;
    // release(key, -1) lets the newest one (and only it) answer; release(key,
    // "all") lets every one answer
    release(key, n = 1) {
      const mine = held.filter((h) => h.key === key && !h.done);
      const pick = n === "all" ? mine : n < 0 ? mine.slice(n) : mine.slice(0, n);
      for (const h of pick) { h.done = true; h.go(); }
      return pick.length;
    },
    count: (key) => held.filter((h) => h.key === key).length,
    open: (key) => held.filter((h) => h.key === key && !h.done).length,
  };
}

// ---- The Usage page (mode=window) -----------------------------------------
function windowServer(lang, log, gate, opts = {}) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" }, fx: { rate: 7.2, at: new Date().toISOString() } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/usage/requests") {
      const period = url.searchParams.get("period"), offset = url.searchParams.get("offset") || "0";
      const key = "requests";
      log.push({ key, period, offset, q: url.search });
      const g = gate.hold(key);
      if (!opts.holdRequests) g.go();
      await g.p;
      const n = opts.rowFor ? opts.rowFor(period, offset) : 1;
      return json({ period, rows: [ROW({ req: "r" + period + "-" + offset })], offset: Number(offset), total: n, calls: n, errors: 0, input: 100, output: 10, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0.01, unpriced: 0, bucket: period === "today" ? "hour" : "day", series: [], by: { provider: [], agent: [], model: [] }, agents: [], providers: [] });
    }
    if (url.pathname === "/api/usage") {
      const period = url.searchParams.get("period");
      log.push({ key: "usage", period, q: url.search });
      const g = gate.hold("usage");
      if (!opts.holdUsage) g.go();
      await g.p;
      const cost = opts.costFor ? opts.costFor(period) : 0.01;
      return json({ calls: 1, errors: 0, input: 100, output: 10, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 0, cost, bucket: "day", series: [{ label: "Mon", input: 100, output: 10, calls: 1, cost }], agents: [{ name: "Codex", calls: 1, cost }], models: [{ name: "gpt-6-sol", calls: 1, cost }], path: "~/.config/magpie/usage.jsonl" });
    }
    if (url.pathname === "/api/usage/quotas") { log.push({ key: "quotas", q: url.search }); return json([]); }
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium"])) {
  test(`${engine}: the Usage page's refresh has one request in flight`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const errors = [], log = [], gate = gates();
    const context = await browser.newContext({ viewport: { width: 1180, height: 700 }, reducedMotion: "reduce" });
    const p = await context.newPage();
    p.setDefaultTimeout(5000);
    p.on("pageerror", (e) => errors.push(e.message));
    await p.clock.install({ time: now });
    await p.route("**/*", windowServer("en", log, gate, { holdUsage: true }));
    await p.goto("http://magpie.test/");
    await p.locator('[data-view="usage"]').first().click(); // Overview
    await gate.waitFor("usage", 1); // the opening read, held
    gate.release("usage", "all"); // let it settle, so the page is drawn and the button is idle
    await p.locator("#stats .kpi").first().waitFor();
    // the timer starts a read that is held; a second must not start behind it
    await p.clock.fastForward(6e3);
    await gate.waitFor("usage", 2);
    const before = gate.count("usage");
    await p.waitForTimeout(150);
    assert.equal(gate.count("usage"), before, "no overlapping request while one is in flight");
    // the reader's own refresh (the button is idle: the read open is the timer's)
    // must still be served once the open one is done
    await p.locator("#usageReload").click();
    await p.waitForTimeout(150);
    assert.equal(gate.count("usage"), before, "the wanting refresh doesn't start a second request");
    gate.release("usage", "all");
    await gate.waitFor("usage", before + 1);
    assert.equal(gate.count("usage"), before + 1, "the wanting refresh is served once the first is done");
    assert(await p.locator("#usageReload").evaluate((el) => el.classList.contains("busy")), "button waits for its queued forced refresh");
    assert(log.some((entry) => entry.key === "quotas" && entry.q.includes("asked=1")), "queued manual refresh keeps the force intent");
    gate.release("usage", "all");
    await p.waitForTimeout(150);
    assert.deepEqual(errors, []);
  });

  test(`${engine}: a rejected refresh releases the in-flight flag`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const errors = [], log = [], gate = gates();
    // the first usage/requests of a kind fails; every one is held until let go
    const fails = new Set();
    const context = await browser.newContext({ viewport: { width: 1180, height: 700 }, reducedMotion: "reduce" });
    const p = await context.newPage();
    p.setDefaultTimeout(5000);
    p.on("pageerror", (e) => errors.push(e.message));
    await p.clock.install({ time: now });
    let seq = 0;
    const base = windowServer("en", log, gate, { holdUsage: false });
    await p.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/api/usage") {
        const mine = ++seq;
        log.push({ key: "usage", q: url.search, seq: mine });
        if (fails.delete(mine)) {
          const g = gate.hold("usage");
          await g.p;
          return route.fulfill({ status: 500, json: { error: "nope" } });
        }
        if (mine > 1) { const g = gate.hold("usage"); await g.p; }
        return route.fulfill({ json: { calls: 1, errors: 0, input: 100, output: 10, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 0, cost: 0.01, bucket: "day", series: [{ label: "Mon", input: 100, output: 10, calls: 1, cost: 0.01 }], agents: [{ name: "Codex", calls: 1, cost: 0.01 }], models: [{ name: "gpt-6-sol", calls: 1, cost: 0.01 }], path: "~/.config/magpie/usage.jsonl" } });
      }
      return base(route);
    });
    await p.goto("http://magpie.test/");
    await p.locator('[data-view="usage"]').first().click(); // Overview
    await p.locator("#stats .kpi").first().waitFor(); // the opening read settled
    // the timer's read is held and will fail; the reader's own click (idle
    // button) must wait for it, not overlap it, and must go out when it fails
    fails.add(seq + 1);
    await p.clock.fastForward(6e3);
    await gate.waitFor("usage", 1);
    const n0 = gate.count("usage");
    await p.locator("#usageReload").click();
    await p.waitForTimeout(200);
    assert.equal(gate.count("usage"), n0, "the asking read didn't overlap the open one");
    gate.release("usage", "all"); // it fails
    await p.waitForTimeout(300);
    assert(gate.count("usage") > n0, "the failure released the flag, so the asking read went out");
    gate.release("usage", "all");
    await p.waitForTimeout(200);
    assert.deepEqual(errors, []);
  });

  test(`${engine}: a period picked mid-flight still updates, newest wins`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const errors = [], log = [], gate = gates();
    const context = await browser.newContext({ viewport: { width: 1180, height: 700 }, reducedMotion: "reduce" });
    const p = await context.newPage();
    p.setDefaultTimeout(5000);
    p.on("pageerror", (e) => errors.push(e.message));
    await p.clock.install({ time: now });
    // every Overview read answers with its own period's cost; a read is held
    // until it is let go, so an older one can be let go after a newer one's
    const cost = (period) => (period === "7d" ? 7.77 : period === "30d" ? 30.3 : 1.11);
    const base = windowServer("en", log, gate, { holdUsage: false, costFor: () => 0 });
    await p.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/api/usage") {
        const period = url.searchParams.get("period");
        log.push({ key: "usage", period, q: url.search });
        const g = gate.hold("usage");
        await g.p;
        const c = cost(period);
        return route.fulfill({ json: { calls: 1, errors: 0, input: 100, output: 10, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 0, cost: c, bucket: "day", series: [{ label: "Mon", input: 100, output: 10, calls: 1, cost: c }], agents: [{ name: "Codex", calls: 1, cost: c }], models: [{ name: "gpt-6-sol", calls: 1, cost: c }], path: "~/.config/magpie/usage.jsonl" } });
      }
      return base(route);
    });
    await p.goto("http://magpie.test/");
    await p.locator('[data-view="usage"]').first().click();
    await gate.waitFor("usage", 1); // Overview's opening read, held
    gate.release("usage", "all");
    await p.locator("#stats .kpi").first().waitFor();
    // the reader asks for a refresh of today, held; then picks 30 days, whose
    // own read answers and is drawn. Today's late answer must not put its own
    // numbers over 30 days'
    await p.locator("#usageReload").click(); // today's refresh, held
    await gate.waitFor("usage", 2);
    await p.locator("#period .opt").nth(2).click(); // 30 days
    await gate.waitFor("usage", 3);
    gate.release("usage", -1); // 30 days' answer first
    await p.waitForTimeout(300);
    assert((await p.locator("#view-usage").textContent()).includes("30.3"), "the newest period's answer is drawn");
    gate.release("usage", "all"); // today's held refresh answers late
    await p.waitForTimeout(300);
    const shown = await p.locator("#view-usage").textContent();
    assert(shown.includes("30.3"), "the older period's answer did not overwrite it: " + shown.slice(0, 160));
    const asked = log.filter((r) => r.key === "usage").map((r) => r.period);
    assert.deepEqual(asked.slice(0, 3), ["today", "today", "30d"], "the reader's ask was served, the pick was too: " + asked.join(","));
    assert.deepEqual(errors, []);
  });

  test(`${engine}: a pick's skeleton is ended by the timer's read of the same figures`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const errors = [], log = [], gate = gates();
    const context = await browser.newContext({ viewport: { width: 1180, height: 700 }, reducedMotion: "reduce" });
    const p = await context.newPage();
    p.setDefaultTimeout(5000);
    p.on("pageerror", (e) => errors.push(e.message));
    await p.clock.install({ time: now });
    // The opening read and the timer's answer with the same figures, so the
    // timer's read is equal to what the page already has and draws nothing of
    // its own; the pick's read, let go last, answers with different ones.
    let read = 0;
    const base = windowServer("en", log, gate, { holdUsage: false, costFor: () => 0 });
    await p.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/api/usage") {
        const period = url.searchParams.get("period"), mine = ++read;
        log.push({ key: "usage", period, mine });
        const g = gate.hold("usage");
        await g.p;
        const cost = mine === 2 ? 9.99 : 1.11; // only the pick's read differs
        return route.fulfill({ json: { period, calls: 1, errors: 0, input: 100, output: 10, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 0, cost, bucket: "day", series: [{ label: "Mon", input: 100, output: 10, calls: 1, cost }], agents: [{ name: "Codex", calls: 1, cost }], models: [{ name: "gpt-6-sol", calls: 1, cost }], path: "~/.config/magpie/usage.jsonl" } });
      }
      return base(route);
    });
    const loading = () => p.locator("#view-usage").evaluate((el) => el.classList.contains("loading"));
    await p.goto("http://magpie.test/");
    await p.locator('[data-view="usage"]').first().click(); // Overview
    await gate.waitFor("usage", 1);
    gate.release("usage", "all"); // the opening read
    await p.locator("#stats .kpi").first().waitFor();
    assert.equal(await loading(), false, "the drawn page is not left loading");
    // the reader picks the period already shown: the skeleton goes up, its read held
    await p.locator("#period .opt").nth(0).click(); // today
    await gate.waitFor("usage", 2);
    assert.equal(await loading(), true, "a pick does not show the skeleton");
    // the timer reads the same period; its figures are the ones already on hand
    await p.clock.fastForward(6e3);
    await gate.waitFor("usage", 3);
    gate.release("usage", -1); // the timer's read, the newest
    await p.locator("#view-usage:not(.loading)").waitFor();
    const shown = await p.locator("#view-usage").textContent();
    assert(shown.includes("1.11"), "the figures on hand did not end the skeleton: " + shown.slice(0, 200));
    gate.release("usage", "all"); // the pick's read, late
    await p.waitForTimeout(300);
    const after = await p.locator("#view-usage").textContent();
    assert(after.includes("1.11") && !after.includes("9.99"), "the late pick's figures were drawn: " + after.slice(0, 200));
    assert.deepEqual(errors, []);
  });

  // ---- The tray panel (mode=panel) ----------------------------------------
  test(`${engine}: the panel's Usage tab keeps one request in flight`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const errors = [], log = [], gate = gates();
    const state = { agents: [], profiles: [], settings: { lang: "en", theme: "light" } };
    const context = await browser.newContext({ viewport: { width: 440, height: 340 }, reducedMotion: "reduce" });
    const p = await context.newPage();
    p.setDefaultTimeout(5000);
    p.on("pageerror", (e) => errors.push(e.message));
    await p.clock.install({ time: now });
    await p.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      const json = (data) => route.fulfill({ json: data });
      if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:true};` });
      if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
      if (url.pathname === "/api/state") return json(state);
      if (url.pathname === "/api/usage/requests") {
        const period = url.searchParams.get("period"), provider = url.searchParams.get("provider") || "";
        log.push({ key: "requests", period, provider, q: url.search });
        const g = gate.hold("requests");
        await g.p;
        const cost = provider === "relay" && period === "7d" ? 5.5 : period === "7d" ? 7.7 : period === "30d" ? 30.3 : 0.01;
        return json({ period, rows: [], offset: 0, total: 1, calls: 1, errors: 0, input: 100, output: 10, cache_read: 0, cache_write: 0, reasoning: 0, cost, unpriced: 0, bucket: period === "today" ? "hour" : "day", series: [{ label: "Mon", time: new Date(now).toISOString(), calls: 1, errors: 0, input: 100, output: 10, cache_write: 0, cache_read: 0, cost, by: { provider: { relay: { calls: 1, tokens: 100, cost } }, agent: {}, model: {} } }], by: { provider: [], agent: [], model: [] }, agents: [], providers: [{ id: "relay", name: "Relay", icon: "generic", calls: 1, errors: 0, input: 100, output: 10, cache_write: 0, cache_read: 0, cost }, { id: "claude", name: "Claude", icon: "generic", calls: 1, errors: 0, input: 100, output: 10, cache_write: 0, cache_read: 0, cost }] });
      }
      if (url.pathname === "/api/gateway/trace") return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
      if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
      if (url.pathname === "/api/usage") return json({ calls: 1, input: 1, output: 1, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0, unpriced: 0, bucket: "day", series: [], agents: [], models: [] });
      if (url.pathname === "/api/usage/quotas") return json([]);
      if (url.pathname === "/api/groups") return json({ groups: [] });
      if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
      if (url.pathname === "/api/window/main" || url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
      if (url.pathname.startsWith("/api/")) return json({});
      const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
      const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
      await route.fulfill({ body: await fs.readFile(file), contentType });
    });
    await p.goto("http://magpie.test/?mode=panel");
    await p.locator('#ptabs [data-ptab="stats"]').click(); // the Usage tab
    await gate.waitFor("requests", 1); // the tab's opening read, held
    gate.release("requests", "all"); // let it settle, so the picker is built
    await p.locator("#panelUsage:not(.pu-loading)").waitFor();
    await p.locator("#panelUsage .pu-tot").waitFor();
    // the tab's 10 s timer must not start a second request behind a held one
    await p.clock.fastForward(12e3);
    await gate.waitFor("requests", 2); // the timer's read, held
    const before = gate.count("requests");
    await p.waitForTimeout(200);
    assert.equal(gate.count("requests"), before, "no overlapping panel request");
    // a period picked while that one is open must be kept, not dropped: it is
    // served once the open one is done, and only once (the newest pick wins)
    await p.locator("#panelUsage .pu-bar .segs .opt").nth(1).click(); // 7 days
    await p.waitForTimeout(100);
    await p.locator("#panelUsage .sess-pick").click({ force: true });
    await p.locator(".proto-menu .pm-item", { hasText: "Relay" }).click({ force: true });
    await p.waitForTimeout(200);
    assert.equal(gate.count("requests"), before, "a pick while one is open doesn't start another");
    gate.release("requests", "all"); // the open one is done: the pick is served, once
    await gate.waitFor("requests", before + 1);
    gate.release("requests", "all");
    await p.waitForTimeout(300);
    const last = await p.evaluate(() => document.querySelector("#panelUsage").textContent);
    assert(last.includes("5.5"), "the latest pick is drawn: " + last.slice(0, 200));
    const picks = log.filter((r) => r.key === "requests");
    assert.equal(picks.at(-1).period, "7d", "the newest period is the one read");
    assert.equal(picks.at(-1).provider, "relay", "the newest provider is the one read");
    assert.deepEqual(errors, []);
  });

  test(`${engine}: a panel error releases the in-flight flag`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const errors = [], log = [], gate = gates();
    let failNext = true;
    const state = { agents: [], profiles: [], settings: { lang: "en", theme: "light" } };
    const context = await browser.newContext({ viewport: { width: 440, height: 340 }, reducedMotion: "reduce" });
    const p = await context.newPage();
    p.setDefaultTimeout(5000);
    p.on("pageerror", (e) => errors.push(e.message));
    await p.clock.install({ time: now });
    await p.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      const json = (data) => route.fulfill({ json: data });
      if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:true};` });
      if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
      if (url.pathname === "/api/state") return json(state);
      if (url.pathname === "/api/usage/requests") {
        log.push({ key: "requests", q: url.search });
        if (failNext) { failNext = false; return route.fulfill({ status: 500, json: { error: "nope" } }); }
        return json({ period: url.searchParams.get("period"), rows: [], offset: 0, total: 1, calls: 1, errors: 0, input: 100, output: 10, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0.01, unpriced: 0, bucket: "hour", series: [], by: { provider: [], agent: [], model: [] }, agents: [], providers: [] });
      }
      if (url.pathname === "/api/gateway/trace") return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
      if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
      if (url.pathname === "/api/usage") return json({ calls: 1, input: 1, output: 1, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0, unpriced: 0, bucket: "day", series: [], agents: [], models: [] });
      if (url.pathname === "/api/usage/quotas") return json([]);
      if (url.pathname === "/api/groups") return json({ groups: [] });
      if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
      if (url.pathname === "/api/window/main" || url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
      if (url.pathname.startsWith("/api/")) return json({});
      const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
      const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
      await route.fulfill({ body: await fs.readFile(file), contentType });
    });
    await p.goto("http://magpie.test/?mode=panel");
    await p.locator('#ptabs [data-ptab="stats"]').click(); // the Usage tab
    await p.waitForTimeout(400);
    const n = log.filter((r) => r.key === "requests").length;
    assert(n >= 1, "the failing read was asked for");
    // the timer fires again: the failure must have released the flag
    await p.clock.fastForward(11e3);
    await p.waitForTimeout(200);
    assert(log.filter((r) => r.key === "requests").length > n, "the flag was released by the failure");
    assert.deepEqual(errors, []);
  });
}
