// Run with Node's test runner and Playwright on the module path; see README.md.
// The Usage page's Requests (Discord 范不着: a per-request ledger, as sub2api
// has, to set beside the vendors' bills): one row per request, newest first,
// with the model asked for, where it went, the model sent and the one the
// reply named — amber only when it is another model — the effort, the tokens,
// cache writes and reads, the cost, the time taken and the status, a failed
// one marked by a red dot and code. The agent, Failed and search filters and
// the pages ask the server for what they show; Export CSV posts the same
// filters. A click with the page scrolled moves nothing, and a narrow window
// scrolls the table in its box, never the page sideways. No left-border
// stripe. English and Chinese, light and dark; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { click, inView } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();

// 130 requests, newest first: a swapped one, one under a dated name, a
// failure (passed on by another computer's magpie), one from before Requested was kept, then plain ones
const ROWS = [
  { route_id: 123, t: new Date(now - 60e3).toISOString(), agent: "codex", agentName: "Codex", icon: "codex-color", provider: "relay", providerName: "Relay", host: "team", req: "sol", model: "gpt-6-sol", served: "gpt-6-luna", swapped: true, effort: "high", in: 12840, out: 912, cache_read: 8192, reasoning: 300, ms: 4210, ttft_ms: 820, status: 200, session: "019a2b", cost: 0.0421, priced: true },
  { route_id: 999, t: new Date(now - 120e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color", provider: "anthropic", providerName: "Claude", host: "ann@example.com", req: "sonnet", model: "claude-sonnet-5", served: "claude-sonnet-5-20260801", effort: "", in: 3021, out: 440, cache_write: 2048, cache_read: 61000, ms: 2380, status: 200, cost: 0.0312, priced: true },
  { route_id: 123, t: new Date(now - 180e3).toISOString(), agent: "codex", agentName: "Codex", via: "office-mac", icon: "codex-color", provider: "relay", providerName: "Relay", host: "team", req: "sol", model: "gpt-6-sol", in: 0, out: 0, ms: 610, status: 429, cost: 0, priced: false },
  { t: new Date(now - 240e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color", provider: "deepseek", providerName: "DeepSeek", model: "deepseek-v4", in: 900, out: 120, ms: 1320, status: 200, cost: 0, priced: false },
];
for (let i = 0; i < 126; i++) {
  ROWS.push({ t: new Date(now - (300 + i * 60) * 1e3).toISOString(), agent: i % 2 ? "claude" : "codex", agentName: i % 2 ? "Claude Code" : "Codex", icon: i % 2 ? "claudecode-color" : "codex-color", provider: "relay", providerName: "Relay", host: "team", req: "sol", model: "gpt-6-sol", served: "gpt-6-sol", in: 1000 + i, out: 100, ms: 900, status: 200, cost: 0.001, priced: true });
}
ROWS.forEach((r, i) => { r.callerKeyId = i % 2 ? "server" : "laptop"; });

function page(q) {
  let rows = ROWS;
  if (q.get("route")) rows = rows.filter((r) => r.route_id === Number(q.get("route")));
  if (q.get("callerKey")) rows = rows.filter((r) => r.callerKeyId === q.get("callerKey"));
  if (q.get("agent")) rows = rows.filter((r) => r.agent === q.get("agent"));
  if (q.get("failed") === "1") rows = rows.filter((r) => r.status >= 400);
  const s = (q.get("q") || "").toLowerCase();
  if (s) rows = rows.filter((r) => [r.req, r.model, r.served, r.provider, r.host, r.session, r.effort].some((v) => (v || "").toLowerCase().includes(s)));
  const offset = +q.get("offset") || 0, limit = +q.get("limit") || 100;
  const sum = (k) => rows.reduce((a, r) => a + (r[k] || 0), 0);
  return {
    period: q.get("period"), rows: rows.slice(offset, offset + limit), offset, total: rows.length,
    calls: rows.length, errors: rows.filter((r) => r.status >= 400).length,
    input: sum("in"), output: sum("out"), cache_read: sum("cache_read"), cache_write: sum("cache_write"), reasoning: sum("reasoning"),
    cost: sum("cost"), unpriced: rows.filter((r) => !r.priced && r.in).length,
    agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }, { id: "codex", name: "Codex", icon: "codex-color" }],
    callerKeys: [{ id: "laptop", name: "Laptop" }, { id: "server", name: "Server" }],
  };
}

function server(lang, theme, asked) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/gateway/trace") {
      await new Promise((r) => setTimeout(r, 100));
      return json({ mine: false, seq: 0, routes: [], totals: { requests: 0, rerouted: 0, errors: 0 }, now: new Date().toISOString() });
    }
    if (url.pathname === "/api/gateway/history") return json({ days: [], routes: [], cut: false });
    if (url.pathname === "/api/gateway/route") {
      if (url.searchParams.get("id") !== "123") return route.fulfill({ status: 404, body: "not found" });
      return json({ id: 123, time: new Date(now - 86400e3).toISOString(), agent: "codex", model: "gpt-6-sol", provider: "relay",
        order: [{ id: "relay", provider: "relay", name: "Relay", model: "gpt-6-sol", kind: "provider", routing: "order" }],
        tries: [{ id: "relay", model: "gpt-6-sol", start: new Date(now - 86400e3).toISOString(), done: true, status: 200, ms: 50 }],
        done: true, status: 200, ms: 50 });
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: false } });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme }, fx: { rate: 7.2, at: new Date().toISOString() } });
    if (url.pathname === "/api/usage/requests") {
      asked.push(url.searchParams);
      return json(page(url.searchParams));
    }
    if (url.pathname === "/api/usage/requests/export") {
      asked.push(Object.assign(new URLSearchParams(url.searchParams), { method: req.method() }));
      return json({ path: "~/Downloads/magpie-requests-30d-2026-09-29.csv", rows: page(url.searchParams).total });
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") {
      return json({
        calls: 130, errors: 1, input: 160000, output: 14000, cache_read: 69000, cache_write: 2048, reasoning: 300, unpriced: 2, cost: 12.34, bucket: "day",
        series: [{ label: "Mon", input: 80000, output: 7000, calls: 65, cost: 6 }, { label: "Tue", input: 80000, output: 7000, calls: 65, cost: 6.34 }],
        agents: [{ name: "Codex", calls: 65, cost: 6 }], models: [{ name: "gpt-6-sol", calls: 65, cost: 6 }], path: "~/.config/magpie/usage.jsonl",
      });
    }
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
  en: {
    tabs: ["Overview", "Requests", "Sessions"],
    cols: ["Time", "Agent", "Requested", "Provider · account", "Sent", "Served", "Effort", "In", "Out", "Cache write", "Cache read", "Cost", "Duration", "Status"],
    sum: "130 requests", pager: "1–100 of 130", older: "Older", newer: "Newer", failed: "Failed", export: "Export CSV",
    why: "The vendor was asked for gpt-6-sol, and its reply says gpt-6-luna answered it", saved: "Saved 130 requests to ~/Downloads/magpie-requests-30d-2026-09-29.csv",
    none: "No requests match these filters.", bad: "Failed: the agent was answered 429", via: "Codex · via office-mac",
  },
  zh: {
    tabs: ["概览", "请求", "会话"],
    cols: ["时间", "Agent", "请求模型", "供应商 · 账号", "发送模型", "实际模型", "推理强度", "输入", "输出", "缓存写入", "缓存读取", "费用", "耗时", "状态"],
    sum: "130 个请求", pager: "第 1–100 条，共 130 条", older: "较早", newer: "较新", failed: "失败", export: "导出 CSV",
    why: null, saved: "已将 130 个请求保存到 ~/Downloads/magpie-requests-30d-2026-09-29.csv",
    none: "没有符合这些筛选条件的请求。", bad: "失败：Agent 收到的是 429", via: "Codex · 来自 office-mac",
  },
};

const scrolled = (page) => page.locator("#view-usage").evaluate((v) => v.scrollTop);
// the reader wheels a control out from under the header or the footer, as
// a reader would: the dashboard above the table leaves its first rows low
async function wheelTo(page, loc) {
  await page.mouse.move(600, 300);
  for (let i = 0; i < 50; i++) {
    const b = await loc.boundingBox(), v = await page.locator("#view-usage").boundingBox();
    if (b && b.y >= v.y && b.y + b.height < v.y + v.height - 40) return;
    await page.mouse.wheel(0, b && b.y < v.y ? -120 : 120);
    await page.waitForTimeout(30);
  }
  assert.fail("the row never came up");
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Usage page's Requests", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const shots = process.env.ARTIFACT_DIR;
    if (shots) await fs.mkdir(shots, { recursive: true });

    const open = async (lang, theme, asked, width = 1180, height = 640) => {
      const errors = [];
      const page = await (await browser.newContext({ viewport: { width, height }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, theme, asked));
      await page.goto("http://magpie.test/");
      await page.locator('nav [data-view="usage"], [data-view="usage"]').first().click();
      await page.locator("#usageTab .opt").nth(1).click();
      await page.locator("#ledWrap .led tbody tr").first().waitFor();
      return { page, errors };
    };
    // the last ledger request, once it has come with want
    const lastAsked = async (page, asked, want) => {
      for (let i = 0; i < 60 && !(asked.length && want(asked.at(-1))); i++) await page.waitForTimeout(40);
      assert(asked.length && want(asked.at(-1)), "asked: " + (asked.at(-1) || ""));
      await page.waitForTimeout(150);
    };

    for (const lang of ["en", "zh"]) {
      const w = L[lang];
      await t.test(lang + ": an unavailable caller filter is cleared", async () => {
        const asked = [];
        const { page: tab, errors } = await open(lang, "light", asked);
        await tab.locator("#ledKey").click();
        await tab.locator(".sess-menu .pm-item", { hasText: "Laptop" }).click();
        await lastAsked(tab, asked, q => q.get("callerKey") === "laptop");
        await tab.route("**/api/usage/requests?**", async (route) => {
          const q = new URL(route.request().url()).searchParams;
          asked.push(q);
          const data = page(q);
          data.callerKeys = [{ id: "server", name: "Server" }];
          data.rows = data.rows.filter(r => r.callerKeyId === "server");
          data.total = data.rows.length;
          await route.fulfill({ json: data });
        });
        await tab.locator("#usageReload").click();
        await lastAsked(tab, asked, q => !q.has("callerKey") && q.get("offset") === "0");
        assert.equal(await tab.locator("#ledKey").textContent(), lang === "zh" ? "全部网关密钥" : "All gateway keys");
        assert(await tab.locator(".led-row").count() > 0, "stale filtered empty data must be reloaded");
        assert.deepEqual(errors, []);
        await tab.close();
      });
      await t.test(lang, async () => {
        const asked = [];
        const { page, errors } = await open(lang, "light", asked);
        assert.deepEqual(await page.locator("#usageTab .opt").allTextContents(), w.tabs);
        assert.equal(asked.at(-1).get("period"), "today");
        assert.equal(asked.at(-1).get("offset"), "0");
        assert.deepEqual(await page.locator(".led thead th").allTextContents(), w.cols);
        assert.equal(await page.locator(".led tbody tr").count(), 100, "a page of 100");
        assert.equal(await page.locator("#ledSum").textContent().then((s) => s.split(" · ")[0]), w.sum);
        assert.equal((await page.locator("#ledPager > span").textContent()), w.pager);

        // the first row: asked for sol, sent gpt-6-sol, served gpt-6-luna, amber
        const first = page.locator(".led tbody tr").first().locator("td");
        assert.equal(await first.nth(2).textContent(), "sol");
        assert.equal(await first.nth(3).textContent(), "Relay · team");
        assert.equal(await first.nth(4).textContent(), "gpt-6-sol");
        assert.equal(await first.nth(6).textContent(), "high");
        assert.equal(await page.locator(".led .swap").count(), 1, "only the swapped row is marked");
        const swap = page.locator(".led tbody tr").first().locator(".swap");
        assert.equal(await swap.textContent(), "gpt-6-luna");
        if (w.why) assert((await swap.getAttribute("title")).startsWith(w.why));
        else assert(/gpt-6-luna/.test(await swap.getAttribute("title")));
        // the dated name: plain, not marked
        const second = page.locator(".led tbody tr").nth(1).locator("td");
        assert.equal(await second.nth(5).locator(".swap").count(), 0);
        assert.equal(await second.nth(5).textContent(), "claude-sonnet-5-20260801");
        assert.equal(await second.nth(3).textContent(), "Claude · ann@example.com");
        // the failure: its row, a red dot and code
        assert.equal(await page.locator(".led tr.bad").count(), 1);
        const bad = page.locator(".led tr.bad td").last();
        assert.equal((await bad.textContent()).trim(), "429");
        assert.equal(await bad.getAttribute("title"), w.bad);
        // its agent is on another computer, whose magpie passed it on (Jorben on Discord)
        assert.equal(await page.locator(".led tr.bad td").nth(1).textContent(), w.via);
        const [dotBad, dotOk] = await page.evaluate(() => [
          getComputedStyle(document.querySelector(".led tr.bad .st .dot")).backgroundColor,
          getComputedStyle(document.querySelector(".led tbody tr:not(.bad) .st .dot")).backgroundColor,
        ]);
        assert.notEqual(dotBad, dotOk, "a failure's dot is another colour");
        // a request from before Requested was kept
        assert.equal(await page.locator(".led tbody tr").nth(3).locator("td").nth(2).textContent(), "—");
        // no coloured stripe down the box's, a row's or a cell's left side
        const stripes = await page.evaluate(() => [...document.querySelectorAll("#ledWrap, #ledWrap tr, #ledWrap td")]
          .filter((e) => { const c = getComputedStyle(e); return parseFloat(c.borderLeftWidth) > 0 && (c.borderLeftWidth !== c.borderRightWidth || c.borderLeftColor !== c.borderRightColor); }).length);
        assert.equal(stripes, 0, "no left-border accents");

        if (shots) await page.screenshot({ path: path.join(shots, `${engine}-usage-ledger-${lang}-light.png`) });

        // scrolled to the pager: Older asks for the next page, a shorter
        // one, and the pager stays where it is on the screen (room kept at
        // the view's foot, not the page riding up); so does Newer
        const viewBox = await page.locator("#view-usage").boundingBox();
        await page.mouse.move(viewBox.x + 40, viewBox.y + viewBox.height / 2);
        for (let i = 0; i < 200 && !(await page.locator("#ledPager").evaluate((p) => { const r = p.getBoundingClientRect(); return r.bottom < document.querySelector("#view-usage").getBoundingClientRect().bottom - 10; })); i++) {
          await page.mouse.wheel(0, 120);
          await page.waitForTimeout(40);
        }
        await page.waitForTimeout(300);
        assert(await scrolled(page) > 0, "the page must be scrolled");
        const pagerAt = () => page.locator("#ledPager").evaluate((p) => Math.round(p.getBoundingClientRect().top));
        const at = await pagerAt();
        await click(page, page.locator("#ledPager button", { hasText: w.older }));
        await lastAsked(page, asked, (q) => q.get("offset") === "100");
        await page.waitForFunction(() => document.querySelectorAll(".led tbody tr").length === 30);
        await page.waitForTimeout(200);
        assert(Math.abs(await pagerAt() - at) <= 1, "Older leaves the pager where it was, within pixel rounding");
        assert.equal(await page.locator("#ledPager > span").textContent(), lang === "en" ? "101–130 of 130" : "第 101–130 条，共 130 条");
        await click(page, page.locator("#ledPager button", { hasText: w.newer }));
        await lastAsked(page, asked, (q) => q.get("offset") === "0");
        await page.waitForFunction(() => document.querySelectorAll(".led tbody tr").length === 100);
        await page.waitForTimeout(200);
        assert(Math.abs(await pagerAt() - at) <= 1, "Newer leaves the pager where it was, within pixel rounding");

        // back up: the filters ask the server
        for (let i = 0; i < 200 && (await scrolled(page)) > 0; i++) { await page.mouse.wheel(0, -400); await page.waitForTimeout(10); }
        await page.waitForTimeout(100);
        await click(page, page.locator("#ledStatus .opt", { hasText: w.failed }));
        await lastAsked(page, asked, (q) => q.get("failed") === "1" && q.get("offset") === "0");
        await page.waitForFunction(() => document.querySelectorAll(".led tbody tr").length === 1);
        assert.equal(await page.locator(".led tr.bad").count(), 1);
        assert(await page.locator("#ledPager").isHidden(), "one page: no pager");
        await click(page, page.locator("#ledStatus .opt").first());
        await lastAsked(page, asked, (q) => !q.has("failed"));

        await click(page, page.locator("#ledAgent"));
        await page.locator(".sess-menu .pm-item", { hasText: "Claude Code" }).click();
        await lastAsked(page, asked, (q) => q.get("agent") === "claude");
        await page.waitForFunction(() => [...document.querySelectorAll(".led tbody tr td:nth-child(2)")].every((c) => c.textContent === "Claude Code"));
        assert.equal(await page.locator("#ledAgent").textContent(), "Claude Code");

        await inView(page, page.locator("#ledQ"));
        await page.locator("#ledQ").fill("sonnet");
        await lastAsked(page, asked, (q) => q.get("q") === "sonnet" && q.get("agent") === "claude");
        await page.waitForFunction(() => document.querySelectorAll(".led tbody tr").length === 1);
        await page.locator("#ledQ").fill("nothing-like-it");
        await lastAsked(page, asked, (q) => q.get("q") === "nothing-like-it");
        await page.locator(".led-none").waitFor();
        assert.equal(await page.locator(".led-none").textContent(), w.none);
        assert(await page.locator("#ledExport").isDisabled(), "nothing to export");
        await page.locator("#ledQ").press("Escape");
        await lastAsked(page, asked, (q) => !q.has("q") && q.get("agent") === "claude");
        await click(page, page.locator("#ledAgent"));
        await page.locator(".sess-menu .pm-item").first().click();
        await lastAsked(page, asked, (q) => !q.has("agent"));
        await page.waitForFunction(() => document.querySelectorAll(".led tbody tr").length === 100);

        // the period: today, and the ledger asks again from the first page
        await click(page, page.locator("#period .opt").first());
        await lastAsked(page, asked, (q) => q.get("period") === "today" && q.get("offset") === "0");

        // Export CSV posts the filters shown, no page, and says where it went
        await click(page, page.locator("#ledExport"));
        await lastAsked(page, asked, (q) => q.method === "POST");
        const ex = asked.findLast((q) => q.method === "POST"); // a refresh may ask after it
        assert.equal(ex.get("period"), "today");
        assert.equal(asked.filter((q) => q.method === "POST").length, 1, "one export");
        assert(!ex.has("offset") && !ex.has("limit"), "every page is exported");
        await page.waitForFunction((s) => document.querySelector("#status").textContent === s, w.saved);
        assert.deepEqual(errors, []);
      });

      await t.test(lang + ": request routing links", async () => {
        const asked = [];
        const { page, errors } = await open(lang, "light", asked);
        const lookups = [];
        page.on("request", req => { if (req.url().includes("/api/gateway/route?")) lookups.push(new URL(req.url())); });
        const originalPeriod = asked.at(-1).get("period");
        const routeTitle = lang === "en" ? "View routing" : "查看路由";
        const usageTitle = lang === "en" ? "View usage" : "查看用量";
        assert.equal(await page.locator(".led tbody tr").nth(3).locator("button").count(), 0, "old rows have no route link");
        // the dashboard above the table can leave its first row below the fold:
        // bring it up first, and the return must come back to where it was
        const link = page.locator(".led tbody tr").first().getByTitle(routeTitle);
        await wheelTo(page, link);
        const left = await scrolled(page);
        await link.click();
        await page.locator("#view-routing").waitFor({ state: "visible" });
        await page.locator(".rt-log-head").getByText(usageTitle, { exact: true }).waitFor();
        assert.match(await page.locator(".rt-steps").textContent(), /gpt-6-sol/);
        assert.equal(lookups[0].searchParams.get("day"), ROWS[0].t.slice(0,10));
        // History stays usable even when another process serves the gateway.
        await page.waitForTimeout(5200);
        assert(await page.locator(".rt-off").isHidden());
        assert.equal(await page.locator("#status").textContent(), "", "navigation reports no error");
        if (shots) await page.screenshot({ path: path.join(shots, `${engine}-route-link-${lang}.png`) });
        await page.locator(".rt-log-head").getByText(usageTitle, { exact: true }).click();
        await lastAsked(page, asked, (q) => q.get("route") === "123" && q.get("period") === "all");
        await page.waitForFunction(() => document.querySelectorAll(".led tbody tr").length === 2);
        assert(await page.locator("#ledRoute").isVisible());
        assert.match(await page.locator("#ledRouteLabel").textContent(), /^(Request: |请求：).*gpt-6-sol/);
        assert(!await page.locator("#ledRouteLabel").textContent().then(text=>text.includes("#123")));
        // the shorter page can't hold it all: the most it holds, then
        await page.waitForFunction((left) => { const v = document.querySelector("#view-usage");
          return v.scrollTop === Math.min(left, v.scrollHeight - v.clientHeight); }, left)
          .catch(async () => assert.fail(`return keeps the Usage view position: left at ${left}, back at ${await scrolled(page)}, room ${await page.locator("#view-usage").evaluate((v) => v.scrollHeight - v.clientHeight)}`));
        await wheelTo(page, page.locator("#ledExport"));
        await page.locator("#ledExport").click();
        await lastAsked(page, asked, (q) => q.method === "POST" && q.get("route") === "123");
        await page.locator("#ledRouteClear").click();
        await lastAsked(page, asked, (q) => !q.has("route") && q.get("period") === originalPeriod);
        await page.waitForFunction(() => document.querySelectorAll(".led tbody tr").length === 100);
        // A pruned request stays in Usage and explains why its route cannot open.
        const pruned = page.locator(".led tbody tr").nth(1).getByTitle(routeTitle);
        await wheelTo(page, pruned);
        await pruned.click();
        await page.waitForFunction((msg) => document.querySelector("#status").textContent === msg,
          lang === "en" ? "Routing history for this request is no longer available." : "此请求的路由历史已不可用。");
        assert(await page.locator("#view-usage").isVisible());
        assert.deepEqual(errors, []);
        await page.close();
      });

      await t.test(lang + ": clearing the route restores prior filters", async () => {
        const asked = [];
        const { page, errors } = await open(lang, "light", asked);
        await page.evaluate(() => {
          period = "7d"; ledAgent = "codex"; ledCallerKey = "server"; ledFailed = true; ledQuery = "sol"; ledOffset = 100;
          window.openUsageRoute({ id: 123, time: new Date().toISOString(), model: "gpt-6-sol" });
        });
        await lastAsked(page, asked, q=>q.get("route") === "123" && !q.has("callerKey"));
        assert.equal(await page.locator(".led tbody tr").count(), 2, "the previous caller filter must not hide this route");
        await page.locator("#ledKey").click();
        await page.locator(".sess-menu .pm-item", { hasText: "Laptop" }).click();
        await lastAsked(page, asked, q=>q.get("route") === "123" && q.get("callerKey") === "laptop");
        await page.locator("#ledExport").click();
        await lastAsked(page, asked, q=>q.method === "POST" && q.get("route") === "123" && q.get("callerKey") === "laptop" && !q.has("offset") && !q.has("limit"));
        await page.locator("#ledRouteClear").click();
        await lastAsked(page, asked, q=>!q.has("route") && q.get("period") === "7d" && q.get("agent") === "codex" && q.get("callerKey") === "server" && q.get("failed") === "1" && q.get("q") === "sol" && q.get("offset") === "100");
        assert.equal(await page.locator("#ledQ").inputValue(), "sol");
        assert.deepEqual(errors, []);
        await page.close();
      });
      await t.test(lang + ": newest live route needs no return-to-live button", async () => {
        const asked = [];
        const { page, errors } = await open(lang, "light", asked);
        await page.route("**/api/gateway/trace?*", async route => {
          await page.waitForTimeout(100);
          await route.fulfill({json:{mine:true, seq:1, routes:[{id:123,time:ROWS[0].t,agent:"codex",model:"gpt-6-sol",provider:"relay",order:[],tries:[],done:true,status:200}],totals:{requests:1,rerouted:0,errors:0},now:new Date().toISOString()}}).catch(()=>{});
        });
        await page.waitForTimeout(5500);
        const link = page.locator(".led tbody tr").first().getByTitle(lang === "en" ? "View routing" : "查看路由");
        await wheelTo(page, link);
        await link.click();
        await page.locator("#view-routing").waitFor({state:"visible"});
        assert.equal(await page.locator(".rt-log-head").getByText(lang === "en" ? "Back to live" : "回到实时", {exact:true}).count(), 0);
        assert.deepEqual(errors, []);
        await page.close();
      });

      await t.test(lang + ": dark, and narrow", async () => {
        const { page, errors } = await open(lang, "dark", []);
        await page.waitForTimeout(200);
        if (shots) await page.screenshot({ path: path.join(shots, `${engine}-usage-ledger-${lang}-dark.png`) });
        // the window at its narrowest (MinWidth in app.go)
        await page.setViewportSize({ width: 560, height: 700 });
        await page.waitForTimeout(200);
        // the table scrolls in its box; the page never sideways
        const sideways = () => page.evaluate(() => {
          const v = document.querySelector("#view-usage"), w = document.querySelector("#ledWrap");
          const wide = [...v.querySelectorAll("*")].filter((e) => !w.contains(e) && e.offsetParent && e.getBoundingClientRect().right > v.getBoundingClientRect().right + 1).map((e) => e.id || e.className).slice(0, 8);
          return [document.documentElement.scrollWidth > document.documentElement.clientWidth || v.scrollWidth > v.clientWidth, wide, [w.scrollWidth, w.clientWidth]];
        });
        const [wider, wide, wrap] = await sideways();
        // the sum in the head whole, the words after it given way
        assert(await page.locator("#usageCost").evaluate((c) => c.scrollWidth <= c.clientWidth && c.querySelector("b").getBoundingClientRect().right <= c.getBoundingClientRect().right + 0.5), "the cost is not cut");
        assert(!wider && !wide.length, `the page does not scroll sideways: ${wide}`);
        assert(wrap[0] > wrap[1], `the table scrolls in its box: ${wrap}`);
        if (shots) await page.screenshot({ path: path.join(shots, `${engine}-usage-ledger-${lang}-narrow-dark.png`) });
        // nor with the tab bar's other tabs, now three
        for (const i of [0, 2]) {
          await page.locator("#usageTab .opt").nth(i).click();
          await page.waitForTimeout(300);
          const [wider, wide] = await sideways();
          assert(!wider && !wide.length, `tab ${i}: the page does not scroll sideways: ${wide}`);
        }
        assert.deepEqual(errors, []);
      });
    }
  });
}
