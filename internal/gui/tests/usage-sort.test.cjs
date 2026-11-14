// Run with Node's test runner and Playwright on the module path; see README.md.
// onducoder on X (Usage › Overview): 能为 agent、模型、网关密钥、账号的消耗量
//增加一个排序 或者 排行榜. Each of the Overview's lists — Agents, Models,
// Accounts, Gateway keys — is a ranking: most tokens first by default, and a
// quiet pick at the right of its heading (the app's own menu, no <select>)
// orders it by cost, by calls or by name instead, each list on its own, kept
// across a reload. Tokens count as the tokens tile does: with the cache
// picked, a row with more cache comes first. An unpriced row goes last by
// cost; an account without a name goes last by name. A folded list hides its
// pick. Every language, 1100 and 420 wide, nothing spilling; no backend, the
// API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets"));

const G = (id, name, input, output, calls, cost, extra) => ({ id, name, icon: "generic", calls, input, output, cache_read: 0, cache_write: 0, cost, ...extra });
// sent in the server's order, most tokens first; the cost, calls and names
// each order them differently
const AGENTS = [
  G("codex", "Codex", 900000, 100000, 20, 1.5),
  G("pi", "Pi", 400000, 50000, 90, 7.25),
  G("amp", "Amp", 100000, 10000, 5, 0, { unpriced: 5, cache_read: 5000000 }),
];
const MODELS = [
  G("gpt-6", "gpt-6", 800000, 90000, 30, 2, { model: "gpt-6" }),
  G("opus-5", "opus-5", 300000, 40000, 10, 9, { model: "opus-5" }),
  G("glm-5", "glm-5", 200000, 20000, 75, 0.4, { model: "glm-5" }),
];
const ACCOUNTS = [
  G("acct-z", "zed@example.com", 700000, 80000, 25, 1),
  G("acct-none", "", 500000, 60000, 60, 0.5),
  G("acct-a", "Ann@example.com", 200000, 20000, 30, 4),
];
const KEYS = [G("key-2", "server", 900000, 100000, 10, 0.2), G("key-1", "laptop", 500000, 60000, 105, 8)];
const sum = (k) => AGENTS.reduce((n, g) => n + g[k], 0);
const USAGE = {
  calls: 115, errors: 0, input: sum("input"), output: sum("output"), reasoning: 0, cache_read: sum("cache_read"), cache_write: 0, unpriced: 5, cost: 8.75, bucket: "day",
  series: [{ label: "Nov 14", time: new Date().toISOString(), calls: 115, input: sum("input"), output: sum("output"), cache_read: sum("cache_read"), cache_write: 0, cost: 8.75 }],
  path: "~/.config/magpie/usage.jsonl",
  agents: AGENTS, models: MODELS, accounts: ACCOUNTS, callerKeys: KEYS,
};

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    const settings = { lang, theme: "light" };
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings, fx: { rate: 7.2, at: new Date().toISOString() } });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json(USAGE);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: false } });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { tokens: "Tokens", cost: "Cost", calls: "Calls", name: "Name", head: "Order by", withCache: "With cache" },
  zh: { tokens: "Token", cost: "费用", calls: "调用次数", name: "名称", head: "排序方式", withCache: "含缓存" },
  "zh-TW": { tokens: "Token", cost: "費用", calls: "呼叫次數", name: "名稱", head: "排序方式", withCache: "含快取" },
  ja: { tokens: "トークン", cost: "費用", calls: "呼び出し数", name: "名前", head: "並べ替え", withCache: "キャッシュ込み" },
  de: { tokens: "Token", cost: "Kosten", calls: "Aufrufe", name: "Name", head: "Sortieren nach", withCache: "Mit Cache" },
};

const goUsage = async (p) => {
  const nav = p.locator('[data-view="usage"]:visible').first();
  if (await nav.count()) await nav.click();
  else await p.evaluate(() => show("usage"));
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Overview's lists rank by tokens, and by cost, calls or name on request", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) {
      const w = L[lang];
      for (const width of [1100, 420]) {
        await t.test(`${lang} ${width}: ranked lists`, async () => {
          const errors = [];
          const ctx = await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" });
          t.after(() => ctx.close());
          const open = async () => {
            const p = await ctx.newPage();
            p.setDefaultTimeout(5000);
            p.setDefaultNavigationTimeout(60000);
            p.on("pageerror", (e) => errors.push(e.message));
            await p.route("**/*", server(lang));
            await p.goto("http://magpie.test/");
            await goUsage(p);
            await p.locator("#usageKeys .row.stat").first().waitFor({ state: "attached" });
            return p;
          };
          let p = await open();
          const ids = (list) => p.locator(`#${list} > .row.stat`).evaluateAll((rs) => rs.map((r) => r.dataset.id));
          const pick = (id) => p.locator(`#usagePane .row-head .us-sort[data-sort="${id}"]`);
          const choose = async (id, word) => {
            await reader.inView(p, pick(id));
            await pick(id).click();
            const menu = p.locator(".proto-menu");
            await menu.waitFor();
            assert.equal(await menu.locator(".pm-head").textContent(), w.head);
            assert.deepEqual(await menu.locator(".pm-name").allTextContents(), [w.tokens, w.cost, w.calls, w.name]);
            await menu.locator(".pm-item", { has: p.locator(".pm-name", { hasText: new RegExp("^" + word + "$") }) }).click();
            await menu.waitFor({ state: "detached" });
          };

          // most tokens first, every list, each pick saying so
          assert.deepEqual(await ids("usageAgents"), ["codex", "pi", "amp"]);
          assert.deepEqual(await ids("usageModels"), ["gpt-6", "opus-5", "glm-5"]);
          assert.deepEqual(await ids("usageAccounts"), ["acct-z", "acct-none", "acct-a"]);
          assert.deepEqual(await ids("usageKeys"), ["key-2", "key-1"]);
          for (const id of ["agents", "models", "accounts", "keys"]) {
            assert.equal(await pick(id).locator("span").textContent(), w.tokens, id + "'s pick");
            assert.equal(await pick(id).evaluate((e) => e.tagName), "BUTTON");
          }
          // the pick sits in its heading, at its right, nothing spilling
          const bad = await p.evaluate(() => {
            const out = [];
            if (document.documentElement.scrollWidth > innerWidth) out.push("page scrolls sideways");
            for (const b of document.querySelectorAll("#usagePane .row-head .us-sort")) {
              const h = b.parentElement.getBoundingClientRect(), r = b.getBoundingClientRect();
              if (r.right > h.right + 0.5 || r.left < h.left) out.push(b.dataset.sort + " past its heading");
              if (h.right - r.right > 8) out.push(b.dataset.sort + " not at the right");
              if (b.scrollWidth > b.clientWidth + 0.5) out.push(b.dataset.sort + " cut");
            }
            return out;
          });
          assert.deepEqual(bad, []);

          // by cost: the unpriced agent last
          await choose("agents", w.cost);
          assert.deepEqual(await ids("usageAgents"), ["pi", "codex", "amp"]);
          assert.equal(await pick("agents").locator("span").textContent(), w.cost);
          // the other lists keep theirs
          assert.deepEqual(await ids("usageModels"), ["gpt-6", "opus-5", "glm-5"]);
          await choose("models", w.calls);
          assert.deepEqual(await ids("usageModels"), ["glm-5", "gpt-6", "opus-5"]);
          // by name, the account without one last, case aside
          await choose("accounts", w.name);
          assert.deepEqual(await ids("usageAccounts"), ["acct-a", "acct-z", "acct-none"]);
          await choose("keys", w.cost);
          assert.deepEqual(await ids("usageKeys"), ["key-1", "key-2"]);

          // a reload keeps each list's order
          await p.close();
          p = await open();
          assert.deepEqual(await ids("usageAgents"), ["pi", "codex", "amp"]);
          assert.deepEqual(await ids("usageModels"), ["glm-5", "gpt-6", "opus-5"]);
          assert.deepEqual(await ids("usageAccounts"), ["acct-a", "acct-z", "acct-none"]);
          assert.deepEqual(await ids("usageKeys"), ["key-1", "key-2"]);

          // back to tokens; with the cache counted, Amp's 5M cache reads rank it first
          await choose("agents", w.tokens);
          assert.deepEqual(await ids("usageAgents"), ["codex", "pi", "amp"]);
          await p.locator(".kpi .kpi-pick").click();
          await p.locator(".proto-menu .pm-item", { has: p.locator(".pm-name", { hasText: new RegExp("^" + w.withCache + "$") }) }).click();
          assert.deepEqual(await ids("usageAgents"), ["amp", "codex", "pi"]);

          // a folded list hides its pick
          const fold = p.locator('#usagePane .row-head .fold[data-fold="models"]');
          await reader.inView(p, fold);
          await fold.click();
          assert.equal(await pick("models").isVisible(), false);
          await fold.click();
          assert.equal(await pick("models").isVisible(), true);

          await p.evaluate(() => { localStorage.removeItem("magpie.usageSort"); localStorage.removeItem("magpie.usageWithCache"); localStorage.removeItem("magpie.usageFolded"); });
          await p.close();
          assert.deepEqual(errors, []);
        });
      }
    }
  });
}
