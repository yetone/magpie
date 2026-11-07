// Run with Node's test runner and Playwright on the module path; see README.md.
// The tray panel's height (fit) is the showing tab's, every part of it
// counted: the header, the tabs and the gap under them, the view's padding,
// what sits around a tab's section (the Get started card, the fold under
// the agents) and the foot. Summing the sections alone left the view short
// by 10px on Routing and 31px on 8 agents, so a scrollbar showed
// (CyberCaius on Discord, on Windows). And on the first open, while the
// trace is on its way, the Routing tab's skeleton is as tall as what comes,
// so the panel doesn't grow when it lands; today's line is asked for at
// once, not after the trace. Going back from a tall tab to a short one the
// panel shrinks again, gliding there (refeccd on Discord: it stayed at the
// tallest tab's height).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const at = (m) => new Date(now - m * 60e3).toISOString();
const relay = { id: "relay:team", provider: "relay", name: "Relay", kind: "key", who: "team", model: "gpt-6-sol" };
const routes = [1, 2, 3, 4, 5, 6, 7, 8].map((i) => ({
  id: 100 + i, seq: 100 + i, time: at(i), agent: "codex", model: "relay/gpt-6-sol", provider: "relay", order: [relay],
  tries: [{ id: relay.id, model: "gpt-6-sol", start: at(i), done: true, status: 200, ms: 300 }], done: true, status: 200, ms: 300, tokens: 1000,
}));
const ids = ["codex", "claude", "gemini", "opencode", "kimi", "qwen", "crush", "goose"];

// held: promises the trace's and today's first answers wait for
function serve(page, lang, agents, held = {}) {
  const state = { agents: ids.slice(0, agents).map((id) => ({ id, name: id, path: "/test/" + id, fields: [] })), profiles: [], settings: { lang, theme: "light" } };
  const fits = [], asks = [];
  const handler = async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      else await held.trace;
      return json({ mine: true, now: now.toISOString(), seq: 108, totals: { requests: 8, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/usage" && url.searchParams.get("period") === "today") {
      await held.today;
      return json({ calls: 42, input: 1000000, output: 234000, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0, series: [] });
    }
    if (url.pathname === "/api/window/fit") {
      // the window takes the height asked for, as the tray's does
      const h = Number(url.searchParams.get("h"));
      fits.push(h);
      asks.push(url.searchParams);
      await page.setViewportSize({ width: 380, height: h }).catch(() => {});
      return route.fulfill({ status: 204 });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
  return { handler, fits, asks };
}

// the view's overflow on each tab, after the panel was fitted
async function overflows(page) {
  const out = {};
  for (const tab of ["agents", "usage", "stats", "routing"]) {
    const b = page.locator(`#ptabs [data-ptab="${tab}"]`);
    if (!(await b.isVisible())) continue;
    await b.click();
    await page.waitForTimeout(150);
    out[tab] = await page.evaluate(() => { const v = document.querySelector("#view-agents"); return v.scrollHeight - v.clientHeight; });
  }
  return out;
}

// a fit that has settled: no new height asked for a while
async function settled(page, fits) {
  let n = -1;
  while (n !== fits.length) { n = fits.length; await page.waitForTimeout(400); }
  return fits[fits.length - 1];
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: the tray panel fits every tab whole`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const errors = [];
      const open = async (agents, held) => {
        const page = await (await browser.newContext({ viewport: { width: 380, height: 200 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        const s = serve(page, lang, agents, held);
        await page.route("**/*", s.handler);
        await page.goto("http://magpie.test/?mode=panel");
        return { page, fits: s.fits };
      };

      // eight agents, and Routing with its stage and six requests: no tab
      // scrolls in the height the panel asked for
      for (const agents of [8, 2]) {
        const { page, fits } = await open(agents);
        await page.locator("#panelRouting .pr-req").first().waitFor({ state: "attached" });
        await page.locator("#agents .row.agent").nth(agents - 1).waitFor();
        await settled(page, fits);
        const over = await overflows(page);
        for (const [tab, px] of Object.entries(over)) assert(px <= 0, `${agents} agents: the ${tab} tab overflows the fitted panel by ${px}px (${JSON.stringify(over)}, fits ${fits.join(" ")})`);
        await page.context().close();
      }

      // the first open: the trace held back, today's line comes without it,
      // and Routing's skeleton holds the height its requests take
      let release;
      const trace = new Promise((r) => { release = r; });
      const { page, fits } = await open(2, { trace });
      await page.locator("#agents .row.agent").nth(1).waitFor();
      await page.waitForFunction(() => /42/.test(document.querySelector("#panelRouting .pr-today")?.textContent || ""), null, { timeout: 3000 })
        .catch(() => assert.fail("today's line waited for the trace: " + JSON.stringify(fits)));
      await page.locator('#ptabs [data-ptab="routing"]').click();
      const before = await settled(page, fits);
      release();
      await page.locator("#panelRouting .pr-req").first().waitFor({ state: "attached" });
      const after = await settled(page, fits);
      assert.equal(after, before, `the panel changed height when the trace landed: ${fits.join(" ")}`);
      const over = await overflows(page);
      assert(over.routing <= 0, `Routing overflows by ${over.routing}px`);

      // a fit measured with the view scrolled leaves it where it is: a
      // panel kept shorter than its agents (a small screen) scrolled down
      await page.route("**/api/window/fit*", (route) => route.fulfill({ status: 204 }));
      await page.locator('#ptabs [data-ptab="agents"]').click();
      await page.setViewportSize({ width: 380, height: 220 });
      // scrolled as a reader does, which the page's scroll guard lets be
      const box = await page.evaluate(() => { const r = document.querySelector("#view-agents").getBoundingClientRect(); return { x: r.x + r.width / 2, y: r.y + Math.min(r.height / 2, 40) }; });
      await page.mouse.move(box.x, box.y);
      await page.mouse.wheel(0, 400);
      await page.waitForTimeout(400);
      const kept = await page.evaluate(async () => {
        const v = document.querySelector("#view-agents");
        const top = v.scrollTop;
        let moved = 0;
        v.addEventListener("scroll", () => moved++);
        fit(40);
        fit();
        await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
        return { top, now: v.scrollTop, moved };
      });
      assert(kept.top > 0, "the agents scroll in a short panel");
      assert.deepEqual({ now: kept.now, moved: kept.moved }, { now: kept.top, moved: 0 }, "measuring the tabs scrolled the view");
      assert.deepEqual(errors, []);
    });

    // two agents and Routing's eight requests: Routing is the tall tab
    test(`${engine} ${lang}: the tray panel shrinks back to a short tab`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const motion of ["reduce", "no-preference"]) {
        const errors = [];
        const page = await (await browser.newContext({ viewport: { width: 380, height: 200 }, reducedMotion: motion })).newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        const s = serve(page, lang, 2);
        await page.route("**/*", s.handler);
        await page.goto("http://magpie.test/?mode=panel");
        await page.locator("#panelRouting .pr-req").first().waitFor({ state: "attached" });
        await page.locator("#agents .row.agent").nth(1).waitFor();
        const short = await settled(page, s.fits);
        await page.locator('#ptabs [data-ptab="routing"]').click();
        const tall = await settled(page, s.fits);
        assert(tall > short, `${motion}: Routing asked no taller a panel than two agents: ${s.fits.join(" ")}`);
        const over = await page.evaluate(() => { const v = document.querySelector("#view-agents"); return v.scrollHeight - v.clientHeight; });
        assert(over <= 0, `${motion}: Routing overflows by ${over}px`);
        const n = s.asks.length;
        await page.locator('#ptabs [data-ptab="agents"]').click();
        const back = await settled(page, s.fits);
        assert.equal(back, short, `${motion}: back on two agents the panel kept ${back}, not ${short}: ${s.fits.join(" ")}`);
        // the tab picked glides the edge on app.css's ease-out, a jump when
        // motion is reduced
        const ask = s.asks[n];
        if (motion === "reduce") assert.equal(ask.get("ms"), null, `a glide under reduced motion: ${ask}`);
        else assert.deepEqual([ask.get("ms"), ask.get("ease")], ["240", ".23,1,.32,1"], `the tab's fit: ${ask}`);
        assert.deepEqual(errors, []);
        await page.context().close();
      }
    });
  }
}
