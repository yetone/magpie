// The real provider editor and Usage page, with isolated API fixtures.
// Provider keys stay manageable; Usage's gateway keys identify the calling client.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const crypto = require("node:crypto");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const fingerprint = (key) => crypto.createHash("sha256").update(key).digest("hex").slice(0, 10);
const PERSONAL = fingerprint("test-personal"), TEAM = fingerprint("test-team");
const totals = (rows) => ({
  calls: rows.length, errors: rows.filter((r) => r.status >= 400).length,
  input: rows.reduce((n, r) => n + r.in, 0), output: rows.reduce((n, r) => n + r.out, 0),
  cache_read: rows.reduce((n, r) => n + (r.cache_read || 0), 0), cache_write: 0, reasoning: 0,
  cost: rows.reduce((n, r) => n + r.cost, 0), unpriced: 0,
});
function fixture(lang, theme, posts, requests) {
  const relay = {
    id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example/v1", responses: "", anthropic: "",
    models: [{ id: "m", name: "Model", on: true }], agents: [], fallback: [], headers: {}, routing: "", catalog: "",
    key: { set: true, masked: "test-…onal" }, balanceToken: { takes: false, set: false },
    keyList: [
      { id: PERSONAL, name: "Personal", masked: "test-…onal", active: true, on: true },
      { id: TEAM, name: "Team", masked: "test-…team", active: false, on: true },
    ],
  };
  const providers = { providers: [relay], presets: [], excluded: [], gateway: { running: true, window: true } };
  const rows = [
    { callerKeyId: "server", callerKeyName: "Server", in: 2000, out: 200, cost: 0.4, cache_read: 1000, status: 200, agent: "claude", agentName: "Claude Code" },
    { callerKeyId: "laptop", callerKeyName: "Laptop", in: 1000, out: 100, cost: 0.2, status: 200, agent: "codex", agentName: "Codex" },
    { callerKeyId: "laptop", callerKeyName: "Laptop", in: 0, out: 0, cost: 0, status: 429, agent: "codex", agentName: "Codex" },
    { in: 50, out: 5, cost: 0.01, status: 200, agent: "codex", agentName: "Codex" },
  ].map((r, i) => ({ t: new Date(Date.now() - i * 60e3).toISOString(), provider: "relay", providerName: "Relay", host: "relay.example",
    model: "m", req: "relay/m", ms: 1000, priced: true, icon: "generic", ...r }));
  const ledger = (q) => {
    let filtered = rows;
    if (q.get("callerKey")) filtered = filtered.filter((r) => r.callerKeyId === q.get("callerKey"));
    if (q.get("agent")) filtered = filtered.filter((r) => r.agent === q.get("agent"));
    if (q.get("failed") === "1") filtered = filtered.filter((r) => r.status >= 400);
    if (q.get("q")) filtered = filtered.filter((r) => (r.callerKeyName || r.model).toLowerCase().includes(q.get("q").toLowerCase()));
    const offset = +q.get("offset") || 0;
    return { ...totals(filtered), total: filtered.length, offset, period: q.get("period"),
      rows: filtered.slice(offset, offset + (+q.get("limit") || 100)),
      callerKeys: callerKeys(), agents: [{ id: "codex", name: "Codex" }, { id: "claude", name: "Claude Code" }] };
  };
  const callerKeys = () => ["server", "laptop"].map((id) => {
    const rs = rows.filter((r) => r.callerKeyId === id);
    return { id, callerKeyId: id, name: rs[0].callerKeyName, icon: "generic", ...totals(rs) };
  });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme, web: false })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/keys/")) {
      const action = url.pathname.split("/").at(-1), body = req.postDataJSON();
      posts.push({ action, body });
      const key = relay.keyList.find((k) => k.id === body.ref);
      if (action === "add") relay.keyList.push({ id: fingerprint(body.key), name: body.name, masked: "test-…new", on: true, active: false });
      if (action === "rename") key.name = body.name;
      if (action === "off" || action === "on") key.on = action === "on";
      if (action === "use") { for (const k of relay.keyList) k.active = k === key; }
      if (action === "remove") relay.keyList = relay.keyList.filter((k) => k !== key);
      return json(providers);
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ ...totals(rows), callerKeys: callerKeys(), agents: [], models: [], series: [], bucket: "day", path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname === "/api/usage/requests") { requests.push(url.searchParams); return json(ledger(url.searchParams)); }
    if (url.pathname === "/api/usage/requests/export") {
      requests.push(Object.assign(url.searchParams, { method: req.method() }));
      return json({ path: "~/Downloads/keys.csv", rows: ledger(url.searchParams).total });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml" }[path.extname(file)] });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: manage keys and inspect their separate usage`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 740 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [], posts = [], requests = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", fixture(lang, "light", posts, requests));
      const w = lang === "zh"
        ? { addButton: "添加", first: "设为首选", remove: "移除", failed: "失败", all: "全部网关密钥" }
        : { addButton: "Add", first: "Make first", remove: "Remove", failed: "Failed", all: "All gateway keys" };
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Relay" }).click();
      assert.equal(await page.locator(".accts .acc:not(.add)").count(), 2);
      await page.locator(".accts .acc.add").click();
      await page.locator(".acc.adding .kf input").nth(0).fill("Backup");
      await page.locator(".acc.adding .kf input").nth(1).fill("test-new");
      await page.locator(".acc.adding").getByRole("button", { name: w.addButton, exact: true }).click();
      await page.locator(".accts .acc", { hasText: "Backup" }).waitFor();
      assert.equal(posts.at(-1).action, "add");
      assert.equal(posts.at(-1).body.key, "test-new");
      const backup = () => page.locator(".accts .acc", { hasText: "Backup" });
      await backup().locator(".dot.tick").click();
      await page.waitForFunction(() => [...document.querySelectorAll(".accts .acc.off")].some((r) => r.textContent.includes("Backup")));
      assert.equal(posts.at(-1).action, "off");
      await backup().locator(".dot.tick").click();
      await page.waitForFunction(() => [...document.querySelectorAll(".accts .acc.in-use")].some((r) => r.textContent.includes("Backup")));
      const team = () => page.locator(".accts .acc", { hasText: "Team" });
      await team().getByRole("button", { name: w.first, exact: true }).click();
      await team().locator(".using").waitFor();
      assert.equal(posts.at(-1).body.ref, TEAM);
      await team().locator(".rename").click();
      await page.locator(".rename-in").fill("Workspace");
      await page.locator(".rename-in").press("Enter");
      await page.locator(".accts .acc .rename", { hasText: "Workspace" }).waitFor();
      assert.equal(posts.at(-1).action, "rename");
      await backup().hover();
      await backup().getByRole("button", { name: w.remove, exact: true }).click();
      const confirmation = page.getByRole("alertdialog");
      assert.match(await confirmation.textContent(), /Backup/);
      await confirmation.getByRole("button", { name: w.remove, exact: true }).click();
      await page.waitForFunction(() => ![...document.querySelectorAll(".accts .acc")].some((r) => r.textContent.includes("Backup")));

      await page.locator("#modal").getByRole("button", { name: lang === "zh" ? "取消" : "Cancel", exact: true }).click();
      await page.locator("#modal").waitFor({ state: "hidden" });
      await page.locator('nav [data-view="usage"]').click();
      await page.locator("#usageKeys .row").first().waitFor();
      assert.deepEqual(await page.locator("#usageKeys .name").allTextContents(), ["Server", "Laptop"]);
      assert.deepEqual(await page.locator("#usageKeys .num b").allTextContents(), ["2.2K", "1.1K"]);
      assert.deepEqual(await page.locator("#usageKeys .cost").allTextContents(), ["≈$0.400", "≈$0.200"]);
      assert(!/Workspace|Personal/.test(await page.locator("#usageKeys").textContent()));
      await page.locator("#usageTab .opt").nth(1).click();
      await page.locator(".led tbody tr").first().waitFor();
      assert.equal(await page.locator(".led tbody tr").first().locator("td").nth(3).textContent(), "Relay · relay.example");
      assert((await page.locator(".led tbody tr").first().locator("td").nth(3).getAttribute("title")).includes("relay.example"));
      await page.locator("#ledKey").click();
      await page.locator(".sess-menu .pm-item", { hasText: "Laptop" }).click();
      await page.waitForFunction(() => document.querySelectorAll(".led tbody tr").length === 2);
      assert.equal(requests.at(-1).get("callerKey"), "laptop");
      assert(!requests.at(-1).has("key"));
      assert.equal(requests.at(-1).get("offset"), "0");
      await page.locator("#ledStatus .opt", { hasText: w.failed }).click();
      await page.waitForFunction(() => document.querySelectorAll(".led tbody tr").length === 1);
      assert.equal(requests.at(-1).get("callerKey"), "laptop");
      assert.equal(requests.at(-1).get("failed"), "1");
      await page.locator("#ledExport").click();
      await page.waitForFunction(() => document.querySelector("#status").textContent.includes("keys.csv"));
      const exported = requests.findLast((q) => q.method === "POST");
      assert.equal(exported.get("callerKey"), "laptop");
      assert(!exported.has("key"));
      assert.equal(exported.get("failed"), "1");
      assert(!exported.has("offset") && !exported.has("limit"));
      await page.locator("#ledStatus .opt").first().click();
      await page.locator("#ledKey").click();
      await page.locator(".sess-menu .pm-item", { hasText: w.all }).click();
      await page.waitForFunction(() => document.querySelectorAll(".led tbody tr").length === 4);
      assert(!requests.at(-1).has("key") && !requests.at(-1).has("callerKey"));
      assert.equal(await page.locator(".led tbody tr").last().locator("td").nth(3).textContent(), "Relay · relay.example");
      await page.setViewportSize({ width: 560, height: 700 });
      assert(await page.locator("#view-usage").evaluate((v) => v.scrollWidth <= v.clientWidth));
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-api-key-usage.png`) });
      }
      assert.deepEqual(errors, []);
    });
  }
}
