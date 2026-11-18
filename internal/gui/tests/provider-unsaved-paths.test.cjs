// Run with Node's test runner and Playwright on the module path; see README.md.
// Duplicate and Add another in a provider's editor open a new draft in its
// place, so they ask before dropping typed changes, as Cancel does. Cancel
// keeps the edit as typed; Discard goes on. An untouched draft asks nothing.
// Both engines, English and Chinese; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));

function server(lang) {
  const relay = { id: "relay", name: "Relay", icon: "generic", preset: "relay", host: "relay.test", chat: "https://relay.test/v1",
    responses: "", anthropic: "", models: [{ id: "fixture-model", name: "Fixture model", on: true }], agents: [], fallback: [],
    headers: { "X-Fixture": "kept" }, key: { set: true, masked: "sk-…1234" }, keyList: [], ready: true };
  const presets = [
    { id: "relay", name: "Relay", icon: "generic", kind: "relay", chat: relay.chat, added: true },
    { id: "acme", name: "Acme", icon: "openai", kind: "vendor", chat: "https://api.acme.example.com/v1", added: false },
    { id: "beta", name: "Beta", icon: "openai", kind: "vendor", chat: "https://api.beta.example.com/v1", added: false },
  ];
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [relay], presets, excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { dup: "Duplicate", more: "Add another Relay", ask: /Discard unsaved/, cancel: /^Cancel$/, copy: "Relay copy" },
  zh: { dup: "复制", more: "再添加一个 Relay", ask: /放弃未保存/, cancel: /^取消$/, copy: "Relay 副本" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(L)) test(`${engine} ${lang}: Duplicate and Add another ask before dropping edits`, async (t) => {
    const w = L[lang];
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" });
    page.setDefaultTimeout(5000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    page.on("dialog", (d) => { errors.push("native dialog"); d.dismiss(); });
    await page.route("**/*", server(lang));
    await page.goto("http://magpie.test/?view=providers");
    const modal = page.locator("#modal"), ask = page.getByRole("alertdialog");
    const name = modal.locator("input.provider-name");
    for (const button of [w.dup, w.more]) {
      await page.locator('#providers .row.provider[data-id="relay"]').click();
      await name.waitFor();
      // untouched: no question
      await modal.getByRole("button", { name: button, exact: true }).click();
      await page.waitForTimeout(200);
      assert.equal(await ask.count(), 0, button + " asked with nothing typed");
      await page.keyboard.press("Escape");
      await modal.waitFor({ state: "hidden" });
      await page.locator('#providers .row.provider[data-id="relay"]').click();
      await name.fill("Relay edited");
      await modal.getByRole("button", { name: button, exact: true }).click();
      await ask.waitFor();
      assert.match(await ask.textContent(), w.ask);
      await ask.getByRole("button", { name: w.cancel }).click();
      await ask.waitFor({ state: "detached" });
      assert.equal(await name.inputValue(), "Relay edited", button + ": Cancel keeps the edit");
      await modal.getByRole("button", { name: button, exact: true }).click();
      await ask.waitFor();
      await ask.locator("button").last().click();
      await ask.waitFor({ state: "detached" });
      await page.waitForFunction(() => !document.querySelector("#modal .bar")?.textContent.includes("Remove") && !document.querySelector("#modal .bar")?.textContent.includes("删除"));
      if (button === w.dup) assert.ok(await modal.evaluate((m, copy) => [...m.querySelectorAll("input")].some((i) => i.value === copy), w.copy), "the copy opened");
      await page.keyboard.press("Escape");
      if (await ask.count()) { await ask.locator("button").last().click(); }
      await modal.waitFor({ state: "hidden" });
    }
    assert.deepEqual(errors, []);
  });
}
