// Run with Node's test runner and Playwright on the module path; see README.md.
// The usage chart's side labels (the window's Requests tab and the tray
// panel's Usage tab) keep the card's padding: the plot starts where the widest
// label ends, so "8000 万", "125 亿", "¥9000" stand as far in from the card's
// edge as its tabs and its ranking do, and none is cut off or runs past the
// card — for tokens, cost in dollars and in yuan, and requests, small and
// large, wide and narrow. English and Chinese; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const midnight = new Date();
midnight.setHours(0, 0, 0, 0);

// a day by the hour of one provider, scaled: tokens, cost and calls an hour
function page(scale) {
  const per = { tokens: 7e7 * scale, cost: 1.2 * scale, calls: Math.max(1, Math.round(900 * scale)) };
  const split = (tokens) => ({ input: tokens * 0.01, output: tokens * 0.005, cache_write: tokens * 0.05, cache_read: tokens * 0.935 });
  const series = Array.from({ length: 24 }, (_, h) => {
    const on = h >= 2 ? 1 : 0;
    const part = { calls: per.calls * on, tokens: per.tokens * on, cost: per.cost * on };
    const by = { provider: { zcode: part }, agent: { claude: part }, model: { "glm-5": part } };
    return { label: String(h), time: new Date(midnight.getTime() + h * 3600e3).toISOString(), calls: part.calls, errors: 0, ...split(part.tokens), cost: part.cost, by };
  });
  const sum = (k) => series.reduce((a, p) => a + p[k], 0);
  const tot = { calls: sum("calls"), errors: 0, input: sum("input"), output: sum("output"), cache_write: sum("cache_write"), cache_read: sum("cache_read"), reasoning: 0, cost: sum("cost"), unpriced: 0 };
  const share = (id, name, icon) => ({ id, name, icon, ...tot });
  return {
    period: "today", rows: [], offset: 0, total: 1, ...tot, bucket: "hour", series,
    by: { provider: [share("zcode", "ZCode", "generic")], agent: [share("claude", "Claude Code", "claudecode-color")], model: [share("glm-5", "glm-5", "")] },
    agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }], providers: [{ id: "zcode", name: "ZCode", icon: "generic" }],
  };
}

function serve(lang, currency, scale, web) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", currency }, fx: { rate: 7.2, at: new Date().toISOString() } });
    if (url.pathname === "/api/usage/requests") return json(page(scale));
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 1, errors: 0, input: 1, output: 1, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 0, cost: 1, bucket: "day", series: [], agents: [], models: [] });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// where the side labels are against their card: each label's box, the card's
// padded inside, where the plot starts (the grid's left end), and whether
// anything in the card runs past it
async function measure(p, card) {
  return p.locator(card).first().evaluate((c) => {
    const r = c.getBoundingClientRect(), cs = getComputedStyle(c);
    const inner = { left: r.left + parseFloat(cs.paddingLeft) + parseFloat(cs.borderLeftWidth), right: r.right - parseFloat(cs.paddingRight) - parseFloat(cs.borderRightWidth) };
    const svg = c.querySelector(".led-chart svg");
    const labs = [...svg.querySelectorAll("text.axis[text-anchor=end]")].map((x) => ({ text: x.textContent, ...x.getBoundingClientRect().toJSON() }));
    const grid = Math.min(...[...svg.querySelectorAll(".grid")].map((g) => g.getBoundingClientRect().left));
    const out = [...c.querySelectorAll("svg text, svg rect.col")].filter((e) => { const b = e.getBoundingClientRect(); return b.width && (b.left < r.left - 0.5 || b.right > r.right + 0.5); }).map((e) => e.textContent || e.tagName);
    const ref = c.querySelector(".led-bar .segs, .led-bar").getBoundingClientRect().left;
    return { inner, labs, grid, out, ref, sideways: document.documentElement.scrollWidth > innerWidth + 1 };
  });
}

function check(m, what) {
  assert.equal(m.labs.length, 5, what + ": five side labels");
  for (const l of m.labs) {
    assert(l.width > 0, `${what}: "${l.text}" is drawn`);
    assert(l.left >= m.inner.left - 0.5, `${what}: "${l.text}" starts at ${l.left.toFixed(1)}, left of the card's padding at ${m.inner.left.toFixed(1)}`);
    assert(l.right <= m.grid + 0.5, `${what}: "${l.text}" runs into the plot (${l.right.toFixed(1)} > ${m.grid.toFixed(1)})`);
  }
  // as far in as the tabs above it
  const widest = Math.min(...m.labs.map((l) => l.left));
  assert(widest >= m.ref - 1.5, `${what}: the widest label at ${widest.toFixed(1)}, the tabs at ${m.ref.toFixed(1)}`);
  assert.deepEqual(m.out, [], what + ": past the card");
  assert(!m.sideways, what + ": the page scrolls sideways");
}

const CASES = [
  // scale, currency: tokens 8000 万 / 80M, cost $1.5 → ¥10, requests 1000
  [1, "usd"], [1, "cny"],
  // large: 125 亿 tokens, $250 / ¥2000 an hour, 2 万 requests
  [150, "usd"], [150, "cny"],
  // small: 5000 tokens, cents
  [0.00007, "usd"],
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the usage chart's side labels keep the card's padding", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const shots = process.env.ARTIFACT_DIR;
    if (shots) await fs.mkdir(shots, { recursive: true });

    for (const lang of ["en", "zh"]) {
      await t.test(lang + ", the window's Requests tab", async () => {
        for (const [scale, currency] of CASES) {
          for (const width of [1180, 560]) {
            const ctx = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
            const p = await ctx.newPage();
            const errors = [];
            p.setDefaultTimeout(5000);
            p.on("pageerror", (e) => errors.push(e.message));
            await p.route("**/*", serve(lang, currency, scale, false));
            await p.goto("http://magpie.test/");
            await p.locator('[data-view="usage"]').first().click();
            await p.locator("#usageTab .opt").nth(1).click();
            await p.locator("#ledChart svg").waitFor();
            for (let i = 0; i < 3; i++) {
              await p.locator("#ledMetric .opt").nth(i).click();
              await p.locator("#ledChart svg").waitFor();
              check(await measure(p, ".led-trend"), `${lang} ${width} ${scale} ${currency} metric ${i}`);
            }
            if (shots && scale === 1) await p.locator(".led-trend").screenshot({ path: path.join(shots, `axis-${engine}-${lang}-${currency}-${width}.png`) });
            assert.deepEqual(errors, []);
            await ctx.close();
          }
        }
      });

      await t.test(lang + ", the tray panel", async () => {
        for (const [scale, currency] of CASES) {
          for (const width of [440, 340]) {
            const ctx = await browser.newContext({ viewport: { width, height: 560 }, reducedMotion: "reduce" });
            const p = await ctx.newPage();
            const errors = [];
            p.setDefaultTimeout(5000);
            p.on("pageerror", (e) => errors.push(e.message));
            await p.route("**/*", serve(lang, currency, scale, true));
            await p.goto("http://magpie.test/?mode=panel");
            await p.locator('[data-ptab="stats"]').click();
            await p.locator("#panelUsage .pu-card .led-chart svg").waitFor();
            for (let i = 0; i < 3; i++) {
              await p.locator("#panelUsage .pu-card .led-bar .opt").nth(i).click();
              await p.locator("#panelUsage .pu-card .led-chart svg").waitFor();
              check(await measure(p, "#panelUsage .pu-card"), `panel ${lang} ${width} ${scale} ${currency} metric ${i}`);
            }
            if (shots && scale === 1) await p.locator("#panelUsage .pu-card").screenshot({ path: path.join(shots, `axis-${engine}-${lang}-${currency}-panel-${width}.png`) });
            assert.deepEqual(errors, []);
            await ctx.close();
          }
        }
      });
    }
  });
}
