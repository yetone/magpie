// Run with Node's test runner and Playwright on the module path; see README.md.
// The tray panel's Usage tab, its period picked: the segmented control's thumb
// glides to the option picked and must not bounce back. Picking a period
// redraws the tab at once and again when the answer lands; the second redraw
// makes a fresh thumb, and resuming the old slide from where it began would
// throw the thumb back to the option left behind a few frames in (the jump
// this guards). The thumb's left edge is sampled every frame across the click:
// it may only move toward the option picked, and comes to rest on it.
// English, Chinese, Japanese and German, Chromium and WebKit; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets");
const midnight = new Date();
midnight.setHours(0, 0, 0, 0);

// The answer arrives a few frames after the click, so the redraw it causes
// lands mid-glide — the case the old slide mishandled. Any delay under the
// 300 ms the thumb records is enough; a beat longer keeps it clear of the
// click's own redraw on a slow machine.
const ANSWER_MS = 120;

const WHO = [
  { id: "anthropic", name: "Claude", icon: "claudecode-color", hours: [8, 20], tokens: 900000, calls: 2, cost: 0.9 },
  { id: "relay", name: "Relay", icon: "generic", hours: [10, 14], tokens: 400000, calls: 3, cost: 0.4 },
];
const shareOf = (id, name, icon, calls, tokens, cost, errors = 0) => ({ id, name, icon, calls, errors, input: tokens * 0.01, output: tokens * 0.005, cache_write: tokens * 0.05, cache_read: tokens * 0.935, cost });

function page(q) {
  const only = q.get("provider");
  const who = WHO.filter((w) => !only || w.id === only);
  const days = q.get("period") === "today" ? 1 : q.get("period") === "7d" ? 7 : 30;
  const hourly = days === 1;
  const n = hourly ? 24 : days;
  const series = Array.from({ length: n }, (_, i) => {
    const time = new Date(hourly ? midnight.getTime() + i * 3600e3 : midnight.getTime() - (n - 1 - i) * 864e5);
    const on = (w) => (hourly ? i >= w.hours[0] && i <= w.hours[1] : i % 2 === 0) ? 1 : 0;
    const by = { provider: {}, agent: {}, model: {} };
    let calls = 0, tokens = 0, cost = 0;
    for (const w of who) {
      if (!on(w)) continue;
      for (const m of ["m1", "m2"]) by.model[m] = { calls: w.calls / 2, tokens: w.tokens / 2, cost: w.cost / 2 };
      by.provider[w.id] = { calls: w.calls, tokens: w.tokens, cost: w.cost };
      calls += w.calls; tokens += w.tokens; cost += w.cost;
    }
    return { label: String(i), time: time.toISOString(), calls, errors: 0, input: tokens * 0.01, output: tokens * 0.005, cache_write: tokens * 0.05, cache_read: tokens * 0.935, cost, by };
  });
  const sum = (k) => series.reduce((a, p) => a + p[k], 0);
  const facet = (w) => shareOf(w.id, w.name, w.icon, w.calls, w.tokens, w.cost);
  return {
    period: q.get("period"), rows: [], offset: 0, total: 1, calls: sum("calls"), errors: 0,
    input: sum("input"), output: sum("output"), cache_write: sum("cache_write"), cache_read: sum("cache_read"), reasoning: 0, cost: sum("cost"), unpriced: 0,
    bucket: hourly ? "hour" : "day", series,
    by: { provider: WHO.map(facet), agent: [], model: [{ id: "m1", name: "m1", calls: 1, tokens: 1, cost: 1 }] },
    agents: [{ id: "codex", name: "Codex", icon: "codex-color" }], providers: WHO.map((w) => ({ id: w.id, name: w.name, icon: w.icon })),
  };
}

function serve(lang, gate) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/usage/requests") {
      if (gate.period === url.searchParams.get("period")) { gate.ready(); await gate.wait; }
      else await new Promise((r) => setTimeout(r, ANSWER_MS));
      return json(page(url.searchParams));
    }
    if (url.pathname === "/api/gateway/trace") return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/usage") return json({ calls: 1, input: 1, output: 1, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0, unpriced: 0, bucket: "day", series: [], agents: [], models: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// The thumb's left edge, every frame for a second, with the target option's
// left at the end: enough to see whether the path ever turns back.
async function sampleAcross(p, click, ms) {
  const sampling = p.evaluate((ms) => new Promise((resolve) => {
    const out = [];
    const t0 = performance.now();
    window.periodClickedAt = 0;
    const tick = () => {
      const th = document.querySelector("#panelUsage .pu-bar .segs .thumb");
      if (th) out.push({ t: Math.round(performance.now() - t0), x: th.getBoundingClientRect().left });
      if (!window.periodClickedAt || performance.now() - window.periodClickedAt < ms) requestAnimationFrame(tick);
      else resolve(out);
    };
    requestAnimationFrame(tick);
  }), ms);
  await click();
  await p.evaluate(() => { window.periodClickedAt = performance.now(); });
  return sampling;
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    test(`${engine} ${lang}: the panel Usage period thumb does not bounce back`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const errors = [];
      // The motion is the thing under test: with reduced motion the browser
      // cuts every transition, and there would be no glide to watch.
      const p = await (await browser.newContext({ viewport: { width: 440, height: 500 }, reducedMotion: "no-preference" })).newPage();
      p.setDefaultTimeout(5000);
      p.on("pageerror", (e) => errors.push(e.message));
      const gate = { period: "" };
      await p.route("**/*", serve(lang, gate));
      await p.goto("http://magpie.test/?mode=panel");
      await p.locator('[data-ptab="stats"]').click();
      await p.locator("#panelUsage .pu-tot").waitFor();
      await p.waitForTimeout(400);

      const opts = p.locator("#panelUsage .pu-bar .segs .opt");
      const seg = p.locator("#panelUsage .pu-bar .segs");
      const from = await seg.locator(".thumb").evaluate((e) => e.getBoundingClientRect().left);
      const target = await opts.nth(1).evaluate((e) => e.getBoundingClientRect().left);
      assert(target > from + 20, "7 days sits to the right of Today");

      const samples = await sampleAcross(p, () => opts.nth(1).click(), 900);
      assert(samples.length > 10, "the thumb was sampled across the click");

      // The thumb starts on Today and ends on 7 days: every step must be
      // toward it, never back. A pixel of slack for sub-pixel layout.
      let worst = 0, back = null;
      for (let i = 1; i < samples.length; i++) {
        const d = samples[i - 1].x - samples[i].x;
        if (d > worst) { worst = d; back = [samples[i - 1], samples[i]]; }
      }
      assert(worst <= 1, `the thumb moved back ${worst.toFixed(2)}px: ${JSON.stringify(back)}`);
      assert(Math.abs(samples[0].x - from) <= 1.5, "the thumb began on Today");
      assert(Math.abs(samples.at(-1).x - target) <= 1.5, `the thumb came to rest on 7 days (${samples.at(-1).x} vs ${target})`);

      // and it stays there: the option under the thumb is the one picked
      await p.waitForTimeout(200);
      const rest = await seg.locator(".thumb").evaluate((e) => e.getBoundingClientRect().left);
      assert(Math.abs(rest - target) <= 1.5, "the thumb is still on 7 days");
      assert.equal((await opts.nth(1).textContent()).trim(), ({ en: "7 days", zh: "7 天", ja: "7 日", de: "7 Tage" })[lang]);

      // back the other way, too: the same rule, target to the left
      const backSamples = await sampleAcross(p, () => opts.nth(0).click(), 900);
      let worstBack = 0, forward = null;
      for (let i = 1; i < backSamples.length; i++) {
        const d = backSamples[i].x - backSamples[i - 1].x;
        if (d > worstBack) { worstBack = d; forward = [backSamples[i - 1], backSamples[i]]; }
      }
      assert(worstBack <= 1, `the thumb moved forward ${worstBack.toFixed(2)}px: ${JSON.stringify(forward)}`);
      assert(Math.abs(backSamples.at(-1).x - from) <= 1.5, "the thumb came to rest on Today again");

      // A second period picked before the first answer arrives must keep
      // moving right, rather than restarting from Today's old position.
      const rapid = await sampleAcross(p, async () => {
        await opts.nth(1).click();
        await p.waitForTimeout(60);
        await opts.nth(2).click();
      }, 900);
      let rapidBack = 0;
      for (let i = 1; i < rapid.length; i++) rapidBack = Math.max(rapidBack, rapid[i - 1].x - rapid[i].x);
      assert(rapidBack <= 1, `rapid picks moved back ${rapidBack.toFixed(2)}px: ${JSON.stringify(rapid)}`);
      const lastTarget = await opts.nth(2).evaluate((e) => e.getBoundingClientRect().left);
      assert(Math.abs(rapid.at(-1).x - lastTarget) <= 1.5, "rapid picks finish on 30 days");

      const rapidLeft = await sampleAcross(p, async () => {
        await opts.nth(1).click();
        await p.waitForTimeout(60);
        await opts.nth(0).click();
        assert.equal(await p.evaluate(() => panelUsePeriod), "today", "the second left pick reached Today");
      }, 900);
      let rapidForward = 0;
      for (let i = 1; i < rapidLeft.length; i++) rapidForward = Math.max(rapidForward, rapidLeft[i].x - rapidLeft[i - 1].x);
      assert(rapidForward <= 1, `rapid left picks moved forward ${rapidForward.toFixed(2)}px`);
      const todayTarget = await opts.nth(0).evaluate((e) => e.getBoundingClientRect().left);
      assert(Math.abs(rapidLeft.at(-1).x - todayTarget) <= 1.5, `rapid left picks finish on Today (${rapidLeft.at(-1).x} vs ${todayTarget}, initial ${from})`);

      // Hold the 7-day answer until Today is pressed. Its redraw must keep
      // that button connected, so the release still dispatches its click.
      gate.period = "7d";
      const held = new Promise((r) => { gate.ready = r; });
      let release;
      gate.wait = new Promise((r) => { release = r; });
      await opts.nth(1).click();
      await held;
      const today = await opts.nth(0).boundingBox();
      await p.mouse.move(today.x + today.width / 2, today.y + today.height / 2);
      await p.mouse.down();
      const answer = p.waitForResponse((r) => new URL(r.url()).searchParams.get("period") === "7d");
      release();
      await answer;
      await p.waitForFunction(() => !panelUseFlight);
      await p.mouse.up();
      assert.equal(await p.evaluate(() => panelUsePeriod), "today", "an answer while Today is pressed keeps its click");
      await p.waitForFunction(() => !panelUseFlight);
      await p.waitForFunction(() => {
        const s = document.querySelector("#panelUsage .pu-bar .segs");
        return Math.abs(s.querySelector(".thumb").getBoundingClientRect().left - s.querySelector(".on").getBoundingClientRect().left) <= 1.5;
      });
      const heldRest = await seg.locator(".thumb").evaluate((e) => e.getBoundingClientRect().left);
      assert(Math.abs(heldRest - todayTarget) <= 1.5, "the retained click finishes on Today");

      assert.deepEqual(errors, []);
    });
  }
}
