// Run with Node's test runner and Playwright on the module path; see README.md.
// The Requests tab opens on what its requests add up to: a strip of five totals
// (tokens, requests, cost, the cache hit rate, the output speed), then the trend of one metric —
// tokens, cost, requests or speed — as columns by the hour, day or week, each told
// apart by model, model at provider, provider or agent, beside a ranking of the same that is the
// chart's legend and a way in: a click on a provider or an agent lists only its
// requests, the provider picker beside the agent's does too, and the ranking
// keeps the others in sight to switch to. The pointer over a column shows what
// each had of it; over a ranked one, the others in the chart fade. The metric
// and what it is split by are remembered; a click moves nothing; the chart fits
// at every width and follows the window; with no request listed there is
// nothing. English and Chinese, light and dark; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const midnight = new Date();
midnight.setHours(0, 0, 0, 0);

// three providers over a day by the hour: who called them, and on what model
const WHO = [
  { id: "relay", name: "Relay", icon: "generic", agent: "codex", model: "gpt-6-sol", hours: [7, 16], per: { calls: 3, tokens: 400000, cost: 0.4 } },
  { id: "anthropic", name: "Claude", icon: "claudecode-color", agent: "claude", model: "claude-sonnet-5", hours: [13, 20], per: { calls: 2, tokens: 900000, cost: 0.9 } },
  { id: "codex", name: "Codex", icon: "codex-color", agent: "codex", model: "gpt-6-luna", hours: [19, 23], per: { calls: 4, tokens: 200000, cost: 0.1 } },
];
const AGENTS = { codex: { name: "Codex", icon: "codex-color" }, claude: { name: "Claude Code", icon: "claudecode-color" } };
const inHour = (w, h) => (h >= w.hours[0] && h <= w.hours[1] ? 1 : 0);
const SERIES = Array.from({ length: 24 }, (_, h) => {
  const by = { provider: {}, agent: {}, model: {} };
  let calls = 0, tokens = 0, cost = 0;
  for (const w of WHO) {
    if (!inHour(w, h)) continue;
    const part = { calls: w.per.calls, tokens: w.per.tokens, cost: w.per.cost };
    for (const [dim, key] of [["provider", w.id], ["agent", w.agent], ["model", w.model]]) {
      const o = by[dim][key] || { calls: 0, tokens: 0, cost: 0 };
      by[dim][key] = { calls: o.calls + part.calls, tokens: o.tokens + part.tokens, cost: o.cost + part.cost };
    }
    calls += part.calls; tokens += part.tokens; cost += part.cost;
  }
  return { label: String(h).padStart(2, "0"), time: new Date(midnight.getTime() + h * 3600e3).toISOString(), calls, errors: 0, input: tokens * 0.01, output: tokens * 0.005, cache_write: tokens * 0.05, cache_read: tokens * 0.935, cost, by };
});
const share = (id, name, icon, calls, tokens, cost, errors = 0) => ({ id, name, icon, calls, errors, input: tokens * 0.01, output: tokens * 0.005, cache_write: tokens * 0.05, cache_read: tokens * 0.935, cost });
const sumOf = (pick) => SERIES.reduce((a, p) => { for (const [k, v] of Object.entries(pick(p))) { a[k] = a[k] || { calls: 0, tokens: 0, cost: 0 }; a[k].calls += v.calls; a[k].tokens += v.tokens; a[k].cost += v.cost; } return a; }, {});
const BY = {
  provider: Object.entries(sumOf((p) => p.by.provider)).map(([id, x]) => share(id, WHO.find((w) => w.id === id).name, WHO.find((w) => w.id === id).icon, x.calls, x.tokens, x.cost, id === "relay" ? 3 : 0)),
  agent: Object.entries(sumOf((p) => p.by.agent)).map(([id, x]) => share(id, AGENTS[id].name, AGENTS[id].icon, x.calls, x.tokens, x.cost)),
  model: Object.entries(sumOf((p) => p.by.model)).map(([id, x]) => share(id, id, "", x.calls, x.tokens, x.cost)),
};
for (const k in BY) BY[k].sort((a, b) => b.input + b.output + b.cache_read + b.cache_write - (a.input + a.output + a.cache_read + a.cache_write));
const sum = (k) => SERIES.reduce((a, p) => a + p[k], 0);
const TOTALS = { calls: sum("calls"), errors: 3, input: sum("input"), output: sum("output"), cache_write: sum("cache_write"), cache_read: sum("cache_read"), reasoning: 0, cost: sum("cost"), unpriced: 0 };
const allTokens = TOTALS.input + TOTALS.output + TOTALS.cache_write + TOTALS.cache_read;
const ROWS = [{ t: new Date().toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color", provider: "anthropic", providerName: "Claude", model: "claude-sonnet-5", req: "sonnet", in: 2, out: 600, cache_write: 900, cache_read: 390000, ms: 2380, status: 200, cost: 0.087, priced: true }];

function page(q, variant) {
  if (q.has("day")) {
    const whole = new URLSearchParams(q);
    whole.delete("day");
    const l = page(whole, variant), point = l.series.find((p) => p.time.slice(0, 10) === q.get("day"));
    const rows = l.rows.filter((r) => r.t.slice(0, 10) === q.get("day"));
    const by = Object.fromEntries(Object.entries(l.by).map(([dim, shares]) => [dim, shares.flatMap((s) => {
      const part = point?.by[dim][s.id];
      return part ? [share(s.id, s.name, s.icon, part.calls, part.tokens, part.cost)] : [];
    })]));
    return { ...l, calls: 0, errors: 0, input: 0, output: 0, cache_write: 0, cache_read: 0, cost: 0,
      ...point, day: q.get("day"), series: l.series, chartBy: l.by, by, rows, total: rows.length };
  }
  const none = variant === "none";
  const noPrice = variant === "unpriced";
  // more providers than the chart's height holds in its ranking
  const many = variant === "many" ? Array.from({ length: 9 }, (_, i) => share("p" + i, "Provider " + i, "generic", 90 - i, 9e6 - i * 8e5, 9 - i, i % 3 ? 0 : 2)) : null;
  const daily = variant === "daily" && q.get("period") === "7d";
  const points = daily ? Array.from({ length: 7 }, (_, i) => {
    const at = new Date(midnight);
    at.setDate(at.getDate() - 6 + i);
    return { ...SERIES[i === 1 ? 0 : i % 2 ? 8 : 20], time: at.toISOString() };
  }) : SERIES;
  const series = points.map((p) => (noPrice ? { ...p, cost: 0, by: { ...p.by, provider: Object.fromEntries(Object.entries(p.by.provider).map(([k, v]) => [k, { ...v, cost: 0 }])) } } : p));
  return {
    period: "today", rows: none ? [] : ROWS, offset: 0, total: none ? 0 : ROWS.length, ...TOTALS, ...(none ? { calls: 0, errors: 0, input: 0, output: 0, cache_write: 0, cache_read: 0, cost: 0 } : {}),
    ...(noPrice ? { cost: 0, unpriced: 40 } : {}),
    ...(daily ? { rows: series.filter((p) => p.calls).map((p) => ({ ...ROWS[0], t: p.time })), total: 6 } : {}),
    bucket: daily ? "day" : "hour", series: none ? [] : series, by: none ? { provider: [], agent: [], model: [] } : many ? { ...BY, provider: many } : BY,
    agents: Object.entries(AGENTS).map(([id, a]) => ({ id, ...a })), providers: (many || WHO).map((w) => ({ id: w.id, name: w.name, icon: w.icon })),
    // the two sources and their total, as the Requests tab's three cells count
    // them: the calls the gateway served, the ones read from an agent's own
    // session file, and both together
    through: { calls: 8, errors: 1, input: TOTALS.input * .4, output: TOTALS.output * .4, cache_write: TOTALS.cache_write * .4, cache_read: TOTALS.cache_read * .4, cost: TOTALS.cost * .4 },
    direct: { calls: 12, errors: 2, input: TOTALS.input * .6, output: TOTALS.output * .6, cache_write: TOTALS.cache_write * .6, cache_read: TOTALS.cache_read * .6, cost: TOTALS.cost * .6 },
  };
}

function server(lang, theme, variant, asked, exported) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme }, fx: { rate: 7.2, at: new Date().toISOString() } });
    if (url.pathname === "/api/usage/requests") { asked.push(url.searchParams); return json(page(url.searchParams, variant)); }
    if (url.pathname === "/api/usage/requests/export") { exported.push(url.searchParams); return json({ rows: 1, path: "/test/requests.csv" }); }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 1, errors: 0, input: 1, output: 1, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 0, cost: 1, bucket: "day", series: [], agents: [], models: [], path: "~/.config/magpie/usage.jsonl" });
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
  en: { strip: ["Tokens", "Requests", "Cost", "Cache hit rate", "Output speed"], metric: ["Tokens", "Cost", "Requests", "Speed"], split: ["Model", "Model · provider", "Provider", "Agent"], all: "All providers", none: "No known price for these requests" },
  zh: { strip: ["Token", "请求", "费用", "缓存命中率", "输出速度"], metric: ["Token", "费用", "请求", "速度"], split: ["模型", "模型 · 供应商", "供应商", "Agent"], all: "全部供应商", none: "这些请求没有已知价格" },
};
// where each split is among #ledSplit's choices
const SPLIT = { model: 0, modelAt: 1, provider: 2, agent: 3 };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Requests tab's totals and trend", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const shots = process.env.ARTIFACT_DIR;
    if (shots) await fs.mkdir(shots, { recursive: true });

    const open = async (lang, theme, { width = 1180, variant = "", ctx } = {}) => {
      const errors = [], asked = [], exported = [];
      const context = ctx || (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" }));
      const p = await context.newPage();
      p.setDefaultTimeout(5000);
      p.on("pageerror", (e) => errors.push(e.message));
      await p.route("**/*", server(lang, theme, variant, asked, exported));
      await p.goto("http://magpie.test/");
      await p.locator('[data-view="usage"]').first().click();
      await p.locator("#usageTab .opt").nth(1).click();
      if (variant !== "none") await p.locator("#ledRank .rk").first().waitFor();
      if (["many", "unpriced"].includes(variant)) await p.locator("#ledSplit .opt").nth(SPLIT.provider).click();
      return { p, errors, asked, exported, context };
    };
    const lastAsked = async (asked, want) => {
      for (let i = 0; i < 60 && !(asked.length && want(asked.at(-1))); i++) await new Promise((r) => setTimeout(r, 40));
      assert(asked.length && want(asked.at(-1)), "asked: " + (asked.at(-1) || ""));
    };
    const names = (p) => p.locator("#ledRank .rk .rk-nm").allTextContents();

    for (const lang of ["en", "zh"]) {
      const w = L[lang];
      await t.test(lang, async () => {
        const { p, errors, asked } = await open(lang, "light");
        // the strip of five totals
        assert.deepEqual(await p.locator("#ledKpi .k").allTextContents(), w.strip);
        assert.equal(await p.locator("#ledKpi .blk").nth(0).getAttribute("title"), Math.round(allTokens).toLocaleString(lang === "zh" ? "zh-CN" : "en"));
        const rate = TOTALS.cache_read / (TOTALS.input + TOTALS.cache_write + TOTALS.cache_read);
        assert.equal(await p.locator("#ledKpi .blk").nth(3).locator(".v").textContent(), (100 * rate).toFixed(1) + "%");
        assert.equal(parseFloat(await p.locator("#ledKpi .blk").nth(3).locator(".meter i").evaluate((i) => i.style.width)), parseFloat((100 * rate).toFixed(1)));
        assert.equal(await p.locator("#ledKpi .blk").nth(1).locator(".v").textContent(), String(TOTALS.calls));

        // the two switches, and the ranking by the most tokens: Claude, Relay, Codex
        assert.deepEqual(await p.locator("#ledMetric .opt").allTextContents(), w.metric);
        assert.deepEqual(await p.locator("#ledSplit .opt").allTextContents(), w.split);
        assert.equal(await p.locator("#ledScope").textContent(), lang === "zh" ? "统计网关调用与会话日志调用；本地拒绝的请求不计入汇总。" : "Gateway and session-log calls; local rejections excluded from totals.");
        assert.equal(await p.locator("#ledMetric .opt.on").textContent(), w.metric[0]);
        assert.equal(await p.locator("#ledSplit .opt.on").textContent(), w.split[0]);
        assert.equal(await p.locator("#period .opt.on").textContent(), lang === "zh" ? "今天" : "Today");
        assert.deepEqual(await names(p), ["claude-sonnet-5", "gpt-6-sol", "gpt-6-luna"]);
        await p.locator("#ledSplit .opt").nth(SPLIT.provider).click(); // exercise provider ranking below
        assert.deepEqual(await names(p), ["Claude", "Relay", "Codex"]);
        assert.deepEqual(await p.locator("#ledRank .rk-sw").evaluateAll((s) => s.map((x) => x.style.background)), ["var(--c1)", "var(--c2)", "var(--c3)"]);
        assert(/\d+%/.test(await p.locator("#ledRank .rk-b").first().textContent()), "a share");
        assert((await p.locator("#ledRank .rk").nth(1).locator(".rk-b .rk-bad").textContent()).includes("3"), "Relay's failures");

        // the columns: a segment of each provider where it had calls
        const keys = await p.locator("#ledChart rect.col").evaluateAll((r) => [...new Set(r.map((x) => x.dataset.k))].sort());
        assert.deepEqual(keys, ["anthropic", "codex", "relay"]);
        const svg = await p.locator("#ledChart svg").boundingBox(), box = await p.locator("#ledChart").boundingBox();
        // and where they are: on the bottom line, inside the plot, each in the slot of its hour
        const pos = await p.locator("#ledChart rect.col").evaluateAll((rs, want) => {
          const s = document.querySelector("#ledChart svg").getBoundingClientRect();
          const boxes = rs.map((r) => ({ k: r.dataset.k, ...r.getBoundingClientRect().toJSON() }));
          const bottom = Math.max(...boxes.map((b) => b.bottom));
          const grid = [...document.querySelectorAll("#ledChart svg .grid")].map((g) => g.getBoundingClientRect().bottom);
          return { inside: boxes.every((b) => b.top >= s.top - 1 && b.bottom <= s.bottom + 1 && b.left >= s.left - 1 && b.right <= s.right + 1), onGrid: Math.abs(bottom - Math.max(...grid)) < 2,
            relay: Math.min(...boxes.filter((b) => b.k === "relay").map((b) => b.left)), claude: Math.min(...boxes.filter((b) => b.k === "anthropic").map((b) => b.left)), left: s.left, width: s.width };
        });
        assert(pos.inside, "the columns are inside the chart");
        assert(pos.onGrid, "the columns stand on the bottom line");
        const hourAt = (x) => ((x - pos.left) / pos.width) * 24;
        assert(hourAt(pos.relay) > 6.5 && hourAt(pos.relay) < 8.6, "Relay's first column is in hour 7: " + hourAt(pos.relay));
        assert(hourAt(pos.claude) > 12.5 && hourAt(pos.claude) < 14.6, "Claude's first column is in hour 13: " + hourAt(pos.claude));
        assert(svg.x >= box.x - 1 && svg.x + svg.width <= box.x + box.width + 1, "the chart is within its box");
        const axis = await p.locator("#ledChart svg .axis").allTextContents();
        assert(axis.includes("00:00") && axis.some((s) => /M$|万|亿/.test(s)), "hours below, tokens at the side: " + axis);

        // the pointer over a column: the hour and what each had of it
        const before = await p.locator("#view-usage").evaluate((v) => v.scrollTop);
        await p.mouse.move(svg.x + svg.width * (20.5 / 24), svg.y + svg.height * 0.5); // hour 20: Claude and Codex
        assert(await p.locator("#ledChart .tip").isVisible());
        const tip = await p.locator("#ledChart .tip").innerText();
        assert(tip.includes("Claude") && tip.includes("Codex") && !tip.includes("Relay"), "who had hour 20: " + tip);
        assert(tip.includes(w.strip[0] === "Tokens" ? "Total" : "合计"), "and the total: " + tip);
        await p.mouse.move(svg.x + svg.width * (8.5 / 24), svg.y + svg.height * 0.5); // hour 8: Relay alone
        const tip8 = await p.locator("#ledChart .tip").innerText();
        assert(tip8.includes("Relay") && !tip8.includes("Claude"), tip8);
        await p.mouse.move(svg.x + svg.width * 0.5, box.y - 30);
        assert(!(await p.locator("#ledChart .tip").isVisible()), "the tip goes with the pointer");

        // a ranked one stands out, and the others fade
        await p.locator("#ledRank .rk").nth(2).hover();
        const faded = await p.locator("#ledChart rect.col").evaluateAll((r) => r.filter((x) => x.style.opacity === "0.22").length);
        const kept = await p.locator("#ledChart rect.col").evaluateAll((r) => r.filter((x) => x.dataset.k === "codex" && x.style.opacity === "").length);
        assert(faded > 0 && kept > 0, `the others fade: ${faded} faded, ${kept} of Codex's kept`);
        await p.mouse.move(5, 5);

        // the metric: cost has its own axis and ranking (Claude cost most, then Relay, Codex)
        await p.locator("#ledMetric .opt").nth(1).click();
        assert((await p.locator("#ledChart svg .axis").allTextContents()).some((s) => /^[$¥]/.test(s)));
        assert.deepEqual(await names(p), ["Claude", "Relay", "Codex"]);
        await p.locator("#ledMetric .opt").nth(2).click(); // requests: Relay 30, Codex 20, Claude 16
        assert.deepEqual(await names(p), ["Relay", "Codex", "Claude"]);
        assert.equal(await p.locator("#view-usage").evaluate((v) => v.scrollTop), before, "a click moved the page");
        // and what it is split by
        await p.locator("#ledSplit .opt").nth(SPLIT.agent).click();
        assert.deepEqual(await names(p), ["Codex", "Claude Code"]);
        await p.locator("#ledSplit .opt").nth(SPLIT.model).click();
        assert.deepEqual(await names(p), ["gpt-6-sol", "gpt-6-luna", "claude-sonnet-5"]);
        await p.locator("#ledRank .rk").first().click();
        await lastAsked(asked, (q) => q.get("model") === "gpt-6-sol" && !q.has("q"));
        await p.locator("#ledRank .rk").first().click();
        await lastAsked(asked, (q) => !q.has("model"));
        await p.locator("#ledSplit .opt").nth(SPLIT.provider).click();
        await p.locator("#ledMetric .opt").nth(0).click();

        // no rule of another part of the page reaches the ranking: nothing in it but the
        // swatches, the bars and a hovered or picked row has a background of its own
        // (the Sessions heatmap's .l1/.l2 once painted its lines in the accent)
        await p.mouse.move(5, 5);
        const painted = await p.locator("#ledRank *").evaluateAll((els) => els
          .filter((e) => !e.closest(".rk-sw, .rk-bar") && getComputedStyle(e).backgroundColor !== "rgba(0, 0, 0, 0)")
          .map((e) => e.className || e.tagName));
        assert.deepEqual(painted, [], "painted in the ranking");
        // and none of the new parts' short class names is one a bare rule elsewhere styles
        const bare = await p.evaluate(() => {
          const rules = [];
          for (const sheet of document.styleSheets) { try { for (const r of sheet.cssRules) if (r.selectorText) rules.push(r.selectorText); } catch {} }
          const global = new Set();
          for (const sel of rules) for (const part of sel.split(",")) { const m = part.trim().match(/^\.([\w-]+)$/); if (m) global.add(m[1]); }
          const used = new Set();
          for (const e of document.querySelectorAll("#ledDash *, #ledWrap .led-detail *")) for (const c of e.classList) used.add(c);
          const ok = new Set(["segs", "opt", "grow", "text", "sess-pick", "skeleton", "thumb", "on", "ic"]); // the page's shared parts, used as meant
          // the new parts' own, prefixed names are theirs; a short bare one may be anyone's
          const own = /^(led|pu|cx|rk|usage)-/;
          return [...used].filter((c) => global.has(c) && !ok.has(c) && !own.test(c));
        });
        assert.deepEqual(bare, [], "a class a global rule also styles");

        // a click on a provider lists only its requests, on it again all of them
        const n = asked.length;
        await p.locator("#ledRank .rk").nth(1).click();
        await lastAsked(asked, (q) => q.get("provider") === "relay");
        assert(asked.length > n);
        await p.locator("#ledProvider", { hasText: "Relay" }).waitFor();
        assert(await p.locator("#ledRank .rk.on").count() === 1 && (await p.locator("#ledRank .rk.on .rk-nm").textContent()) === "Relay");
        await p.locator("#ledRank .rk").nth(1).click();
        await lastAsked(asked, (q) => !q.get("provider"));
        await p.locator("#ledProvider", { hasText: w.all }).waitFor();
        // the picker beside the agent's does the same
        await p.locator("#ledProvider").click();
        await p.locator(".sess-menu button, .sess-menu [role=option], .sess-menu .opt").filter({ hasText: "Codex" }).first().click();
        await lastAsked(asked, (q) => q.get("provider") === "codex");
        assert.equal(await p.locator("#view-usage").evaluate((v) => v.scrollTop), before, "a click moved the page");

        if (shots) await p.screenshot({ path: path.join(shots, `chart-${engine}-${lang}.png`) });
        assert.deepEqual(errors, []);
      });
    }

    for (const lang of ["en", "zh"]) await t.test(lang + ": a day filters details while keeping the whole chart", async () => {
      const { p, asked, exported, errors } = await open(lang, lang === "zh" ? "dark" : "light", { variant: "daily" });
      await p.locator("#period .opt").nth(1).click();
      const days = p.locator("#ledChart .led-day");
      await days.nth(6).waitFor();
      const initial = await p.locator("#ledChart rect.col").evaluateAll((rs) => rs.map((r) => [r.dataset.day, r.dataset.k, r.getAttribute("height"), r.dataset.color]));
      const scroll = await p.locator("#view-usage").evaluate((v) => v.scrollTop);
      await days.nth(4).click();
      await lastAsked(asked, (q) => q.has("day") && q.get("offset") === "0");
      await p.locator('#ledChart .led-day[aria-pressed="true"]').waitFor();
      const day = asked.at(-1).get("day");
      assert.equal(await p.locator("#ledWrap tr.led-row").count(), 1, "only the selected day's requests");
      assert.deepEqual(await names(p), ["claude-sonnet-5", "gpt-6-luna"]);
      assert.equal(await p.locator("#ledKpi .blk").nth(1).locator(".v").textContent(), "6");
      assert.deepEqual(await p.locator("#ledChart rect.col").evaluateAll((rs) => rs.map((r) => [r.dataset.day, r.dataset.k, r.getAttribute("height"), r.dataset.color])), initial);
      await p.locator("#ledRank .rk").first().hover();
      await p.mouse.move(5, 5);
      assert(await p.locator("#ledChart rect.col").evaluateAll((rs, day) => rs.every((r) => r.dataset.day === day ? r.style.opacity === "" && r.style.fill === r.dataset.color : r.style.opacity === "0.22" && r.style.fill === "var(--faint)"), day), "hovering the ranking keeps the day selection");
      assert.equal(await p.locator("#view-usage").evaluate((v) => v.scrollTop), scroll, "selecting a day moves nothing");
      await p.locator("#ledMetric .opt").nth(1).click();
      assert.equal(await p.locator('#ledChart .led-day[aria-pressed="true"]').count(), 1, "the metric keeps selection");
      if (shots) await p.locator("#ledDash").screenshot({ path: path.join(shots, `day-${engine}-${lang}.png`) });
      await p.setViewportSize({ width: 1100, height: 760 });
      await p.waitForTimeout(100);
      assert.equal(await p.locator('#ledChart .led-day[aria-pressed="true"]').count(), 1, "resize keeps selection");
      await p.locator("#ledExport").click();
      await lastAsked(exported, (q) => q.get("day") === day);
      await p.locator('#ledChart .led-day[aria-pressed="true"]').click();
      await lastAsked(asked, (q) => !q.has("day"));
      await p.waitForFunction(() => document.querySelectorAll("#ledWrap tr.led-row").length === 6);
      assert.equal(await p.locator('#ledChart .led-day[aria-pressed="true"]').count(), 0, "clicking the selected day clears it");
      // Hold the first response so the second click happens before it arrives.
      let release, held, blocked;
      const delay = () => {
        held = new Promise((resolve) => { release = resolve; });
        blocked = false;
      };
      const hold = async (route) => {
        const q = new URL(route.request().url()).searchParams;
        if (!blocked && q.has("day")) {
          blocked = true;
          asked.push(q);
          await held;
          await route.fulfill({ json: page(q, "daily") });
          return;
        }
        await route.fallback();
      };
      const reply = (day) => p.waitForResponse((r) => {
        const url = new URL(r.url());
        return url.pathname === "/api/usage/requests" && (url.searchParams.get("day") || "") === day;
      }).then((r) => r.finished());
      await p.route("**/api/usage/requests?*", hold);
      delay();
      const beforeClicks = asked.length, clickedDay = await days.nth(4).getAttribute("data-day");
      const requested = p.waitForRequest((r) => new URL(r.url()).searchParams.get("day") === clickedDay);
      const selectedReply = reply(clickedDay), clearedReply = reply("");
      await days.nth(4).dispatchEvent("click");
      await requested;
      await days.nth(4).dispatchEvent("click");
      release();
      await Promise.all([selectedReply, clearedReply]);
      await p.evaluate(() => new Promise(requestAnimationFrame));
      await lastAsked(asked, (q) => asked.length > beforeClicks && !q.has("day"));
      assert.equal(await p.locator("#ledWrap tr.led-row").count(), 6, "the late response does not replace the cleared details");
      assert.equal(await p.locator('#ledChart .led-day[aria-pressed="true"]').count(), 0, "two quick clicks clear selection");

      // A delayed redraw must not take focus back after the user moves it.
      delay();
      const movedRequest = p.waitForRequest((r) => new URL(r.url()).searchParams.get("day") === clickedDay);
      const movedReply = reply(clickedDay);
      await days.nth(4).focus();
      await p.keyboard.press("Enter");
      await movedRequest;
      const away = p.locator('#ledQ');
      await away.focus();
      release();
      await movedReply;
      await p.locator('#ledChart .led-day[aria-pressed="true"]').waitFor();
      assert(await away.evaluate((e) => e === document.activeElement), "the redraw does not steal focus");
      const resetReply = reply("");
      await days.nth(4).dispatchEvent("click");
      await resetReply;
      await p.waitForFunction(() => !document.querySelector('#ledChart .led-day[aria-pressed="true"]'));
      await p.unroute("**/api/usage/requests?*", hold);
      const keyboardDay = await days.nth(1).getAttribute("data-day");
      await days.nth(1).focus();
      await p.keyboard.press("Space");
      await lastAsked(asked, (q) => q.has("day"));
      await p.waitForFunction(() => document.querySelectorAll("#ledWrap tr.led-row").length === 0);
      await p.waitForFunction((day) => document.activeElement?.matches('#ledChart .led-day[aria-pressed="true"]') && document.activeElement.dataset.day === day, keyboardDay);
      await p.keyboard.press("Enter");
      await lastAsked(asked, (q) => !q.has("day"));
      await p.waitForFunction((day) => document.activeElement?.matches('#ledChart .led-day[aria-pressed="false"]') && document.activeElement.dataset.day === day, keyboardDay);
      await p.keyboard.press("Space");
      await lastAsked(asked, (q) => q.has("day"));
      await p.waitForFunction((day) => document.activeElement?.matches('#ledChart .led-day[aria-pressed="true"]') && document.activeElement.dataset.day === day, keyboardDay);
      assert(await p.locator("#ledDash").isVisible(), "the empty day keeps the chart");
      assert.equal(await names(p).then((x) => x.length), 0, "the empty day clears the ranking");
      await days.nth(2).click();
      await p.waitForFunction(() => document.querySelectorAll("#ledWrap tr.led-row").length === 1);
      await p.locator("#period .opt").nth(0).click();
      await lastAsked(asked, (q) => q.get("period") === "today" && !q.has("day"));
      assert.deepEqual(errors, []);
    });

    await t.test("the metric is remembered, while today and model are defaults", async () => {
      const first = await open("en", "light");
      await first.p.locator("#ledMetric .opt").nth(1).click();
      await first.p.locator("#ledSplit .opt").nth(SPLIT.provider).click();
      await first.p.evaluate(() => localStorage.setItem("magpie.ledSplit", "provider"));
      const second = await open("en", "light", { ctx: first.context });
      assert.equal(await second.p.locator("#ledMetric .opt.on").textContent(), "Cost");
      assert.equal(await second.p.locator("#ledSplit .opt.on").textContent(), "Model");
      assert.deepEqual(await names(second.p), ["claude-sonnet-5", "gpt-6-sol", "gpt-6-luna"]);
    });

    await t.test("dark, and narrow", async () => {
      const { p, errors } = await open("en", "dark", { width: 560 });
      const fits = await p.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1 && document.getElementById("view-usage").scrollWidth <= document.getElementById("view-usage").clientWidth + 1);
      assert(fits, "the page doesn't scroll sideways");
      const chart = await p.locator("#ledChart").boundingBox(), rank = await p.locator("#ledRank").boundingBox(), svg = await p.locator("#ledChart svg").boundingBox();
      assert(svg.width > 300 && svg.x + svg.width <= chart.x + chart.width + 1, "the chart fits at 560");
      assert(rank.y >= chart.y + chart.height - 1, "the ranking goes under the chart when there is no room beside it");
      assert.equal(await p.locator("#ledKpi").evaluate((k) => getComputedStyle(k).gridTemplateColumns.split(" ").length), 2, "two totals to a row");
      // the lines between the columns of one bar belong to the totals' cards:
      // a card starting a row has no left line, and the rows under the first
      // are divided — the two sources the Requests tab draws keep none of it
      assert.equal(await p.locator("#ledKpi .blk").nth(2).evaluate((b) => getComputedStyle(b).borderLeftWidth), "0px", "a totals card starting a row carries no left line");
      assert.notEqual(await p.locator("#ledKpi .blk").nth(2).evaluate((b) => getComputedStyle(b).borderTopWidth), "0px", "and the rows are divided");
      await p.locator("#usageTab .opt").nth(1).click();
      await p.locator("#ledVia .blk").first().waitFor();
      assert.equal(await p.locator("#ledVia .blk").nth(1).evaluate((b) => getComputedStyle(b).borderLeftWidth), "0px", "a source cell carries no line between cells of one bar");
      // the chart follows the window
      await p.setViewportSize({ width: 1100, height: 760 });
      await p.waitForTimeout(300);
      const wider = await p.locator("#ledChart svg").boundingBox();
      assert(wider.width > 500 && (await p.locator("#ledRank").boundingBox()).x > wider.x + wider.width - 5, `beside it again: ${svg.width} → ${wider.width}`);
      if (process.env.ARTIFACT_DIR) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `chart-${engine}-dark.png`) });
      assert.deepEqual(errors, []);
    });

    await t.test("a long ranking is as tall as the chart and scrolls in itself", async () => {
      const { p, errors, asked } = await open("en", "light", { variant: "many" });
      await p.locator("#ledRank .rk").nth(7).waitFor();
      const box = async () => {
        const chart = await p.locator("#ledChart").boundingBox(), rank = await p.locator("#ledRank").boundingBox();
        const [top, height, client] = await p.locator("#ledRank").evaluate((r) => [r.scrollTop, r.scrollHeight, r.clientHeight]);
        return { chart, rank, top, height, client };
      };
      let b = await box();
      const plot = await p.locator("#ledChart .plot").boundingBox();
      assert(Math.abs(b.rank.height - plot.height) <= 1 && Math.abs(b.chart.height - plot.height) <= 1, `the ranking is ${b.rank.height}px beside a ${plot.height}px chart`);
      assert(b.height > b.client + 40, "the ranking holds more than it shows");
      // no scrollbar: the edge with more beyond it fades
      const look = () => p.locator("#ledRank").evaluate((r) => ({ bar: r.offsetWidth - r.clientWidth, above: r.classList.contains("more-above"), below: r.classList.contains("more-below"), mask: getComputedStyle(r).webkitMaskImage || getComputedStyle(r).maskImage }));
      let seen = await look();
      assert.equal(seen.bar, 0, "no scrollbar");
      assert(!seen.above && seen.below && /gradient/.test(seen.mask), "at the top, only the foot fades");
      // but a rail says there is more, and how much: beside the ranking, as
      // tall as it, its thumb at the top and as long as the part in sight
      const rail = async () => {
        const r = await p.locator("#ledRail").boundingBox(), th = await p.locator("#ledRail i").boundingBox();
        return { r, th };
      };
      let g = await rail();
      assert(g.r && Math.abs(g.r.y - b.rank.y) <= 1 && Math.abs(g.r.height - b.rank.height) <= 1 && g.r.x >= b.rank.x + b.rank.width - 1, "the rail beside the ranking, as tall");
      assert(Math.abs(g.th.y - g.r.y) <= 1 && Math.abs(g.th.height - Math.max(24, (b.client * b.client) / b.height)) <= 1, `the thumb at the top, ${g.th.height}px for ${b.client} of ${b.height}`);
      // the wheel over it scrolls the ranking, not the page
      const view = () => p.locator("#view-usage").evaluate((v) => v.scrollTop);
      const before = await view();
      await p.mouse.move(b.rank.x + b.rank.width / 2, b.rank.y + b.rank.height / 2);
      await p.mouse.wheel(0, 120);
      for (let i = 0; i < 40 && (await box()).top === 0; i++) await p.waitForTimeout(25);
      b = await box();
      assert(b.top > 0, "the ranking scrolled");
      seen = await look();
      assert(seen.above && seen.below === b.top < b.height - b.client - 1, "scrolled, the head fades too");
      g = await rail();
      assert(Math.abs(g.th.y - g.r.y - ((g.r.height - g.th.height) * b.top) / (b.height - b.client)) <= 1.5, "the thumb follows the scroll");
      assert.equal(await view(), before, "the page stayed where it was");
      // a pick redraws the ranking where it was scrolled to (clicked where it
      // is, as a reader does, not scrolled to first as a test's click would)
      const rows = p.locator("#ledRank .rk:not(.plain)");
      let hit = -1, at;
      for (let i = (await rows.count()) - 1; i >= 0 && hit < 0; i--) {
        at = await rows.nth(i).boundingBox();
        if (at.y >= b.rank.y && at.y + at.height <= b.rank.y + b.rank.height) hit = i;
      }
      assert(hit > 0, "a row in sight");
      await p.mouse.click(at.x + at.width / 2, at.y + at.height / 2);
      await lastAsked(asked, (q) => q.get("provider") === "p" + hit);
      await p.locator("#ledRank .rk.on").waitFor();
      const after = await box();
      assert.equal(after.top, b.top, "the pick kept the ranking's scroll");
      assert.equal(await view(), before, "a click moved the page");
      // the thumb dragged to the rail's foot takes the ranking to its end; a
      // click at the rail's head, back to its start
      g = await rail();
      await p.mouse.move(g.th.x + g.th.width / 2, g.th.y + g.th.height / 2);
      await p.mouse.down();
      await p.mouse.move(g.th.x + g.th.width / 2, g.r.y + g.r.height + 40, { steps: 6 });
      await p.mouse.up();
      b = await box();
      assert(b.top >= b.height - b.client - 1, "dragged to its end");
      g = await rail();
      assert(Math.abs(g.th.y + g.th.height - (g.r.y + g.r.height)) <= 1.5, "the thumb at the rail's foot");
      await p.mouse.click(g.r.x + g.r.width / 2, g.r.y + 3);
      assert.equal((await box()).top, 0, "a click at the rail's head, back to the start");
      assert.equal(await view(), before, "the rail moved the page");
      if (shots) await p.locator(".led-trend").screenshot({ path: path.join(shots, `chart-${engine}-long-ranking.png`) });
      // narrow, the ranking goes under the chart at its own height
      await p.setViewportSize({ width: 560, height: 760 });
      await p.waitForTimeout(300);
      const n = await box();
      assert(n.rank.y >= n.chart.y + n.chart.height - 1 && n.height <= n.client + 1, "under the chart, the whole ranking shows");
      seen = await look();
      assert(!seen.above && !seen.below, "nothing fades when it all shows");
      assert(await p.locator("#ledRail").isHidden(), "no rail when it all shows");
      // wide again, and scrolled to its end: only the head fades
      await p.setViewportSize({ width: 1180, height: 760 });
      await p.waitForTimeout(300);
      await p.locator("#ledRank").evaluate((r) => { r.scrollTop = r.scrollHeight; });
      for (let i = 0; i < 40 && (await look()).below; i++) await p.waitForTimeout(25);
      seen = await look();
      assert(seen.above && !seen.below, "at the end, only the head fades");
      assert(await p.locator("#ledRail").isVisible(), "wide again, the rail is back");
      assert.deepEqual(errors, []);
      // headless hides scrollbars: Chromium drawing the classic one, as macOS
      // does with a mouse plugged in, has none on the ranking either
      if (engine === "chromium") {
        const classic = await chromium.launch({ channel: "chromium", ignoreDefaultArgs: ["--hide-scrollbars"] });
        try {
          const c = await open("en", "light", { variant: "many", ctx: await classic.newContext({ viewport: { width: 1180, height: 760 }, reducedMotion: "reduce" }) });
          await c.p.addStyleTag({ content: "*::-webkit-scrollbar { width: 14px; height: 14px; } *::-webkit-scrollbar-thumb { background: #888; }" });
          assert(await c.p.locator("#view-usage").evaluate(() => { const d = document.createElement("div"); d.style.cssText = "width:50px;height:50px;overflow:scroll"; document.body.append(d); const w = d.offsetWidth - d.clientWidth; d.remove(); return w > 0; }), "this Chromium draws scrollbars");
          assert.equal(await c.p.locator("#ledRank").evaluate((r) => r.offsetWidth - r.clientWidth), 0, "no scrollbar on the ranking where the system draws one");
          assert.deepEqual(c.errors, []);
        } finally {
          await classic.close();
        }
      }
    });

    await t.test("no price, no requests", async () => {
      const a = await open("en", "light", { variant: "unpriced" });
      await a.p.locator("#ledMetric .opt").nth(1).click();
      assert.equal((await a.p.locator("#ledChart .none").textContent()).trim(), "No known price for these requests");
      const b = await open("en", "light", { variant: "none" });
      await b.p.waitForTimeout(300);
      assert(await b.p.locator("#ledDash").isHidden(), "nothing to total or chart");
      assert.deepEqual([...a.errors, ...b.errors], []);
    });
  });
}
