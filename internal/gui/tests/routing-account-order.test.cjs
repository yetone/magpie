// Run with Node's test runner and Playwright on the module path; see README.md.
// The live routing stage seats a provider's accounts and keys in the order
// its page lists them and a drag sets — the order the gateway tries them in
// — not by name, and a drag moves them there without a reload (Koohoko,
// #217: after dragging, the live routing animation kept the old order).
// One request through a group of a subscription, a provider's keys and a
// plugin's accounts, weighed least used first (another order again); each
// provider's rows read in its list's order, and after the subscription's
// accounts are dragged (Alt+↓ in its editor) and the plugin's rearranged,
// the stage, shown again, reads in the new orders.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function fixture(lang) {
  const base = { icon: "generic", chat: "https://example.invalid/v1", models: [{ id: "m", on: true }], agents: [], fallback: [], headers: {}, key: { set: true, masked: "fixture" }, routing: "usage" };
  const login = (agent, user, active) => ({ agent, user, plan: "Pro", active, on: true });
  const providers = { providers: [
    // listed not by name: the one signed in to, then as dragged
    { ...base, id: "antigravity", name: "Antigravity", keyList: [], account: { agent: "antigravity", agentName: "Antigravity", user: "ag-a@x.test",
      logins: [login("antigravity", "ag-a@x.test", true), login("antigravity", "ag-c@x.test", false), login("antigravity", "ag-b@x.test", false)] } },
    { ...base, id: "relay", name: "Relay", keyList: [3, 1, 2].map((n, i) => ({ id: `key-${n}`, name: `Key ${n}`, masked: `k${n}`, active: i === 0, on: true })) },
    // a plugin's provider: its accounts are listed by the provider's id
    { ...base, id: "fakeco", name: "Fakeco", plugin: "fakeco", keyList: [], account: { agent: "fakeco", agentName: "Fakeco", user: "p-z@x.test",
      logins: [login("fakeco", "p-z@x.test", true), login("fakeco", "p-x@x.test", false), login("fakeco", "p-y@x.test", false)] } },
  ], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } };
  const items = (p) => p.account ? p.account.logins : p.keyList;
  const ref = (p, x) => p.account ? x.user : x.id;
  const arrange = (p, order) => {
    const next = order.map((r) => items(p).find((x) => ref(p, x) === r));
    next.forEach((x, i) => { x.active = i === 0; });
    if (p.account) { p.account.logins = next; p.account.user = next[0].user; } else p.keyList = next;
  };
  // a seat as the gateway's trace tells it, rank its place in the list
  const seat = (p, x, rank, used) => p.account
    ? { id: x.active ? p.id : `${p.id}@${x.user}`, provider: p.id, name: p.name, who: x.user, kind: "account", agent: p.account.agent, model: "m", routing: "usage", known: true, used, rank, shared: true }
    : { id: `${p.id}#${x.id}`, provider: p.id, name: p.name, who: x.name, kind: "key", model: "m", routing: "usage", tokens: used * 1000, rank, shared: true };
  const t0 = Date.now() - 2000;
  const order = [];
  for (const p of providers.providers) items(p).forEach((x, i) => order.push(seat(p, x, i, [0.5, 0.1, 0.3][i])));
  order.sort((a, b) => a.used - b.used || (a.tokens || 0) - (b.tokens || 0));
  const members = providers.providers.map((p) => `${p.id}/m`);
  const route = { id: 100, seq: 1, time: new Date(t0).toISOString(), agent: "claude", model: "group/mixed", provider: "antigravity",
    group: { id: "mixed", name: "Mixed", routing: "usage", affinity: "", members }, order,
    tries: [{ id: order[0].id, model: "m", start: new Date(t0 + 10).toISOString(), done: true, status: 200, ms: 300 }], done: true, status: 200, ms: 320, tokens: 100 };
  const state = { agents: [{ id: "claude", name: "Claude", icon: "generic", path: "/fixture", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  const f = { providers, wait: [] };
  f.route = async (r) => {
    const req = r.request(), url = new URL(req.url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs={lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window={};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/gateway/trace") {
      const out = (routes) => json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 1, rerouted: 0, errors: 0 }, routes });
      if (!url.searchParams.get("wait")) return out([route]);
      return new Promise((res) => f.wait.push(() => res(out([])))); // no more requests
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (req.method() === "POST" && url.pathname === "/api/provider/arrange") {
      const body = req.postDataJSON();
      arrange(providers.providers.find((p) => p.id === body.id), body.accountOrder);
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return r.fulfill({ body: await fs.readFile(file), contentType });
  };
  return f;
}

// the stage's rows, as each provider's seats read top to bottom
const seated = (page) => page.locator(".rt-accts > li[title]").evaluateAll((ls) => {
  const by = {};
  for (const l of ls) { const p = l.title.split(/[@#]/)[0]; (by[p] ||= []).push(l.querySelector(".who").textContent); }
  return by;
});
const listed = (f) => Object.fromEntries(f.providers.providers.map((p) => [p.id, p.account ? p.account.logins.map((l) => l.user) : p.keyList.map((k) => k.name)]));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: the live routing stage seats accounts in their dragged order`, { timeout: 60000 }, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 900 }, reducedMotion: "reduce" })).newPage();
      const f = fixture(lang), errors = [];
      t.after(async () => { for (const w of f.wait) w(); await browser.close(); });
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", f.route);
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-accts > li[title]").nth(8).waitFor();
      assert.deepEqual(await seated(page), listed(f), "each provider's rows in its list's order, not by name nor as weighed");
      assert.deepEqual(listed(f).antigravity, ["ag-a@x.test", "ag-c@x.test", "ag-b@x.test"]);

      // drag the subscription's first account down one, in its editor
      await page.locator('#nav button[data-view="providers"]').click();
      await page.locator('.row.provider[data-id="antigravity"]').click();
      const first = page.locator('.accts [data-account-id="ag-a@x.test"]');
      await first.focus();
      await page.keyboard.press("Alt+ArrowDown");
      await page.waitForFunction(() => !accountArranging && !accountSaving && document.querySelector(".accts [data-account-id]")?.dataset.accountId === "ag-c@x.test");
      // the plugin's, rearranged as its editor's drag does it
      await page.evaluate(async () => { providers = await api("provider/arrange", { id: "fakeco", accountOrder: ["p-y@x.test", "p-z@x.test", "p-x@x.test"] }); });
      assert.deepEqual(listed(f).antigravity, ["ag-c@x.test", "ag-a@x.test", "ag-b@x.test"]);

      // back on Routing, no reload and no new request: the stage follows
      await page.keyboard.press("Escape");
      await page.locator("#modal .ehead").waitFor({ state: "hidden" });
      await page.locator('#nav button[data-view="routing"]').click();
      await page.waitForFunction((want) => {
        const by = {};
        for (const l of document.querySelectorAll(".rt-accts > li[title]")) { const p = l.title.split(/[@#]/)[0]; (by[p] ||= []).push(l.querySelector(".who").textContent); }
        return JSON.stringify(by) === JSON.stringify(want);
      }, listed(f), { timeout: 4000 }).catch(() => {});
      assert.deepEqual(await seated(page), listed(f), "the stage follows the drag");
      assert.deepEqual(errors, []);
    });
  }
}
