// Run with Node's test runner and Playwright on the module path; see README.md.
// #1839 (kensenzhao): the requests magpie makes by itself — a routing
// group's classifier, a web search — go to the same accounts as the agents'
// and count against them, so the Usage page tells them apart: magpie's row
// among the agents and its rows in the ledger carry a "magpie's own" mark,
// an account's, a key's or a model's row says how many of its calls magpie
// made itself, and the account's card says how many of the period's
// requests were magpie's own. The Routing page says, beside a classifier on
// a subscription (or a group whose first model is one), that every turn
// starts with a request to it; a classifier on a key says nothing of it.
// In every language, in Chromium and WebKit at 1100 and 390 wide, no click
// moving the page and nothing wider than the window; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const later = new Date(now + 3 * 3600e3).toISOString();

const tot = (calls, input, output) => ({ calls, errors: 0, input, output, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0, unpriced: 0 });
const SUMMARY = {
  ...tot(5, 2100, 210), bucket: "day", series: [], path: "~/.config/magpie/usage.jsonl",
  agents: [
    { id: "codex", name: "Codex", icon: "codex-color", ...tot(3, 1600, 200) },
    { id: "magpie", name: "magpie", icon: "generic", ...tot(2, 500, 10) },
  ],
  models: [{ id: "codex/gpt-5", provider: "codex", model: "gpt-5", name: "gpt-5", sub: "Codex", icon: "codex-color", ...tot(5, 2100, 210), own: { calls: 2, tokens: 510, cost: 0 } }],
  accounts: [
    { id: "codex@me@example.com", provider: "codex", account: "me@example.com", name: "me@example.com", sub: "Codex", icon: "codex-color", ...tot(4, 2000, 205), own: { calls: 2, tokens: 510, cost: 0 } },
    { id: "codex@you@example.com", provider: "codex", account: "you@example.com", name: "you@example.com", sub: "Codex", icon: "codex-color", ...tot(1, 100, 5) },
  ],
};
const row = (i, extra) => ({
  t: new Date(now - (i + 1) * 60e3).toISOString(), agent: "codex", agentName: "Codex", icon: "codex-color",
  provider: "codex", providerName: "Codex", providerAccount: "me@example.com", req: "gpt-5", model: "gpt-5", served: "gpt-5",
  ms: 900, status: 200, rid: "r-" + i, ep: "/v1/responses", in: 800, out: 100, cost: 0, priced: true, ...extra,
});
const ROWS = [
  row(0),
  row(1, { agent: "magpie", agentName: "magpie", icon: "generic", in: 300, out: 2, kind: "routing" }),
  row(2),
];

function serveUsage(lang) {
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
    if (url.pathname === "/api/usage/requests") return json({ period: url.searchParams.get("period"), rows: ROWS, offset: 0, total: ROWS.length, calls: ROWS.length, errors: 0, input: 1900, output: 202, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0, unpriced: 0, agents: [{ id: "codex", name: "Codex", icon: "codex-color" }, { id: "magpie", name: "magpie", icon: "generic" }] });
    if (url.pathname === "/api/usage/requests/content") return json({ found: false, why: "read" });
    if (url.pathname === "/api/usage/quotas") return json([
      { provider: "codex", name: "Codex", icon: "codex-color", user: "me@example.com", plan: "Plus", windows: [{ name: "5 hours", used: 40, resetsAt: later }] },
      { provider: "codex", name: "Codex", icon: "codex-color", user: "you@example.com", plan: "Plus", windows: [{ name: "5 hours", used: 10, resetsAt: later }] },
    ]);
    if (url.pathname === "/api/usage") {
      // the summary after the allowances, as a long period's is: the cards
      // drawn without it are drawn again once it comes
      await new Promise((r) => setTimeout(r, 800));
      return json(SUMMARY);
    }
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the Routing page: a classifier on a subscription's model, one on a key's,
// and one that is a group whose first model is a subscription's
const models = [
  { id: "codex/gpt-5-mini", name: "gpt-5-mini", providerName: "Codex", icon: "codex-color", context: 400000, ready: true, sub: true },
  { id: "or/flash", name: "flash", providerName: "OpenRouter", icon: "generic", context: 128000, ready: true },
  { id: "a/opus", name: "opus", providerName: "A", icon: "generic", context: 1000000, ready: true },
];
const member = (id, context) => ({ id, ready: true, context });
const group = (id, classifier, extra) => ({ id, name: id, members: ["a/opus", "or/flash"], routing: "order", ready: true, classifier,
  memberInfo: [member("a/opus", 1000000), member("or/flash", 128000)], rules: [{ use: "or/flash", intent: "a quick question" }], ...extra });
const GROUPS = {
  models,
  groups: [
    group("onsub", "codex/gpt-5-mini"),
    group("onkey", "or/flash"),
    { ...group("bysubgroup", "group/cls"), effort: "auto" },
    { id: "cls", name: "cls", members: ["codex/gpt-5-mini", "or/flash"], routing: "order", ready: true, memberInfo: [member("codex/gpt-5-mini", 400000), member("or/flash", 128000)] },
  ],
  pools: [],
};

function serveRouting(lang) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(GROUPS);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const ONE = "Every turn starts with a request to {name}, a subscription: it counts against its allowance as the agent's own do. A free model or one on an API key leaves the subscription to the agents.";
const TWO = "Every turn starts with two requests to {name}, a subscription: they count against its allowance as the agent's own do. A free model or one on an API key leaves the subscription to the agents.";

// the page's word for a string, and that it is the language's own
async function say(page, lang, key, vars) {
  const s = await page.evaluate(([k, v]) => t(k, v), [key, vars || {}]);
  if (lang !== "en") assert.notEqual(s, key.replace(/\{(\w+)\}/g, (_, k) => vars[k]), `"${key}" is translated`);
  return s;
}

// a left border no other side has: a coloured stripe, never wanted
const leftStripe = (loc) => loc.evaluate((e) => {
  const s = getComputedStyle(e);
  return s.borderLeftWidth !== s.borderRightWidth || s.borderLeftColor !== s.borderRightColor;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 390]) {
    test(`${engine} ${width}: magpie's own requests are told apart on the Usage page`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const lang of LANGS) {
        await t.test(lang, async () => {
          const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
          const page = await context.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serveUsage(lang));
          try {
            await page.goto("http://magpie.test/?view=usage");
            const n = (v) => page.evaluate((x) => fmtN(x), v);
            const own = await say(page, lang, "magpie's own");
            // magpie's row among the agents is marked, the agent's not
            const agents = page.locator("#usageAgents .row");
            await agents.nth(1).waitFor({ timeout: 15000 });
            const mine = page.locator('#usageAgents .row[data-id="magpie"] .own-badge');
            assert.equal(await mine.textContent(), own);
            assert.equal(await page.locator('#usageAgents .row[data-id="codex"] .own-badge').count(), 0);
            assert.equal(await leftStripe(mine), false, "no left-border accent");
            // an account's row says how many of its calls magpie made itself
            const by = await say(page, lang, "{n} by magpie itself", { n: await n(2) });
            const acct = page.locator('#usageAccounts .row[data-id="codex@me@example.com"] .sub');
            assert((await acct.textContent()).includes(by), await acct.textContent());
            assert(!(await page.locator('#usageAccounts .row[data-id="codex@you@example.com"] .sub').textContent()).includes(by), "the other account had none");
            assert((await page.locator('#usageModels .row .sub').first().textContent()).includes(by), "and the model's row");
            // the account's card says how many of the period's were magpie's own
            const line = page.locator("#subscriptionUsage .quota-own");
            await line.waitFor();
            assert.equal(await line.count(), 1, "only the account with any");
            const said = await say(page, lang, "{n} requests in this period were magpie's own", { n: await n(2) });
            assert.equal(await line.locator(":scope > span:not(.own-badge)").textContent(), said);
            assert.equal(await line.locator(".own-badge").textContent(), own);
            const lb = await line.boundingBox(), cb = await page.locator("#subscriptionUsage .subscription-card").first().boundingBox();
            assert(lb.x >= cb.x - 0.5 && lb.x + lb.width <= cb.x + cb.width + 0.5, `the line inside its card: ${JSON.stringify(lb)} in ${JSON.stringify(cb)}`);
            assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "the overview scrolls sideways");

            // the ledger: magpie's request is marked, the agent's not
            await page.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
            const rows = page.locator(".led tbody tr.led-row");
            await rows.nth(ROWS.length - 1).waitFor({ timeout: 15000 });
            await reader.inView(page, rows.nth(1));
            assert.equal(await rows.nth(1).locator(".who .own-badge").textContent(), own);
            assert.equal(await rows.nth(0).locator(".own-badge").count(), 0);
            assert.equal(await rows.nth(2).locator(".own-badge").count(), 0);
            const b = await rows.nth(1).locator(".who .own-badge").boundingBox();
            assert(b && b.width > 0 && b.x + b.width <= width, `the mark shows: ${JSON.stringify(b)}`);
            // clear of the sticky sideways scroll bar at the view's foot
            const v = await page.locator("#view-usage").boundingBox(), rb = await rows.nth(1).boundingBox();
            if (rb.y + rb.height > v.y + v.height - 48) {
              await page.mouse.wheel(0, 120);
              await page.waitForTimeout(300);
            }
            const top = await page.locator("#view-usage").evaluate((v) => v.scrollTop), at = await page.evaluate(() => [scrollX, scrollY]);
            await rows.nth(1).click();
            await page.waitForTimeout(150);
            assert.equal(await page.locator("#view-usage").evaluate((v) => v.scrollTop), top, "a click moved the page");
            assert.deepEqual(await page.evaluate(() => [scrollX, scrollY]), at);
            assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "the ledger scrolls the page sideways");
            assert.deepEqual(errors, []);
          } finally {
            if (process.env.ARTIFACT_DIR) {
              await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
              await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${width}-${lang}-own-usage.png`) });
            }
            await context.close();
          }
        });
      }
    });

    test(`${engine} ${width}: a classifier on a subscription says every turn starts with a request to it`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const lang of LANGS) {
        await t.test(lang, async () => {
          const height = 1400;
          const context = await browser.newContext({ viewport: { width, height }, reducedMotion: "reduce" });
          const page = await context.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serveRouting(lang));
          try {
            const open = async (i) => {
              await page.goto("http://magpie.test/?view=groups");
              const g = page.locator(".rt-group").nth(i);
              await g.waitFor();
              const edit = g.locator("button", { hasText: await page.evaluate(() => t("Edit")) });
              await page.mouse.move(Math.min(550, (width / 2) | 0), 300);
              for (let k = 0; k < 12 && (await edit.boundingBox()).y > height - 160; k++) {
                await page.mouse.wheel(0, 300);
                await page.waitForTimeout(150);
              }
              const bb = await edit.boundingBox();
              await page.mouse.click(bb.x + bb.width / 2, bb.y + bb.height / 2);
              const ed = page.locator(".rt-gedit");
              await ed.locator(".rt-classifier").waitFor();
              return ed;
            };
            const name = "gpt-5-mini · Codex";
            // a subscription's model: one request
            let ed = await open(0);
            let hint = ed.locator(".rt-cls-sub");
            assert.equal(await hint.textContent(), await say(page, lang, ONE, { name }));
            const hb = await hint.boundingBox(), eb = await ed.boundingBox();
            assert(hb.width > 0 && hb.x >= eb.x - 0.5 && hb.x + hb.width <= eb.x + eb.width + 0.5, `inside the editor: ${JSON.stringify(hb)} in ${JSON.stringify(eb)}`);
            assert.equal(await leftStripe(hint), false, "no left-border accent");
            assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "the page scrolls sideways");
            // a key's model: nothing of it
            ed = await open(1);
            assert.equal(await ed.locator(".rt-cls-sub").count(), 0, "a classifier on a key says nothing of a subscription");
            // a group asking a subscription first, picking the effort too: two
            ed = await open(2);
            assert.equal(await ed.locator(".rt-cls-sub").textContent(), await say(page, lang, TWO, { name }));
            assert.deepEqual(errors, []);
          } finally {
            if (process.env.ARTIFACT_DIR) {
              await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
              await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${width}-${lang}-own-routing.png`) });
            }
            await context.close();
          }
        });
      }
    });
  }
}
