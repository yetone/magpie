// Run with Node's test runner and Playwright on the module path; see README.md.
// shangyuan0818 on X: a Claude Code turn of a long message read 2 or 4 in
// the request list's In. Claude Code's in is only what its prompt cache
// didn't hold. Where a narrow window leaves the cache columns out, In says
// the whole prompt; where they show, In is the vendor's in, and its
// tooltip gives the split. A row the cache had nothing of says its in, in
// every language, in Chromium and WebKit at 1400 and 390 wide; the API is
// faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const row = (i, extra) => ({
  t: new Date(now - (i + 1) * 60e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color",
  provider: "claude", providerName: "Claude", providerAccount: "a@example.com", req: "claude-opus-5", model: "claude-opus-5", served: "claude-opus-5",
  ms: 3000, status: 200, rid: "r-" + i, ep: "/v1/messages", cost: 0.01, priced: true, source: "log", ...extra,
});
const ROWS = [
  row(0, { in: 2, out: 900, cache_read: 52000, cache_write: 8400 }),
  row(1, { in: 1234, out: 50, cache_read: 0, cache_write: 0 }),
];

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: new Date(now).toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/requests") return json({ period: url.searchParams.get("period"), rows: ROWS, offset: 0, total: ROWS.length, calls: ROWS.length, errors: 0, input: 1236, output: 950, cache_read: 52000, cache_write: 8400, reasoning: 0, cost: 0.02, unpriced: 0, agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }] });
    if (url.pathname === "/api/usage/requests/content") return json({ found: false, why: "read" });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 2, errors: 0, input: 1236, output: 950, cache_read: 52000, cache_write: 8400, reasoning: 0, unpriced: 0, cost: 0.02, bucket: "day", series: [], agents: [], models: [], path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1400, 390]) {
    test(`${engine} ${width}: In says the whole prompt where the cache columns are left out`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
        await t.test(lang, async () => {
          const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
          const page = await context.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang));
          try {
            await page.goto("http://magpie.test/?view=usage");
            await page.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
            await page.locator(".led tbody tr.led-row").nth(ROWS.length - 1).waitFor({ timeout: 15000 });
            const got = await page.evaluate(() => {
              const table = document.querySelector(".led"), heads = [...table.tHead.rows[0].cells];
              const col = (name) => heads.findIndex((h) => h.textContent === t(name));
              const shown = (e) => e.getClientRects().length > 0;
              const i = col("In"), r = col("Cache read");
              const cell = (row) => table.tBodies[0].rows[row].cells[i];
              const tight = table.classList.contains("tight");
              // each way the window can lay it out: the class is what
              // ledFit sets for the width, toggled to read the other too
              const read = (on) => {
                table.classList.toggle("tight", on);
                return { cacheShown: shown(heads[r]), cached: cell(0).innerText.trim(), plain: cell(1).innerText.trim() };
              };
              const wide = read(false), narrow = read(true);
              table.classList.toggle("tight", tight);
              return {
                tight, wide, narrow, head: heads[i].title, cachedTip: cell(0).title,
                want: { own: fmtN(2), whole: fmtN(60402), plain: fmtN(1234) },
                tip: t("Prompt {all}: {in} new · {read} cache read · {write} cache write", { all: ledNum(60402), in: ledNum(2), read: ledNum(52000), write: ledNum(8400) }),
              };
            });
            if (width === 390) assert(got.tight, "the narrow window leaves columns out");
            assert(got.wide.cacheShown && !got.narrow.cacheShown);
            assert.equal(got.wide.cached, got.want.own, "beside the cache columns, the vendor's in");
            assert.equal(got.narrow.cached, got.want.whole, "with them left out, the whole prompt");
            for (const v of [got.wide, got.narrow]) assert.equal(v.plain, got.want.plain, "a prompt the cache had nothing of is its in");
            assert.equal(got.cachedTip, got.tip);
            assert(got.head.length > 0, "the header says what In counts");
            if (lang !== "en") assert.notEqual(got.tip, "Prompt 60,402: 2 new · 52,000 cache read · 8,400 cache write", "translated");
            assert.deepEqual(errors, []);
          } finally {
            await context.close();
          }
        });
      }
    });
  }
}
