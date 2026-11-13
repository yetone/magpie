// Run with Node's test runner and Playwright on the module path; see README.md.
// A key's own price rate (@tnt12231 on X: a relay bills its key groups at
// rates of their own). A provider of two keys or more has a line for each
// key under Advanced, next to its Price rate; a key left empty shows the
// provider's rate as its placeholder and bills at it. Save posts the keys
// that have one, {} when none has, and refuses a rate that is 0, out of
// range or has more than three decimals before anything is posted. A key a
// fold hides keeps its rate. A provider of one key has no lines and posts
// none, so the server keeps what it had. In every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const unfold = async (page) => {
  const adv = page.locator(".editor details.advanced");
  await adv.waitFor({ state: "attached" });
  if (!(await adv.evaluate((d) => d.open))) await adv.locator(":scope > summary").click();
};
const key = (id, name) => ({ id, name, masked: `sk-…${id.slice(0, 3)}`, active: id === "aaaaaaaaaa", on: true });
const base = {
  icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, balanceToken: { takes: false, set: false }, proxy: "", maxConcurrency: null,
};
const relay = { ...base, id: "relay", name: "Relay", keyList: [key("aaaaaaaaaa", "Half"), key("bbbbbbbbbb", "Premium"), key("cccccccccc", "Plain")], keyPriceRates: { bbbbbbbbbb: 1.2 } };
const many = { ...base, id: "many", name: "Many", keyList: Array.from({ length: 10 }, (_, i) => key(String(i).repeat(10), `Key ${i}`)), keyPriceRates: { "8888888888": 0.6 } };
const single = { ...base, id: "single", name: "Single", keyList: [key("aaaaaaaaaa", "Only")] };

function serve(lang, posts) {
  const providers = { providers: [relay, many, single], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/provider/") && route.request().method() === "POST") {
      posts.push({ action: url.pathname.slice("/api/provider/".length), body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const strings = ["Save", "Price rate per key", "A key left empty bills at the provider's price rate", "Price rate of {user}",
  "Price rate of {user}: a number above 0 up to 1000, at most three decimals"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    const open = async (t, name) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posts = [];
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: name }).click();
      await page.locator(".editor details.advanced").waitFor({ state: "attached" });
      await page.waitForTimeout(300);
      const w = await page.evaluate(([lang, strings]) => Object.fromEntries(strings.map((s) => [s, lang === "en" ? s : I18N[lang]?.[s]])), [lang, strings]);
      return { page, errors, posts, w };
    };
    const saveButton = async (page) => page.locator(".editor .bar").getByRole("button", { name: await page.evaluate((lang) => lang === "en" ? "Save" : I18N[lang].Save, lang), exact: true });
    const rate = (page, id) => page.locator(`.editor .key-rate[data-key="${id}"] input.key-price-rate`);
    const save = async (page, posts) => {
      const n = posts.length;
      await (await saveButton(page)).click();
      for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
      return posts.at(-1);
    };

    test(`${engine} ${lang}: each key gets its own price rate, the rest the provider's`, async (t) => {
      const { page, errors, posts, w } = await open(t, "Relay");
      for (const s of strings) assert(w[s], `${s} has its ${lang}`);
      assert(await page.locator(".editor details.advanced").evaluate((d) => d.open), "a key's own rate opens Advanced");
      assert.equal(await page.locator(".editor label", { hasText: w["Price rate per key"] }).count(), 1);
      assert.equal(await page.locator(".editor .key-rates > .hint").textContent(), w["A key left empty bills at the provider's price rate"]);
      assert.equal(await page.locator(".editor .key-rate").count(), 3);
      assert.equal(await rate(page, "bbbbbbbbbb").inputValue(), "1.2");
      assert.equal(await rate(page, "aaaaaaaaaa").inputValue(), "");
      assert.equal(await rate(page, "aaaaaaaaaa").getAttribute("placeholder"), "1", "the official price when the provider has no rate");
      assert.equal(await rate(page, "aaaaaaaaaa").getAttribute("aria-label"), w["Price rate of {user}"].replace("{user}", "Half"));

      // the provider's rate is what an empty key shows
      await page.locator(".editor input.price-rate").fill("0.9");
      assert.equal(await rate(page, "cccccccccc").getAttribute("placeholder"), "0.9");

      const bad = w["Price rate of {user}: a number above 0 up to 1000, at most three decimals"].replace("{user}", "Half");
      for (const v of ["0", "0.1234", "1001", "-1", "abc"]) {
        await rate(page, "aaaaaaaaaa").fill(v);
        await (await saveButton(page)).click();
        await page.locator(".editor .editor-error").waitFor();
        assert.equal(await page.locator(".editor .editor-error").textContent(), bad, v);
        assert.equal(posts.length, 0, `${v} is not posted`);
      }
      // a comma for the decimal point, as some keyboards type it
      await rate(page, "aaaaaaaaaa").fill("0,5");
      let saved = await save(page, posts);
      assert.equal(saved.action, "save");
      assert.equal(saved.body.priceRate, 0.9);
      assert.deepEqual(saved.body.keyPriceRates, { aaaaaaaaaa: 0.5, bbbbbbbbbb: 1.2 });

      await page.locator(".row.provider", { hasText: "Relay" }).click();
      await unfold(page);
      await rate(page, "bbbbbbbbbb").fill("");
      saved = await save(page, posts);
      assert.deepEqual(saved.body.keyPriceRates, {}, "every key back on the provider's rate is {}, not left out");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a key a fold hides keeps its rate`, async (t) => {
      const { page, errors, posts } = await open(t, "Many");
      await unfold(page);
      // the first five, and the one with a rate of its own
      assert.equal(await page.locator(".editor .key-rate").count(), 6);
      assert.equal(await rate(page, "8888888888").inputValue(), "0.6");
      await rate(page, "0000000000").fill("2");
      const saved = await save(page, posts);
      assert.deepEqual(saved.body.keyPriceRates, { "0000000000": 2, "8888888888": 0.6 });
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: one key has no lines and posts none`, async (t) => {
      const { page, errors, posts } = await open(t, "Single");
      await unfold(page);
      await page.locator(".editor input.price-rate").waitFor();
      assert.equal(await page.locator(".editor .key-rates").count(), 0);
      const saved = await save(page, posts);
      assert(!Object.hasOwn(saved.body, "keyPriceRates"), "the server keeps what it had");
      assert.deepEqual(errors, []);
    });
  }
}
