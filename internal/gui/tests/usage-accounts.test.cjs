// The Usage page tells subscription accounts apart (#557): Overview lists
// each account's tokens and cost, an older call whose record names none as
// "account not recorded", and Requests has an Account filter that asks for
// the account's calls (and exports them), without moving the page.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const totals = (rows) => ({
  calls: rows.length, errors: rows.filter((r) => r.status >= 400).length,
  input: rows.reduce((n, r) => n + r.in, 0), output: rows.reduce((n, r) => n + r.out, 0),
  cache_read: 0, cache_write: 0, reasoning: 0,
  cost: rows.reduce((n, r) => n + r.cost, 0), unpriced: 0,
});

function fixture(lang, requests) {
  const providers = { providers: [], presets: [], excluded: [], gateway: { running: true, window: true } };
  const who = ["a@example.com", "b@example.com", "", "a@example.com"];
  const rows = Array.from({ length: 40 }, (_, i) => {
    const account = who[i % who.length];
    const claude = i % 8 === 3;
    return {
      t: new Date(Date.now() - i * 60e3).toISOString(), agent: "codex", agentName: "Codex", icon: "generic",
      provider: claude ? "claude" : "codex", providerName: claude ? "Claude" : "Codex",
      host: account ? (claude ? "api.anthropic.com" : "chatgpt.com") + " as " + account : "chatgpt.com",
      providerAccount: account, model: "gpt-5", req: "gpt-5", in: 1000, out: 100, cost: 0.25, ms: 900, status: 200, priced: true,
    };
  });
  const sum = (provider, account) => totals(rows.filter((r) => r.provider === provider && r.providerAccount === account));
  const accounts = [
    { id: "codex@a@example.com", provider: "codex", account: "a@example.com", name: "a@example.com", sub: "Codex", icon: "generic", ...sum("codex", "a@example.com") },
    { id: "codex@b@example.com", provider: "codex", account: "b@example.com", name: "b@example.com", sub: "Codex", icon: "generic", ...sum("codex", "b@example.com") },
    { id: "codex@", provider: "codex", account: "", name: "", sub: "Codex", icon: "generic", ...sum("codex", "") },
    { id: "claude@a@example.com", provider: "claude", account: "a@example.com", name: "a@example.com", sub: "Claude", icon: "generic", ...sum("claude", "a@example.com") },
  ];
  const ledger = (q) => {
    let filtered = rows;
    if (q.get("account")) filtered = filtered.filter((r) => r.providerAccount === q.get("account"));
    const offset = +q.get("offset") || 0;
    return { ...totals(filtered), total: filtered.length, offset, period: q.get("period"),
      rows: filtered.slice(offset, offset + (+q.get("limit") || 100)), callerKeys: [],
      agents: [{ id: "codex", name: "Codex" }], providers: [{ id: "codex", name: "Codex" }, { id: "claude", name: "Claude" }],
      accounts: [{ id: "a@example.com", providers: ["Codex", "Claude"] }, { id: "b@example.com", providers: ["Codex"] }] };
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: false })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ ...totals(rows), accounts, callerKeys: [], agents: [], models: [], series: [], bucket: "day", path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname === "/api/usage/requests") { requests.push(url.searchParams); return json(ledger(url.searchParams)); }
    if (url.pathname === "/api/usage/requests/export") {
      requests.push(Object.assign(url.searchParams, { method: req.method() }));
      return json({ path: "~/Downloads/accounts.csv", rows: ledger(url.searchParams).total });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml" }[path.extname(file)] });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: usage by subscription account`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 600 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [], requests = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", fixture(lang, requests));
      const w = lang === "zh"
        ? { head: "账号", none: "未记录账号", all: "全部账号" }
        : { head: "Accounts", none: "account not recorded", all: "All accounts" };
      await page.goto("http://magpie.test/?view=usage");

      // Overview: one row per provider and account, the unrecorded one named so
      await page.locator("#usageAccounts .row").first().waitFor();
      assert.equal(await page.locator("#usageAccountsHead").isHidden(), false);
      assert.equal((await page.locator("#usageAccountsHead").textContent()).trim(), w.head);
      assert.deepEqual(await page.locator("#usageAccounts .name").allTextContents(), ["a@example.com", "b@example.com", w.none, "a@example.com"]);
      assert.deepEqual(await page.locator("#usageAccounts .sub").evaluateAll((s) => s.map((x) => x.textContent.split(" · ")[0])), ["Codex", "Codex", "Codex", "Claude"]);
      assert.deepEqual(await page.locator("#usageAccounts .cost").allTextContents(), ["≈$3.75", "≈$2.50", "≈$2.50", "≈$1.25"]);
      // no coloured stripe down a row's side
      assert(await page.locator("#usageAccounts .row").evaluateAll((rs) => rs.every((r) => parseFloat(getComputedStyle(r).borderLeftWidth) === 0)));

      // Requests: the Account filter asks for one account's calls
      await page.locator("#usageTab .opt").nth(1).click();
      await page.locator(".led tbody tr").first().waitFor();
      assert.equal(await page.locator("#ledAccount").isHidden(), false);
      assert.equal((await page.locator("#ledAccount").textContent()).trim(), w.all);
      const view = "#view-usage";
      // the reader scrolls a little, the filter still in sight
      const box = await page.locator(view).boundingBox();
      await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
      await page.mouse.wheel(0, 40);
      await page.waitForFunction((sel) => document.querySelector(sel).scrollTop > 0, view);
      await page.waitForTimeout(200);
      const was = await page.locator(view).evaluate((v) => v.scrollTop);
      assert(was > 0, "the view must scroll");
      await page.locator("#ledAccount").click();
      const item = page.locator(".sess-menu .pm-item", { hasText: "b@example.com" });
      assert.equal((await item.locator(".pm-note").textContent()).trim(), "Codex");
      assert.equal((await page.locator(".sess-menu .pm-item", { hasText: "a@example.com" }).locator(".pm-note").textContent()).trim(), "Codex · Claude");
      await item.click();
      await page.waitForFunction(() => document.querySelectorAll(".led tbody tr").length === 10);
      assert.equal(requests.at(-1).get("account"), "b@example.com");
      assert.equal(requests.at(-1).get("offset"), "0");
      assert.equal((await page.locator("#ledAccount").textContent()).trim(), "b@example.com");
      assert.equal(await page.locator(view).evaluate((v) => v.scrollTop), was, "picking an account must not move the page");
      await page.locator("#ledExport").click();
      await page.waitForFunction(() => document.querySelector("#status").textContent.includes("accounts.csv"));
      assert.equal(requests.findLast((q) => q.method === "POST").get("account"), "b@example.com");
      await page.locator("#ledAccount").click();
      await page.locator(".sess-menu .pm-item", { hasText: w.all }).click();
      await page.waitForFunction(() => document.querySelectorAll(".led tbody tr").length === 40);
      assert(!requests.at(-1).has("account"));
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-usage-accounts.png`) });
      }
      assert.deepEqual(errors, []);
    });
  }
}
