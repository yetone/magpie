// Run with Node's test runner and Playwright on the module path; see README.md.
// A relay's top-up rate (@YIXIE233 on X: relays on sub2api report each
// key's group rate, and each sells its dollars at its own price). A
// provider of API keys has the box under Advanced; a subscription has
// none. The rates read from the relay are named under it, and a key left
// empty shows its group rate times the top-up rate as its placeholder,
// following the box as it is typed; a key's own rate stands. Save posts
// the rate, null when emptied, and refuses one out of range or past four
// decimals before anything is posted. How often the rates are read
// (@YIXIE233 on X asked for 3 hours) sits under it while a top-up rate is
// typed: whole hours from 1 to 168, null when emptied. In every language.
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
const relay = { ...base, id: "relay", name: "Relay", keyList: [key("aaaaaaaaaa", "Cheap"), key("bbbbbbbbbb", "Pro"), key("cccccccccc", "Own")],
  keyPriceRates: { cccccccccc: 0.3 }, topUpRate: 0.07, rateReadHours: 3, keyGroupRates: { aaaaaaaaaa: 1, bbbbbbbbbb: 2.5 } };
const plain = { ...base, id: "plain", name: "Plain", keyList: [key("aaaaaaaaaa", "Only")] };
const sub = { ...base, id: "codex", name: "Codex Sub", icon: "openai", account: { kind: "codex" }, key: { set: false }, keyList: [] };

function serve(lang, posts) {
  const providers = { providers: [relay, plain, sub], presets: [], excluded: [], gateway: { running: true, window: true } };
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

const strings = ["Save", "Top-up rate", "What one dollar of the relay cost you, like 0.07 for ¥7 per $100. Each key's group rate is read from the relay, and its calls cost the two multiplied",
  "Read from the relay: {rates}", "Top-up rate: a number above 0 up to 1000, at most four decimals",
  "Read rates every (hours)", "Read rates every: a whole number of hours from 1 to 168"];

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
    const save = async (page, posts) => {
      const n = posts.length;
      await (await saveButton(page)).click();
      for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
      return posts.at(-1);
    };
    const box = (page) => page.locator(".editor input.top-up-rate");
    const rate = (page, id) => page.locator(`.editor .key-rate[data-key="${id}"] input.key-price-rate`);

    test(`${engine} ${lang}: a relay's top-up rate times each key's group rate`, async (t) => {
      const { page, errors, posts, w } = await open(t, "Relay");
      for (const s of strings) assert(w[s], `${s} has its ${lang}`);
      assert(await page.locator(".editor details.advanced").evaluate((d) => d.open), "a top-up rate opens Advanced");
      await box(page).waitFor();
      assert.equal(await box(page).inputValue(), "0.07");
      const text = await page.locator(".editor details.advanced").textContent();
      assert(text.includes(w["Read from the relay: {rates}"].replace("{rates}", "Cheap ×1, Pro ×2.5")), "the rates read are named");
      // an empty key shows what it bills at: its group's rate × the top-up rate
      await rate(page, "bbbbbbbbbb").waitFor();
      assert.equal(await rate(page, "aaaaaaaaaa").getAttribute("placeholder"), "0.07");
      assert.equal(await rate(page, "bbbbbbbbbb").getAttribute("placeholder"), "0.175");
      assert.equal(await rate(page, "cccccccccc").inputValue(), "0.3", "the key's own rate stands");
      await box(page).fill("0.08");
      assert.equal(await rate(page, "bbbbbbbbbb").getAttribute("placeholder"), "0.2");

      for (const v of ["0.07142", "1001", "-1", "abc"]) {
        await box(page).fill(v);
        await (await saveButton(page)).click();
        await page.locator(".editor .editor-error").waitFor();
        assert.equal(await page.locator(".editor .editor-error").textContent(), w["Top-up rate: a number above 0 up to 1000, at most four decimals"], v);
        assert.equal(posts.length, 0, `${v} is not posted`);
      }
      // a comma for the decimal point, as some keyboards type it
      await box(page).fill("0,0714");
      let saved = await save(page, posts);
      assert.equal(saved.action, "save");
      assert.equal(saved.body.topUpRate, 0.0714);
      assert.deepEqual(saved.body.keyPriceRates, { cccccccccc: 0.3 });

      await page.locator(".row.provider", { hasText: "Relay" }).click();
      await box(page).waitFor();
      await box(page).fill("");
      saved = await save(page, posts);
      assert.equal(saved.body.topUpRate, null, "emptied, it is taken off");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: how often the rates are read`, async (t) => {
      const { page, errors, posts, w } = await open(t, "Relay");
      const every = page.locator(".editor input.rate-read-hours");
      await every.waitFor();
      assert.equal(await every.inputValue(), "3");
      assert.equal(await every.getAttribute("placeholder"), "6", "empty is six hours");
      assert.equal(await every.getAttribute("aria-label"), w["Read rates every (hours)"]);
      for (const v of ["0", "169", "2.5", "abc"]) {
        await every.fill(v);
        await (await saveButton(page)).click();
        await page.locator(".editor .editor-error").waitFor();
        assert.equal(await page.locator(".editor .editor-error").textContent(), w["Read rates every: a whole number of hours from 1 to 168"], v);
        assert.equal(posts.length, 0, `${v} is not posted`);
      }
      await every.fill("12");
      let saved = await save(page, posts);
      assert.equal(saved.body.rateReadHours, 12);
      assert.equal(saved.body.topUpRate, 0.07);
      await page.locator(".row.provider", { hasText: "Relay" }).click();
      await every.waitFor();
      await every.fill("");
      saved = await save(page, posts);
      assert.equal(saved.body.rateReadHours, null, "emptied, back to six");

      // a provider with no top-up rate reads nothing: the box shows once one is typed
      await page.locator(".row.provider", { hasText: "Plain" }).click();
      await unfold(page);
      await box(page).waitFor();
      assert(await every.isHidden(), "no top-up rate, no interval");
      await box(page).fill("0.5");
      await every.waitFor();
      await box(page).fill("");
      assert(await every.isHidden());
      saved = await save(page, posts);
      assert.equal(saved.body.topUpRate, null);
      assert(!("rateReadHours" in saved.body), "no top-up rate, the interval is left as it was");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a provider of keys has it under Advanced, a subscription none`, async (t) => {
      const { page, errors, posts } = await open(t, "Plain");
      assert(!(await page.locator(".editor details.advanced").evaluate((d) => d.open)), "nothing set, Advanced stays folded");
      await unfold(page);
      await box(page).waitFor();
      assert.equal(await box(page).inputValue(), "");
      const saved = await save(page, posts);
      assert.equal(saved.body.topUpRate, null);
      await page.locator(".row.provider", { hasText: "Codex Sub" }).click();
      await unfold(page);
      await page.waitForTimeout(300);
      assert.equal(await box(page).count(), 0);
      assert.deepEqual(errors, []);
    });
  }
}
