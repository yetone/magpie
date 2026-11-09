// Run with Node's test runner and Playwright on the module path; see README.md.
// magpie's ask of a search API for an agent's web search (cybercaius on
// Discord: which search API a search used couldn't be seen) is a row of
// the Requests tab: a "Search API" badge, the API's name where the model
// asked for is, its host and masked key, how long it took and whether it
// worked; its tooltip says the query is not kept and it counts nowhere.
// A plan's own search service (Kimi Code's, #3528) is badged "Web search"
// under the plan's name.
// An ask with a route and an archive (axelburks on Discord: 搜索API代搜时
// 路由页面不显示是请求了哪个API … 无请求存档) opens both from its details:
// View routing, and the request and answer it sent the API.
// In every language, Chromium and WebKit, wide and narrow, light and dark;
// no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const tot = (calls, tokens, cost) => ({ calls, errors: 0, input: tokens * 0.2, output: tokens * 0.1, cache_write: 0, cache_read: tokens * 0.7, cost });
const ROW = { t: new Date().toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color", provider: "anthropic", providerName: "Claude", model: "claude-sonnet-5", in: 2, out: 600, cache_write: 0, cache_read: 3900, ms: 2380, status: 200, cost: 0.08, priced: true };
const KEPT = "2026-11-05/040654-a02c8d68dfe256d8";
const ASKED = { ...ROW, provider: "", providerName: "", model: "", req: "Tavily", host: "api.tavily.com", providerKeyName: "tvly…ab12", in: 0, out: 0, cache_read: 0, ms: 820, status: 200, cost: 0, priced: false, source: "search", kind: "web_search", route_id: 1793851649094, archive: KEPT };
const ARCHIVED = {
  id: KEPT, call: { kind: "web_search", model: "Tavily", provider: "api.tavily.com", status: 200 },
  request: { method: "POST", path: "/search", headers: { Authorization: ["[REDACTED]"], "Content-Type": ["application/json"] }, body: '{"max_results":5,"query":"latest go","search_depth":"basic"}', size: 61 },
  response: { status: 200, headers: { "Content-Type": ["application/json"] }, body: '{"results":[{"title":"Go downloads","url":"https://go.dev/dl/"}]}', size: 66 },
};
// a plan's own search service (#3528): under the plan, badged as a web search
const PLAN = { ...ASKED, t: new Date(Date.now() - 30000).toISOString(), provider: "kimi", providerName: "Kimi Code", req: "Kimi Code", host: "api.kimi.com", providerKeyName: "", ms: 1300 };
const FAILED = { ...ASKED, t: new Date(Date.now() - 60000).toISOString(), req: "Brave Search", host: "api.search.brave.com", providerKeyName: "", ms: 40, status: 0, err: "Get api.search.brave.com: EOF" };

function page(q, called) {
  const rows = [ROW, ...(called ? [ASKED, PLAN, FAILED] : [])];
  return {
    period: "today", rows, offset: 0, total: rows.length, ...tot(1, 4500, 0.08), reasoning: 0, unpriced: 0,
    bucket: "hour", series: [], by: { provider: [], agent: [], model: [] },
    agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }], providers: [{ id: "anthropic", name: "Claude", icon: "claudecode-color" }],
    through: tot(1, 4500, 0.08), direct: tot(0, 0, 0), purposes: called ? ["kind:web_search", "unmarked"] : ["unmarked"],
  };
}

function server(lang, theme, called, asked) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme }, fx: { rate: 7.2, at: new Date().toISOString() } });
    if (url.pathname === "/api/usage/requests") { asked.push(url.searchParams); return json(page(url.searchParams, called)); }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/archive") return json(`${url.searchParams.get("date")}/${url.searchParams.get("id")}` === KEPT ? ARCHIVED : { error: "not found" });
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

// the badge, the purpose's name (a plan's badge too), a word of the tooltip that says it
// counts nowhere, and the failed status word
const L = {
  en: { badge: "Search API", purpose: "Web search", why: "not counted in any total", bad: "Failed" },
  zh: { badge: "搜索 API", purpose: "联网搜索", why: "不计入任何汇总", bad: "失败" },
  "zh-TW": { badge: "搜尋 API", purpose: "聯網搜尋", why: "不計入任何彙總", bad: "失敗" },
  ja: { badge: "検索 API", purpose: "ウェブ検索", why: "どの合計にも数えません", bad: "失敗" },
  de: { badge: "Such-API", purpose: "Websuche", why: "zählt in keiner Summe", bad: "Fehlgeschlagen" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a search API ask is a row of its own", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const shots = process.env.ARTIFACT_DIR;
    if (shots) await fs.mkdir(shots, { recursive: true });

    const open = async (lang, { theme = "light", width = 1180, called = true } = {}) => {
      const errors = [], asked = [];
      const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
      t.after(() => context.close());
      const p = await context.newPage();
      p.setDefaultTimeout(5000);
      p.setDefaultNavigationTimeout(60000);
      p.on("pageerror", (e) => errors.push(e.message));
      await p.route("**/*", server(lang, theme, called, asked));
      await p.goto("http://magpie.test/");
      await p.locator('[data-view="usage"]').first().click();
      await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
      await p.locator("#ledVia .blk").first().waitFor();
      return { p, errors };
    };

    for (const lang of Object.keys(L)) {
      const w = L[lang];
      for (const width of [1180, 420]) {
        await t.test(`${lang} at ${width}`, async () => {
          const { p, errors } = await open(lang, { width, theme: width < 600 ? "dark" : "light" });
          const badge = p.locator(".led .src.search");
          await badge.first().waitFor();
          assert.equal(await badge.count(), 3);
          assert.equal(await badge.first().textContent(), w.badge);
          const why = await badge.first().getAttribute("title");
          assert(why.includes(w.why) && why.includes("Tavily") && why.includes("Claude Code"), why);
          // the model row has none, and neither is a probe or an MCP call
          assert.equal(await p.locator(".led .src.probe, .led .src.mcp").count(), 0);
          const asked = p.locator(".led tr.led-row", { has: p.locator(".src.search") }).first();
          const text = await asked.textContent();
          assert(text.includes("Tavily") && text.includes("api.tavily.com") && text.includes("tvly…ab12"), text);
          assert((await asked.locator(".st").textContent()).startsWith("200"));
          const plan = p.locator(".led tr.led-row", { hasText: "api.kimi.com" });
          assert.equal(await plan.locator(".src.search").textContent(), w.purpose);
          assert((await plan.locator(".where-name").textContent()).includes("Kimi Code"));
          const failed = p.locator(".led tr.led-row", { hasText: "Brave Search" });
          assert((await failed.locator(".st").textContent()).startsWith(w.bad));
          // its details: the route it went by, and the archive of what it
          // sent the API and what came back
          const detail = await reader.openRequest(p, asked);
          await detail.locator(".led-route-link").waitFor();
          const box = detail.locator(".led-archive .call-archive");
          assert.equal(await box.locator(".call-archive-id").innerText(), KEPT);
          await box.locator(".call-body-head button").first().click();
          await box.locator(".call-details").waitFor();
          const kept = await box.innerText();
          assert(kept.includes("POST /search") && kept.includes("latest go") && kept.includes("go.dev/dl") && kept.includes("[REDACTED]"), kept);
          await reader.closeRequest(p); // back to the list
          await detail.waitFor({ state: "detached" });
          // the purpose picker names it in the page's language
          assert.equal(await p.evaluate(() => purposeOptions(["kind:web_search"], "")[0].name), w.purpose);
          // no coloured stripe down its side, and the page doesn't scroll sideways
          assert(parseFloat(await badge.first().evaluate((b) => getComputedStyle(b).borderLeftWidth)) <= 1);
          assert(await p.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "no sideways scroll");
          if (shots) await p.screenshot({ path: path.join(shots, `search-${engine}-${lang}-${width}.png`) });
          assert.deepEqual(errors, []);
        });
      }
    }

    await t.test("no search API ask: no badge", async () => {
      const { p, errors } = await open("en", { called: false });
      await p.locator(".led tr", { hasText: "claude-sonnet-5" }).first().waitFor();
      assert.equal(await p.locator(".led .src.search").count(), 0);
      assert.deepEqual(errors, []);
    });
  });
}
