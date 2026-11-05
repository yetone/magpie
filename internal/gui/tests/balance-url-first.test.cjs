// Run with Node's test runner and Playwright on the module path; see README.md.
// A relay's user who wanted its balance found the account token's field
// first, asking for a new-api System Access Token or a sub2api JWT, while
// the Balance URL that reads it with the key alone sat at the foot of the
// form under More endpoints (a relay's report, 11-05). A custom provider's
// Balance URL and field now stand in Advanced right above the token, whose
// hint says to leave it empty for the key's own balance; a saved Balance
// URL opens Advanced.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [],
  balanceURL: "https://relay.example.com/v1/usage", balancePath: "",
  balanceToken: { takes: true, set: false },
};
const plain = { ...relay, id: "plain", name: "Plain", balanceURL: "" };

function server(lang, list) {
  const providers = { providers: list, presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: true })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/provider/balance") return json({ ok: true, amount: "$10472.14" });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the order the rows of Advanced come in, by their labels
const order = (page, labels) => page.locator(".editor details.advanced").evaluate((adv, labels) => {
  const all = [...adv.querySelectorAll("*")].filter((e) => !e.children.length && labels.includes(e.textContent.trim()));
  return labels.map((l) => all.findIndex((e) => e.textContent.trim() === l));
}, labels);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a custom provider's Balance URL comes before the account token", async (t) => {
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
          await page.route("**/*", server(lang, [relay, plain]));
          await page.goto("http://magpie.test/?view=providers");
          const tr = (k) => page.evaluate((k) => t(k), k);
          const labels = [await tr("Balance URL"), await tr("Balance field"), await tr("Account balance")];
          const hint = await tr("Leave it empty to read the key's own balance: most relays tell it at the Balance URL above (a sub2api relay's …/v1/usage, a new-api relay's …/api/usage/token). Only for the whole account's balance: a new-api relay's System Access Token, or a sub2api panel's login token (a JWT, sent as a Bearer); it is used for nothing else.");
          if (lang !== "en") assert(!hint.startsWith("Leave it empty"), "the hint is translated");

          // the reporter's case: a saved relay with its /v1/usage
          await page.locator(".row.provider", { hasText: "Relay" }).click();
          assert(await page.locator(".editor details.advanced").evaluate((d) => d.open), "a saved Balance URL opens Advanced");
          assert.equal(await page.locator(".editor details.advanced .bal-url").count(), 1, "the Balance URL is in Advanced");
          assert.equal(await page.locator(".editor details.more:not(.advanced) .bal-url").count(), 0, "not under More endpoints");
          const [u, f, a] = await order(page, labels);
          assert(u >= 0 && f > u && a > f, `Balance URL, then its field, then the token: ${[u, f, a]}`);
          assert.equal(await page.locator(".editor details.advanced").getByText(hint, { exact: true }).count(), 1, "the token's hint says to leave it empty");
          await page.locator(".editor .bal-url").scrollIntoViewIfNeeded();
          assert(await page.locator(".editor .bal-url").isVisible());
          await page.locator(".editor").getByRole("button", { name: await tr("Check balance"), exact: true }).click();
          await page.locator(".editor .bal-res.ok").waitFor();
          // the balance rows fit at a narrow width, the URL wide enough to read
          const fit = await page.locator(".editor details.advanced").evaluate((adv) => {
            const a = adv.getBoundingClientRect();
            const rows = [adv.querySelector(".bal-ask"), adv.querySelector(".bal-path"), adv.querySelector(".bal-fix")];
            return { inside: rows.every((e) => { const r = e.getBoundingClientRect(); return r.right <= a.right + 1; }), url: adv.querySelector(".bal-url").getBoundingClientRect().width >= 120 };
          });
          assert.deepEqual(fit, { inside: true, url: true });

          // one with no Balance URL keeps Advanced shut, as before
          await page.locator(".editor .bar").getByRole("button", { name: await tr("Cancel"), exact: true }).click();
          await page.locator(".row.provider", { hasText: "Plain" }).click();
          assert.equal(await page.locator(".editor details.advanced").evaluate((d) => d.open), false, "nothing set, nothing opened");

          // a new custom provider: the same order
          await page.evaluate(() => { adding = true; editing = { custom: true }; draft = null; renderProviders(); });
          await page.locator(".editor details.advanced > summary").click();
          const [nu, nf, na] = await order(page, labels);
          assert(nu >= 0 && nf > nu && na > nf, `new: ${[nu, nf, na]}`);
          assert.deepEqual(errors, []);
          await context.close();
        });
      }
    }
  });
}
