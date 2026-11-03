// Run with Node's test runner and Playwright on the module path; see README.md.
// A request whose reply said what it cost in its plan's credits (WorkBuddy's
// usage.credit) says them on its Routing row, as the Usage page's ledger
// does, and its session sums them (jiabubu on Discord: "路由页面的请求列表，
// 消耗积分的请求没有显示积分消耗量"). A request with no known price shows
// its credits alone; one priced shows both; one that said none reads as
// before. In every language, at 1100 and 420. No backend: the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function fixture() {
  const now = Date.now(), at = (s) => new Date(now - s * 1000).toISOString();
  const seat = { id: "workbuddy", provider: "workbuddy", name: "WorkBuddy", who: "jiabubu", kind: "account", model: "glm-5.1" };
  const tier = (credits) => ({ provider: "workbuddy", model: "glm-5.1", in: 9, out: 12, ...(credits != null ? { credits } : {}) });
  const req = (id, session, ago, usage, extra = {}) => ({ id, seq: id, time: at(ago), agent: "claude", session,
    model: "workbuddy/glm-5.1", provider: "workbuddy", order: [seat], tries: [{ id: seat.id, model: "glm-5.1", start: at(ago), done: true, status: 200, ms: 500 }],
    done: true, status: 200, ms: 500, tokens: 21, usage, priced: false, cost: 0, ...extra });
  return [
    req(1, "s-credits", 4, [tier(1.5)]),
    req(2, "s-credits", 3, [tier(0.25)]),
    req(3, "s-priced", 2, [tier(2)], { priced: true, cost: 0.01 }),
    req(4, "s-none", 1, [tier()]),
  ];
}

function serve(lang, routes) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "claude", name: "Claude Code", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.has("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 4, totals: { requests: 4, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/gateway/session-titles") return json({ titles: {} });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// "{n} credits" and the help under the pointer, as i18n.js says them
const words = {
  en: { n: (n) => `${n} credits`, one: /^What it cost in its plan's credits, as the reply said$/, some: /What 2 of these requests cost in their plan's credits/ },
  zh: { n: (n) => `${n} 积分`, one: /^按回复所说，这次请求消耗的套餐积分$/, some: /其中 2 次请求/ },
  "zh-TW": { n: (n) => `${n} 積分`, one: /^依回覆所述，這次請求消耗的方案積分$/, some: /其中 2 次請求/ },
  ja: { n: (n) => `${n} クレジット`, one: /^このリクエストが消費したプランのクレジット/, some: /うち 2 件/ },
  de: { n: (n) => `${n} Credits`, one: /^Was die Anfrage laut Antwort an Credits des Tarifs gekostet hat$/, some: /Was 2 dieser Anfragen/ },
};
const num = (lang, v) => v.toLocaleString(lang);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: routing says the credits a reply said it cost (jiabubu)`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const w = words[lang], errors = [];
        const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, fixture()));
        await page.goto("http://magpie.test/?view=routing");
        const rows = page.locator(".rt-req");
        await rows.nth(3).waitFor();

        // newest first: none said, priced and said, then the two credited alone
        const cost = (i) => rows.nth(i).locator(".cost-metric");
        assert.equal(await cost(0).count(), 0, "a reply that said no credits and has no price shows no cost, as before");
        assert.equal(await cost(1).locator(".v").textContent(), `≈$0.010 · ${w.n(num(lang, 2))}`);
        assert.equal(await cost(2).locator(".v").textContent(), w.n(num(lang, 0.25)), "no known price: the credits alone, never a dash");
        assert.equal(await cost(3).locator(".v").textContent(), w.n(num(lang, 1.5)));
        assert.match(await cost(3).getAttribute("title"), w.one);
        assert.equal((await cost(1).getAttribute("title")).split("\n").length, 2, "a priced row says both what the price is and what the credits are");

        // By session: a session sums its credits, with or without a price
        await page.evaluate(() => localStorage.setItem("magpie.routingBySession", "1"));
        await page.reload();
        const group = (s) => page.locator("button.rt-session").filter({ has: page.locator(`.nm[title$="${s}"]`) });
        await group("s-credits").waitFor();
        const sum = group("s-credits").locator(".cost");
        assert.equal(await sum.isVisible(), true, "a session of credits alone shows them");
        assert.equal(await sum.textContent(), w.n(num(lang, 1.75)));
        assert.match(await sum.getAttribute("title"), w.some);
        assert.equal(await group("s-priced").locator(".cost").textContent(), `≈$0.010 · ${w.n(num(lang, 2))}`);
        assert.equal(await group("s-none").locator(".cost").isVisible(), false, "a session that said none reads as before");

        const fit = await page.evaluate(() => ({ client: document.documentElement.clientWidth, scroll: document.documentElement.scrollWidth }));
        assert(fit.scroll <= fit.client + 1, `the page overflows at ${width}: ${JSON.stringify(fit)}`);
        const reqs = await page.locator(".rt-reqs").evaluate((e) => ({ client: e.clientWidth, scroll: e.scrollWidth }));
        assert(reqs.scroll <= reqs.client + 1, `request list overflows at ${width}: ${JSON.stringify(reqs)}`);
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-routing-credits.png`), fullPage: true });
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
