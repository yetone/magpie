// Run with Node's test runner and Playwright on the module path; see README.md.
// What WorkBuddy requests cost in the plan's credits (#2512, wwcraft_ on
// Discord: the Usage page should say the credits spent by model, agent and
// session). Each reply says it as usage.credit, the figure WorkBuddy's own
// app shows on a reply as "Credits Consumed"; magpie keeps it on the row and
// the Usage page's ranking says the sum on its line of numbers, the
// Sessions page on the line of where magpie routed the session. A group no
// reply said credits for says none, not 0. Every language, Chromium and
// WebKit; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const H = 3600e3;
const hour = (i) => new Date(Math.floor(now / H) * H - i * H).toISOString();
const row = (i, model, provider) => ({
  route_id: 900 + i, t: new Date(now - (i + 1) * 60e3).toISOString(), agent: "codex", agentName: "Codex", icon: "codex-color",
  provider, providerName: provider, req: model, model, served: model, in: 9, out: 12, cache_read: 0, cache_write: 0,
  ms: 900, status: 200, rid: "req_" + i, ep: "/v1/chat/completions", cost: 0, priced: false,
});
// 70 gemini-3.5-flash replies at 0.01 credits, one free deepseek reply that
// said 0, and a model of another provider that said nothing
const GEM = { id: "gemini-3.5-flash", calls: 70, input: 630, output: 840, cache_read: 0, cache_write: 0, cost: 0, credits: 0.7000000000000005, credited: 70 };
const FREE = { id: "deepseek-v4.1-flash", calls: 1, input: 9, output: 1, cache_read: 0, cache_write: 0, cost: 0, credits: 0, credited: 1 };
const OTHER = { id: "glm-5.2", calls: 3, input: 9, output: 1, cache_read: 0, cache_write: 0, cost: 0 };

function ledger(period) {
  const all = [GEM, FREE, OTHER];
  const s = (k) => all.reduce((a, x) => a + (x[k] || 0), 0);
  return {
    // a WorkBuddy reply that said 0.01 credits, one of another provider that
    // said none, and a priced one whose reply said credits too
    period, rows: [{ ...row(0, "gemini-3.5-flash", "workbuddy-ai"), credits: 0.01 }, row(1, "glm-5.2", "zai"), { ...row(2, "kimi-k2.6", "workbuddy-ai"), cost: 0.002, priced: true, credits: 2.5 }], offset: 0, total: 3,
    calls: s("calls"), errors: 0, input: s("input"), output: s("output"), cache_read: 0, cache_write: 0, cost: 0, unpriced: 74, credits: 0.7, credited: 71,
    by: { model: all }, bucket: "hour",
    series: [{ label: "", time: hour(0), calls: s("calls"), input: s("input"), output: s("output"), by: { model: Object.fromEntries(all.map((x) => [x.id, { calls: x.calls, tokens: x.input + x.output, cost: 0 }])) } }],
    agents: [{ id: "codex", name: "Codex", icon: "codex-color" }],
  };
}

const at = (min) => new Date(now - min * 60e3).toISOString();
const SESSION = {
  agent: "codex", id: "c-1", cwd: "/work/tool", title: "port the parser", start: at(30), last: at(5), name: "Codex", icon: "openai",
  models: [{ model: "gemini-3.5-flash", input: 630, output: 840, cache_read: 0, cache_write: 0, cost: 0, priced: false }],
  input: 630, output: 840, cache_read: 0, cache_write: 0, cost: 0, unpriced: 70,
  resume: "cd /work/tool && codex resume c-1", path: "~/.codex/x/c-1.jsonl",
  via: [{ provider: "workbuddy-ai", model: "gemini-3.5-flash", calls: 70, tokens: 1470, credits: 0.7000000000000005, credited: 70 }, { provider: "zai", model: "glm-5.2", calls: 3, tokens: 10 }],
};

// the Overview's summary: two WorkBuddy accounts whose replies said
// credits, and one Codex account's that said none (lcyksp on Discord: the
// 积分 each account spent, not only its tokens)
const acct = (id, provider, sub, calls, credits, credited) => ({ id: provider + "@" + id, provider, account: id, name: id, sub, icon: "generic", calls, errors: 0, input: 900 * calls, output: 100 * calls, cache_read: 0, cache_write: 0, cost: 0, ...(credited ? { credits, credited } : {}) });
const ACCOUNTS = [acct("a@x.com", "workbuddy", "WorkBuddy", 70, 35, 70), acct("b@x.com", "workbuddy", "WorkBuddy", 2, 2.25, 1), acct("c@x.com", "codex", "Codex", 5)];
const OVERVIEW = { calls: 77, errors: 0, input: 69300, output: 7700, reasoning: 0, unpriced: 77, cost: 0, cache_read: 0, cache_write: 0, credits: 37.25, credited: 71, bucket: "day", series: [], agents: [], models: [], accounts: ACCOUNTS, callerKeys: [], path: "~/.config/magpie/usage.jsonl" };
const EMPTY = { calls: 0, errors: 0, input: 0, output: 0, reasoning: 0, unpriced: 0, cost: 0, bucket: "day", series: [], agents: [], models: [], path: "" };

function server(lang, usage = EMPTY) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/usage/requests") return json(ledger(url.searchParams.get("period")));
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json(usage);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: false } });
    if (url.pathname === "/api/sessions") return json({ sessions: [SESSION], terminal: true, dirs: ["~/.codex/sessions"] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname === "/api/sessions/manage") {
      return json({ agents: [{ agent: "codex", count: 1, deletable: true, name: "Codex", icon: "openai" }], agent: "codex", sessions: [{ ...SESSION, size: 4096, messages: 6, files: 1, deletable: true }], terminal: true, trash: [], trashDir: "~/magpie/trash/sessions" });
    }
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

// each language's "0.7 credits" and "0 credits"
const L = {
  en: ["0.7 credits", "0 credits", /credits/],
  zh: ["0.7 积分", "0 积分", /积分/],
  "zh-TW": ["0.7 積分", "0 積分", /積分/],
  ja: ["0.7 クレジット", "0 クレジット", /クレジット/],
  de: ["0,7 Credits", "0 Credits", /Credits/],
};

// each language's 0.01 and 2.5 credits, and the detail's Credits
const R = {
  en: ["0.01 credits", "2.5 credits", "Credits"],
  zh: ["0.01 积分", "2.5 积分", "积分"],
  "zh-TW": ["0.01 積分", "2.5 積分", "積分"],
  ja: ["0.01 クレジット", "2.5 クレジット", "クレジット"],
  de: ["0,01 Credits", "2,5 Credits", "Credits"],
};

// each language's 35, 2.25 and 37.25 credits
const A = {
  en: ["35 credits", "2.25 credits", "37.25 credits"],
  zh: ["35 积分", "2.25 积分", "37.25 积分"],
  "zh-TW": ["35 積分", "2.25 積分", "37.25 積分"],
  ja: ["35 クレジット", "2.25 クレジット", "37.25 クレジット"],
  de: ["35 Credits", "2,25 Credits", "37,25 Credits"],
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Overview says each account's credits and the period's", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const [lang, [a, b, all]] of Object.entries(A)) {
      await t.test(lang, async () => {
        const errors = [];
        const ctx = await browser.newContext({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" });
        const p = await ctx.newPage();
        p.setDefaultTimeout(5000);
        p.setDefaultNavigationTimeout(60000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", server(lang, OVERVIEW));
        await p.goto("http://magpie.test/?view=usage");
        await p.locator("#usageAccounts .row").first().waitFor();
        const subs = await p.locator("#usageAccounts .sub").allTextContents();
        assert.equal(subs.length, 3, subs.join(" | "));
        assert(subs[0].includes(a), "a@x.com's line: " + subs[0]);
        assert(subs[1].includes(b), "b@x.com's line: " + subs[1]);
        assert(!/credit|积分|積分|クレジット/i.test(subs[2]), "an account no reply said credits for: " + subs[2]);
        const kpi = (await p.locator("#stats .kpi").nth(3).textContent()).trim();
        assert(kpi.includes(all), "the calls tile: " + kpi);
        assert.equal(await p.locator("#stats .kpi").count(), 4, "no tile is added");
        // a narrow window keeps each line inside its row
        await p.setViewportSize({ width: 420, height: 800 });
        await p.waitForTimeout(150);
        const fits = await p.locator("#usageAccounts .row").evaluateAll((rs) => rs.every((r) => r.scrollWidth <= r.clientWidth + 1));
        assert(fits, "the accounts' rows fit a 420px window");
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-account-credits.png`), fullPage: true });
        }
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the credits WorkBuddy's replies said, by model and by session", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const [lang, [gem, free, word]] of Object.entries(L)) {
      await t.test(lang, async () => {
        const errors = [];
        const ctx = await browser.newContext({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" });
        const p = await ctx.newPage();
        p.setDefaultTimeout(5000);
        p.setDefaultNavigationTimeout(60000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", server(lang));
        await p.goto("http://magpie.test/");
        await p.locator('[data-view="usage"]').first().click();
        await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
        await p.locator("#ledWrap .led tbody tr").first().waitFor();
        await p.waitForTimeout(300);

        // the ranking: each model's sum on its line, none where none was said
        const line = async (id) => (await p.locator("#ledRank .rk").filter({ hasText: id }).locator(".rk-b").textContent()).trim();
        const g = await line("gemini-3.5-flash");
        assert(g.includes(gem), `gemini-3.5-flash's line: ${g}`);
        assert(!/0{5}/.test(g), `no float noise: ${g}`);
        const f = await line("deepseek-v4.1-flash");
        assert(f.includes(free), `the free model's line: ${f}`);
        const o = await line("glm-5.2");
        assert(!word.test(o), `a model no reply said credits for: ${o}`);
        const tip = await p.locator("#ledRank .rk").filter({ hasText: "gemini-3.5-flash" }).locator(".rk-credits").getAttribute("title");
        assert(tip && tip.includes("70"), "its title says how many requests: " + tip);

        // a narrow window keeps the line inside the ranking
        await p.setViewportSize({ width: 420, height: 800 });
        await p.waitForTimeout(150);
        const fits = await p.locator("#ledRank .rk").filter({ hasText: "gemini-3.5-flash" }).evaluate((b) => b.scrollWidth <= b.clientWidth + 1);
        assert(fits, "the ranking's line fits a 420px window");
        await p.setViewportSize({ width: 1100, height: 800 });

        // the Sessions page: the routed line of the session says them
        await p.goto("http://magpie.test/?view=sessions");
        const view = p.locator("#view-sessions");
        const r = view.locator('.row.sm-sess[data-id="c-1"]');
        await r.waitFor();
        await r.click();
        const det = view.locator(".sm-item.open .sess-detail");
        await det.waitFor();
        const text = await det.innerText();
        assert(text.includes("workbuddy-ai/gemini-3.5-flash") && text.includes(gem), "the session's details: " + text);
        const glm = text.split("\n").find((l) => l.includes("zai/glm-5.2")) || "";
        assert(glm && !word.test(glm), "the route no reply said credits for: " + glm);
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-plan-credits.png`) });
        }
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}

// Each request's own row says what its reply said it cost in credits
// (jiabubu on Discord: each request's line said its credits, and now
// none does): in the cost column in place of a plan model's dash, or
// under a price, and in the row's details; a row whose reply said none
// keeps its dash.
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": each request's row says the credits its reply said", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const [lang, [one, priced, dt]] of Object.entries(R)) {
      await t.test(lang, async () => {
        const errors = [];
        const ctx = await browser.newContext({ viewport: { width: 1400, height: 800 }, reducedMotion: "reduce" });
        const p = await ctx.newPage();
        p.setDefaultTimeout(5000);
        p.setDefaultNavigationTimeout(60000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", server(lang));
        await p.goto("http://magpie.test/");
        await p.locator('[data-view="usage"]').first().click();
        await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
        const rows = p.locator("#ledWrap .led tbody tr:not(.led-detail)");
        await rows.nth(2).waitFor();
        const cost = (i) => rows.nth(i).locator("td.cost");
        const a = (await cost(0).innerText()).trim();
        assert.equal(a, one, "the WorkBuddy row's cost cell");
        assert(!(await cost(0).getAttribute("class")).includes("faint"), "said, not faint");
        const tip = await cost(0).getAttribute("title");
        assert(tip && tip.length > 0, "its title says what it is");
        assert.equal((await cost(1).innerText()).trim(), "—", "a reply that said none keeps its dash");
        const c = await cost(2).innerText();
        assert(c.includes("$") && c.includes(priced), "a priced row says both: " + c);
        // its details say it, at a narrow width too, where the row is cut
        await rows.nth(0).click();
        const det = p.locator("#ledReqPane .led-detail");
        await det.waitFor();
        const text = await det.innerText();
        assert(text.includes(dt) && text.includes(one), "the details: " + text);
        await p.setViewportSize({ width: 420, height: 800 });
        await p.waitForTimeout(200);
        const fits = await det.locator(".led-dl").evaluate((d) => d.scrollWidth <= d.clientWidth + 1);
        assert(fits, "the details fit a 420px window");
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-row-credits.png`) });
        }
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
