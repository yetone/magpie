// Run with Node's test runner and Playwright on the module path; see README.md.
// A relay added by hand with no Balance URL has its balance found by magpie
// (provider/balance_probe.go). The editor says where, under the Balance URL,
// so an amount read at the wrong query can be put right: Use copies the
// query into the fields. A check that found nothing says where it looked.
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
  balanceURL: "", balancePath: "",
  balanceFound: "https://relay.example.com/v1/usage", balanceFoundField: "$remaining",
  balanceToken: { takes: true, set: false },
};
const plain = { ...relay, id: "plain", name: "Plain", balanceFound: "", balanceFoundField: "" };
const own = { ...relay, id: "own", name: "Own", balanceURL: "https://relay.example.com/mine" };

function server(lang, list, checks) {
  const providers = { providers: list, presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: true })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/provider/balance") return json(checks(JSON.parse(route.request().postData() || "{}")));
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the editor says where magpie found a relay's balance, and Use puts it in the fields", async (t) => {
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
          const checks = (b) => b.id === "plain"
            ? (b.balanceURL ? { ok: true, amount: "$1.00" } : { ok: false, amount: "", looked: true, found: "", foundField: "" })
            : { ok: true, amount: "$5.00", looked: !b.balanceURL, found: b.balanceURL ? "" : "https://relay.example.com/api/usage/token", foundField: "$data.total_available / 500000" };
          await page.route("**/*", server(lang, [relay, plain, own], checks));
          await page.goto("http://magpie.test/?view=providers");
          const tr = (k, v) => page.evaluate(([k, v]) => t(k, v), [k, v]);
          const open = async (name) => {
            await page.goto("http://magpie.test/?view=providers");
            await page.locator(".row.provider").first().waitFor();
            errors.length = 0; // the last page's requests, cut off by the load
            await page.locator(".row.provider", { hasText: name }).first().click();
            const adv = page.locator(".editor details.advanced");
            if (!(await adv.evaluate((d) => d.open))) await adv.locator("> summary").click();
          };
          const line = page.locator(".editor .bal-found");

          // found by magpie: said under the Balance URL, with its field
          await open("Relay");
          await line.waitFor();
          assert.equal(await line.locator("span").textContent(), await tr("Found by magpie: {url}", { url: "https://relay.example.com/v1/usage $remaining" }));
          if (lang !== "en") assert(!(await line.locator("span").textContent()).startsWith("Found by"), "translated");
          const fit = await line.evaluate((e) => { const a = e.closest("details.advanced").getBoundingClientRect(), r = e.getBoundingClientRect(); return r.right <= a.right + 1; });
          assert(fit, "the line fits at " + width + "px");
          // a Balance URL typed hides it, and clearing it brings it back
          await page.locator(".editor .bal-url").fill("https://x.example.com/b");
          assert(await line.isHidden(), "hidden under a URL of the user's");
          await page.locator(".editor .bal-url").fill("");
          assert(await line.isVisible(), "back when the URL is cleared");
          // Check balance tells where it is found now
          await page.locator(".editor").getByRole("button", { name: await tr("Check balance"), exact: true }).click();
          await page.locator(".editor .bal-res.ok").waitFor();
          await page.waitForFunction(() => document.querySelector(".editor .bal-found span")?.textContent.includes("/api/usage/token"));
          // Use: the query goes into the fields, to be changed
          await line.getByRole("button", { name: await tr("Use"), exact: true }).click();
          assert.equal(await page.locator(".editor .bal-url").inputValue(), "https://relay.example.com/api/usage/token");
          assert.equal(await page.locator(".editor .bal-path").inputValue(), "$data.total_available / 500000");
          assert(await line.isHidden());
          assert.deepEqual(await page.evaluate(() => [draft.balanceURL, draft.balancePath]), ["https://relay.example.com/api/usage/token", "$data.total_available / 500000"]);

          // looked for and found nowhere: the check says where it looked
          await open("Plain");
          assert.equal(await line.count() && await line.isVisible(), false, "no line with nothing found");
          await page.locator(".editor").getByRole("button", { name: await tr("Check balance"), exact: true }).click();
          await page.waitForFunction(() => document.querySelector(".editor .bal-res")?.textContent !== "…");
          assert.equal(await page.locator(".editor .bal-res").textContent(), await tr("No balance at /v1/usage or /api/usage/token"));

          // the user's own Balance URL: nothing of magpie's
          await open("Own");
          assert.equal(await line.count(), 0, "not drawn under a saved Balance URL");
          assert.deepEqual(errors, []);
          await context.close();
        });
      }
    }
  });
}
