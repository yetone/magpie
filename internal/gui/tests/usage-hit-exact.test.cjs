// Run with Node's test runner and Playwright on the module path; see README.md.
// shangyuan0818 on X: the Requests tab's ranking said each model's cache
// hit rate as "98%" beside "99.6%", and the request list did the same,
// with no exact figure anywhere. Every hit rate now has two decimals
// (98.00%, 99.60%), still never 100.00% for a prompt not all cached, and
// its tooltip says the tokens read over the prompt and the share
// unrounded. ohblue on X: the Speed column showed "—" for a 200 with 93
// tokens out 0.1 s after a 6.3 s first token, with nothing saying why
// unless the dash was hovered. The dash keeps its reason on hover (a help
// cursor says there is one), the column's header says what Speed is, and
// the request's details say the reason in words, for a reader who can't
// hover. In Chromium and WebKit, in every language, wide and at a phone's
// width; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const reader = require("./reader.cjs");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();

// three models: 9,800 of 10,000 (98%), 9,960 of 10,000 (99.6%), 99,999 of
// 100,000 (99.999%, which must not read 100.00%)
const share = (id, input, cache_read) => ({ id, name: id, icon: "", calls: 5, errors: 0, input, output: 500, cache_read, cache_write: 0, reasoning: 0, cost: 0.1, unpriced: 0 });
const MODELS = [share("model-b", 1, 99999), share("model-a", 200, 9800), share("model-c", 40, 9960)];
const req = (i, x) => ({
  t: new Date(now - (i + 1) * 60e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color",
  provider: "anthropic", providerName: "Anthropic", providerAccount: "a@example.com", req: "m", model: "m", served: "m",
  status: 200, rid: "r-" + i, ep: "/v1/messages", cost: 0.01, priced: true, cache_write: 0, ...x,
});
// the reporter's row: 491 in, 93 out, its first token at 6.3 s, done at
// 6.42 s; and one that wrote 600 tokens over the 6 s after its first
const ROWS = [
  req(0, { in: 200, cache_read: 9800, out: 600, ttft_ms: 1000, ms: 7000 }),
  req(1, { in: 40, cache_read: 9960, out: 19, ms: 3000 }),
  req(2, { in: 1, cache_read: 99999, out: 19, ms: 3000 }),
  req(3, { in: 491, cache_read: 0, out: 93, ttft_ms: 6300, ms: 6420 }),
];
const WANT_HIT = ["98.00%", "99.60%", "99.99%", "0.00%"];
const sum = (k) => MODELS.reduce((a, m) => a + m[k], 0);
const SERIES = [{ label: "00", time: new Date(now - 3600e3).toISOString(), calls: 15, errors: 0, input: sum("input"), output: sum("output"), cache_read: sum("cache_read"), cache_write: 0, cost: 0.3,
  by: Object.fromEntries(["provider", "agent", "model"].map((d) => [d, Object.fromEntries(MODELS.map((m) => [m.id, { calls: 5, tokens: m.input + m.output + m.cache_read, cost: 0.1 }]))])) }];
const page = (q) => ({ period: q.get("period"), rows: ROWS, offset: 0, total: ROWS.length, calls: 15, errors: 0, input: sum("input"), output: sum("output"), cache_read: sum("cache_read"), cache_write: 0, reasoning: 0, cost: 0.3, unpriced: 0,
  bucket: "hour", series: SERIES, by: { provider: MODELS, agent: MODELS, model: MODELS }, agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }] });
const usage = { calls: 15, errors: 0, input: 0, output: 0, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0, unpriced: 0, path: "/test/usage.jsonl", bucket: "day", agents: [], models: [], series: [] };

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: new Date(now).toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, calls: [], groups: [] } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/requests") return json(page(url.searchParams));
    if (url.pathname === "/api/usage/requests/content") return json({ found: false, why: "read" });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json(usage);
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname.startsWith("/api/window/")) return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404 }); }
  };
}

const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const TIP = "{a} of {b} prompt tokens read from the cache: {p}";
const HELD = "It came in under a second after a wait of {wait}: it may have been written during the wait and let go at once, so how fast it was written isn't seen";
const SPEED_HEAD = "Output tokens a second after the first, over the streamed replies";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: every Usage hit rate has two decimals and its exact share on hover; a speed's dash says why`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of LANGS) {
      await t.test(lang, async () => {
        const context = await browser.newContext({ viewport: { width: 1600, height: 900 }, reducedMotion: "reduce" });
        const errors = [];
        try {
          const p = await context.newPage();
          p.setDefaultTimeout(5000);
          p.setDefaultNavigationTimeout(60000);
          p.on("pageerror", (e) => errors.push(e.message));
          await p.route("**/*", serve(lang));
          await p.goto("http://magpie.test/?view=usage&tab=requests");
          const say = (s, v) => p.evaluate(([s, v]) => t(s, v), [s, v]);
          const num = (n) => p.evaluate((n) => ledNum(n), n);
          const tip = async (read, prompt, pct) => say(TIP, { a: await num(read), b: await num(prompt), p: pct });
          if (lang !== "en") for (const s of [TIP, HELD, SPEED_HEAD]) assert.notEqual(await say(s, { a: 1, b: 2, p: "3", wait: "4" }), await p.evaluate(([s]) => s.replace("{a}", 1).replace("{b}", 2).replace("{p}", "3").replace("{wait}", "4"), [s]), "translated: " + s);
          const rows = p.locator(".led tbody tr.led-row");
          await rows.nth(ROWS.length - 1).waitFor({ timeout: 15000 });

          // the ranking beside the chart: two decimals each, exact on hover
          await p.locator("#ledRank .rk-hit").nth(MODELS.length - 1).waitFor();
          const rank = await p.locator("#ledRank .rk").evaluateAll((rs) => rs.map((r) => ({ name: r.querySelector(".rk-nm").textContent, hit: r.querySelector(".rk-hit")?.textContent, title: r.querySelector(".rk-hit")?.title })));
          const byName = Object.fromEntries(rank.map((r) => [r.name, r]));
          const rate = (v) => say("hit rate {p}", { p: v });
          assert.equal(byName["model-a"].hit, await rate("98.00%"));
          assert.equal(byName["model-c"].hit, await rate("99.60%"));
          assert.equal(byName["model-b"].hit, await rate("99.99%"), "never 100.00% of a prompt not all cached");
          assert.equal(byName["model-a"].title, await tip(9800, 10000, "98%"));
          assert.equal(byName["model-b"].title, await tip(99999, 100000, "99.999%"));
          // the card over them, two decimals too, exact on hover
          const card = await p.locator("#ledKpi .blk").evaluateAll((bs, label) => {
            const b = bs.find((x) => x.querySelector(".k")?.textContent === label);
            return b && { v: b.querySelector(".v").textContent, title: b.title };
          }, await say("Cache hit rate"));
          const read = sum("cache_read"), prompt = read + sum("input");
          assert.equal(card.v, (100 * read / prompt).toFixed(2) + "%");
          assert(card.title.includes(await tip(read, prompt, +(100 * read / prompt).toFixed(6) + "%")), card.title);

          // the request list: the same two decimals down the column
          const cols = await p.locator(".led").evaluate((tb, [hitL, speedL]) => {
            const heads = [...tb.tHead.rows[0].cells];
            const at = (l) => heads.findIndex((c) => c.textContent === l);
            const h = at(hitL), s = at(speedL);
            const cells = [...tb.tBodies[0].rows].filter((x) => x.classList.contains("led-row"));
            return {
              hit: cells.map((x) => ({ text: x.cells[h].textContent, title: x.cells[h].title })),
              speed: cells.map((x) => ({ text: x.cells[s].textContent, title: x.cells[s].title, cursor: getComputedStyle(x.cells[s]).cursor })),
              speedHead: heads[s].title,
            };
          }, [await say("Cache hit rate"), await say("Speed")]);
          assert.deepEqual(cols.hit.map((c) => c.text), WANT_HIT);
          assert(cols.hit[0].title.startsWith(await tip(9800, 10000, "98%")), cols.hit[0].title);
          assert(cols.hit[2].title.startsWith(await tip(99999, 100000, "99.999%")), cols.hit[2].title);
          assert.equal(cols.speedHead, await say(SPEED_HEAD));
          // one that can be timed says its speed; the reporter's says why not
          assert.equal(cols.speed[0].text, await say("{n} tok/s", { n: await num(100) }));
          assert.equal(cols.speed[3].text, "—");
          const why = await say(HELD, { wait: await p.evaluate(() => ledTook(6300)) });
          assert.equal(cols.speed[3].title, why);
          assert.equal(cols.speed[3].cursor, "help", "the dash shows it has something to say");
          assert.notEqual(cols.speed[0].cursor, "help");

          // the request's details say it in words, at a phone's width too
          for (const width of [1600, 420]) {
            await p.setViewportSize({ width, height: 900 });
            const box = await reader.openRequest(p, rows.nth(3));
            const speed = box.locator("dt", { hasText: await say("Speed") });
            await speed.waitFor();
            assert.equal(await speed.evaluate((d) => d.nextElementSibling.textContent), "— " + why);
            const hit = box.locator("dt", { hasText: await say("Cache hit rate") });
            assert.equal(await hit.evaluate((d) => d.nextElementSibling.textContent), "0.00%");
            assert.equal(await hit.evaluate((d) => d.nextElementSibling.title), await tip(0, 491, "0%"));
            assert(await p.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "the page scrolls sideways at " + width);
            await reader.closeRequest(p);
          }
          assert.deepEqual(errors, []);
        } finally {
          await context.close();
        }
      });
    }
  });
}
