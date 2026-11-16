// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider's balance, as the Usage page reads it, is a quiet figure on its
// row in Providers too (lyj1115 on X: magpie found the relay's balance, and
// the row showed only the API key). Several keys' balances are added up as
// the Usage card adds them; a failed read, a signed-in account and the Usage
// page turned off show none. The read is Usage's own, never an asked one.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const base = {
  icon: "generic", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [{ id: "a", masked: "sk-…one", active: true, on: true }],
  balanceURL: "", balancePath: "", balanceToken: { takes: false, set: false },
};
const relay = { ...base, id: "relay", name: "Relay", host: "relay.example.com", chat: "https://relay.example.com/v1", balanceProbe: true, balanceFound: "https://relay.example.com/v1/usage", balanceFoundField: "$remaining" };
const many = { ...base, id: "many", name: "Many", host: "many.example.com", key: { set: true, masked: "sk-…aaa" },
  keyList: [{ id: "a", name: "work", masked: "sk-…aaa", active: true, on: true }, { id: "b", masked: "sk-…bbb", on: true }] };
const broke = { ...base, id: "broke", name: "Broke", host: "broke.example.com" };
const none = { ...base, id: "none", name: "Nothing", host: "none.example.com" };
const acct = { ...base, id: "acct", name: "Acct", host: "acct.example.com", key: { set: false, masked: "" }, keyList: [], account: { user: "me@example.com", agentName: "Codex", plan: "Plus", logins: [] } };
const quotas = [
  { provider: "relay", name: "Relay", icon: "generic", windows: [], balance: "$10472.14" },
  { provider: "many", name: "Many", icon: "generic", user: "work", windows: [], balance: "$12.50" },
  { provider: "many", name: "Many", icon: "generic", user: "sk-…bbb", windows: [], balance: "$7.50" },
  { provider: "broke", name: "Broke", icon: "generic", windows: [], error: "401 invalid key", balance: "$1.00" },
  { provider: "acct", name: "Acct", icon: "generic", windows: [], balance: "$3.00" },
];

function server(lang, featuresOff, asked) {
  const providers = { providers: [relay, many, broke, none, acct], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: true, featuresOff })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/usage/quotas") { asked.push(url.search); return json(quotas); }
    if (url.pathname.startsWith("/api/usage/quotas/history")) return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a provider's balance shows on its row in Providers", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const width of [900, 420]) {
      for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
        await t.test(`${width}px ${lang}`, async () => {
          const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
          const page = await context.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          const asked = [];
          await page.route("**/*", server(lang, [], asked));
          await page.goto("http://magpie.test/?view=providers");
          const row = (id) => page.locator(`#providers .row.provider[data-id="${id}"]`);
          const bal = (id) => row(id).locator(".who > .sub > .pv-bal");
          await bal("relay").waitFor();
          const tr = (k, v) => page.evaluate(([k, v]) => t(k, v), [k, v]);

          // the relay's found balance, quietly after its models
          assert.equal(await bal("relay").textContent(), " · $10472.14");
          assert.equal(await bal("relay").getAttribute("title"), (await tr("Balance")) + " $10472.14");
          assert.match(await row("relay").locator(".who > .sub").textContent(), /^relay\.example\.com · .+ · \$10472\.14$/);
          // two keys: added up, as the Usage card's head does
          const sum = await page.evaluate(() => balanceAmount({ balance: "$12.50" }, 20)); // "$20,00" in German
          assert.match(sum, /^\$20[.,]00$/);
          assert.equal(await bal("many").textContent(), " · " + sum);
          // a read that failed, no card, a signed-in account: nothing
          for (const id of ["broke", "none", "acct"]) assert.equal(await bal(id).count(), 0, id);
          // Usage's own read, not an asked one
          assert(asked.length >= 1 && asked.every((s) => !s.includes("asked")), "asked: " + asked.join(","));
          // a quiet figure: the row's facts' colour and size, on one line
          const look = await row("relay").evaluate((r) => {
            const sub = r.querySelector(".who > .sub"), b = sub.querySelector(".pv-bal"), s = getComputedStyle(sub), c = getComputedStyle(b);
            return { color: s.color === c.color, size: s.fontSize === c.fontSize, oneLine: sub.scrollHeight <= parseFloat(s.lineHeight || "16") * 1.6 + 2 || sub.getClientRects().length === 1, fits: sub.getBoundingClientRect().right <= r.getBoundingClientRect().right + 1 };
          });
          assert.deepEqual(look, { color: true, size: true, oneLine: true, fits: true });
          // drawn again with the list, never twice
          await page.evaluate(() => renderProviders());
          assert.equal(await row("relay").locator(".pv-bal").count(), 1);
          assert.deepEqual(errors, []);
          await context.close();
        });
      }
    }
    await t.test("Usage turned off: no balance, nothing read", async () => {
      const context = await browser.newContext({ viewport: { width: 900, height: 760 } });
      const page = await context.newPage();
      const asked = [];
      await page.route("**/*", server("en", ["usage"], asked));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator('#providers .row.provider[data-id="relay"]').waitFor();
      // show() asks for Usage's read before the list is drawn, so none was asked
      assert.equal(await page.evaluate(() => quotas === null && !quotasLoading), true);
      assert.equal(await page.locator("#providers .pv-bal").count(), 0);
      assert.deepEqual(asked, []);
      await context.close();
    });
  });
}
