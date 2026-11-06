// Run with Node's test runner and Playwright on the module path; see README.md.
// zzzkl3994 on Discord: in Routing's request list some streamed requests
// showed a speed and some didn't, while the Usage page's Requests tab gave
// every one of them a speed. A request that tells no measured speed (its
// reasoning hidden before a tool call, or its content let go in a burst)
// has the Requests tab's floor there, "≥N tok/s": Routing's row now says
// the same, by the same rule, and a request too small for either still
// says none. Each row's speed is checked against the Usage page's own
// functions for the same request. Every language, both engines, a wide and
// a phone's width.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const seat = { id: "zcode", provider: "zcode", name: "ZCode", who: "me@example.com", kind: "account", model: "glm-5.2" };
// [out, ms, ttft, extra]: newest first
const timing = [
  [600, 8000, 2000, {}], // measured: 100 tok/s
  [1500, 9000, 3000, { reasoning: 1200, reasoningHidden: true }], // a tool call after hidden reasoning: ≥50
  [2400, 20000, 5000, { flow: 200 }], // its content let go in a burst: ≥120 over the whole request
  [300, 5000, 4900, {}], // too little after its first token, too little in all: none
];
const routes = timing.map(([out, ms, ttft, extra], i) => {
  const time = new Date(now.getTime() - (i + 1) * 60e3).toISOString();
  return {
    id: 50 - i, seq: 50 - i, time, agent: "claude", session: "floor-" + i, model: "zcode/glm-5.2", provider: "zcode",
    order: [seat], tries: [{ id: seat.id, model: seat.model, start: time, done: true, status: 200, ms, ttft, ...extra }],
    done: true, status: 200, ms, ttft, tokens: out + 9000, out, ...extra,
  };
});
// the same request as the Usage page's ledger row has it
const ledgerRow = (r) => ({
  status: 200, source: "gateway", agent: r.agent, out: r.out, ms: r.ms, ttft_ms: r.ttft, reasoning: r.reasoning || 0,
  reasoning_hidden: !!r.reasoningHidden, flow_ms: r.flow || 0, first_text_ms: r.firstText || 0, first_reasoning_ms: r.firstReasoning || 0,
});

function serve(lang) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 50, totals: { requests: routes.length, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a streamed request's row says the speed the Requests tab says`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 390]) {
        await t.test(`${lang} ${width}px`, async () => {
          const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
          t.after(() => context.close());
          const page = await context.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang));
          await page.goto("http://magpie.test/?view=routing");
          const rows = page.locator(".rt-req");
          await rows.nth(routes.length - 1).waitFor();
          // what the Requests tab says of each, from the Usage page's own rule
          const usage = await page.evaluate((rs) => rs.map((r) => {
            const v = ledRowSpeed(r), f = ledFloorSpeed(r);
            return v ? t("{n} tok/s", { n: ledNum(Math.round(v)) }) : f ? "≥" + t("{n} tok/s", { n: ledNum(Math.round(f)) }) : "";
          }), routes.map(ledgerRow));
          assert.equal(usage.filter(Boolean).length, 3, `the Requests tab gives three of them a speed: ${JSON.stringify(usage)}`);
          assert.match(usage[1], /^≥50\b/);
          assert.match(usage[2], /^≥120\b/);
          for (let i = 0; i < routes.length; i++) {
            const speed = rows.nth(i).locator(".rt-metric.speed .v");
            if (!usage[i]) { assert.equal(await speed.count(), 0, `row ${i} says no speed, as the Requests tab`); continue; }
            assert.equal((await speed.textContent()).trim(), usage[i], `row ${i} says the Requests tab's speed`);
          }
          // a floor says what it was told from
          const tip = await rows.nth(2).locator(".rt-metric.speed").getAttribute("title");
          const want = await page.evaluate(() => t("At least: {n} output tokens in {ms}, from the request to its last token, the wait for the first one counted too", { n: ledNum(2400), ms: ledTook(20000) }));
          assert.equal(tip, want);
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "nothing runs off the page");
          assert.deepEqual(errors, []);
        });
      }
    }
  });
}
