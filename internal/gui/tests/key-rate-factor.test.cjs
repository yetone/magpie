// Run with Node's test runner and Playwright on the module path; see README.md.
// A key's cost factor (@YIXIE233 on X): groups of one relay whose
// upstreams cache 60% and 90% of a prompt cost 2-3x apart at the same
// group rate, so a key's rate read from the relay takes a factor of its
// own. The factors show, in a column of their own beside the keys' rates,
// only while the provider has a top-up rate; a key left empty shows its
// group rate x the top-up rate x its factor as its placeholder, following
// the boxes as they are typed. A provider of one key has the box under the
// top-up rate. Save posts the factors, {} when all are emptied, and
// refuses one out of range or past four decimals before anything is
// posted. In every language, wide and narrow.
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
  keyPriceRates: { cccccccccc: 0.3 }, topUpRate: 0.07, keyGroupRates: { aaaaaaaaaa: 1, bbbbbbbbbb: 2.5, cccccccccc: 4 }, keyRateFactors: { bbbbbbbbbb: 1.5 } };
const plain = { ...base, id: "plain", name: "Plain", keyList: [key("aaaaaaaaaa", "One"), key("bbbbbbbbbb", "Two")] };
const solo = { ...base, id: "solo", name: "Solo", keyList: [key("aaaaaaaaaa", "Only")], topUpRate: 0.07, keyGroupRates: { aaaaaaaaaa: 2 } };

function serve(lang, posts) {
  const providers = { providers: [relay, plain, solo], presets: [], excluded: [], gateway: { running: true, window: true } };
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

const strings = ["Save", "Cost factor", "Cost factor of {user}", "Cost factor of {user}: a number above 0 up to 1000, at most four decimals"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [900, 420]) {
      const open = async (t, name) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [];
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: name }).click();
        await page.locator(".editor details.advanced").waitFor({ state: "attached" });
        await unfold(page);
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
      const top = (page) => page.locator(".editor input.top-up-rate");
      const rate = (page, id) => page.locator(`.editor .key-rate[data-key="${id}"] input.key-price-rate`);
      const factor = (page, id) => page.locator(`.editor .key-rate[data-key="${id}"] input.key-rate-factor`);
      // nothing in the editor is wider than the editor
      const fits = (page) => page.evaluate(() => {
        const ed = document.querySelector(".editor .inner") || document.querySelector(".editor");
        const r = ed.getBoundingClientRect();
        return [...ed.querySelectorAll(".key-rate input, .key-rate-head span")].filter((n) => n.offsetParent).filter((n) => { const b = n.getBoundingClientRect(); return b.left < r.left - 1 || b.right > r.right + 1; })
          .map((n) => `${n.className || n.textContent} ${Math.round(n.getBoundingClientRect().right)} > ${Math.round(r.right)}`);
      });

      test(`${engine} ${lang} ${width}: each key's factor on its read rate`, async (t) => {
        const { page, errors, posts, w } = await open(t, "Relay");
        for (const s of strings) assert(w[s], `${s} has its ${lang}`);
        await factor(page, "bbbbbbbbbb").waitFor();
        assert(await factor(page, "aaaaaaaaaa").isVisible(), "a top-up rate shows the factors");
        assert.equal(await factor(page, "aaaaaaaaaa").getAttribute("placeholder"), "1");
        assert.equal(await factor(page, "bbbbbbbbbb").inputValue(), "1.5");
        assert.equal(await factor(page, "bbbbbbbbbb").getAttribute("aria-label"), w["Cost factor of {user}"].replace("{user}", "Pro"));
        assert(await page.locator(".editor .key-rate-head", { hasText: w["Cost factor"] }).isVisible(), "the column is named");
        assert.deepEqual(await fits(page), [], "the lines fit the editor");
        // group rate x top-up rate x factor
        assert.equal(await rate(page, "aaaaaaaaaa").getAttribute("placeholder"), "0.07");
        assert.equal(await rate(page, "bbbbbbbbbb").getAttribute("placeholder"), "0.2625");
        assert.equal(await rate(page, "cccccccccc").inputValue(), "0.3", "the key's own rate stands");
        await factor(page, "aaaaaaaaaa").fill("2");
        assert.equal(await rate(page, "aaaaaaaaaa").getAttribute("placeholder"), "0.14");
        await top(page).fill("0.1");
        assert.equal(await rate(page, "bbbbbbbbbb").getAttribute("placeholder"), "0.375");
        // no top-up rate, no factors
        await top(page).fill("");
        assert(!(await factor(page, "aaaaaaaaaa").isVisible()), "no top-up rate hides them");
        assert(!(await page.locator(".editor .key-rate-head").isVisible()));
        await top(page).fill("0.07");
        assert(await factor(page, "aaaaaaaaaa").isVisible());

        for (const v of ["1.23456", "1001", "0", "-1", "abc"]) {
          await factor(page, "aaaaaaaaaa").fill(v);
          await (await saveButton(page)).click();
          await page.locator(".editor .editor-error").waitFor();
          assert.equal(await page.locator(".editor .editor-error").textContent(), w["Cost factor of {user}: a number above 0 up to 1000, at most four decimals"].replace("{user}", "Cheap"), v);
          assert.equal(posts.length, 0, `${v} is not posted`);
          assert(await factor(page, "aaaaaaaaaa").evaluate((b) => b === document.activeElement), "the box is focused");
        }
        await factor(page, "aaaaaaaaaa").fill("0,8");
        let saved = await save(page, posts);
        assert.equal(saved.action, "save");
        assert.deepEqual(saved.body.keyRateFactors, { aaaaaaaaaa: 0.8, bbbbbbbbbb: 1.5 });
        assert.equal(saved.body.topUpRate, 0.07);
        assert.deepEqual(saved.body.keyPriceRates, { cccccccccc: 0.3 });

        await page.locator(".row.provider", { hasText: "Relay" }).click();
        await unfold(page);
        await factor(page, "bbbbbbbbbb").waitFor();
        await factor(page, "bbbbbbbbbb").fill("");
        saved = await save(page, posts);
        assert.deepEqual(saved.body.keyRateFactors, {}, "emptied, it is taken off");
        assert.deepEqual(errors, []);
      });

      // Drawing the saved factors changes nothing: leaving the editor asks
      // no Discard, and the column's heading is not a key's line.
      test(`${engine} ${lang} ${width}: saved factors leave the editor unchanged`, async (t) => {
        const { page, errors, posts } = await open(t, "Relay");
        await factor(page, "bbbbbbbbbb").waitFor();
        assert.equal(await page.locator(".editor .key-rate").count(), 3, "one line a key");
        const modal = page.locator("#modal"), ask = page.getByRole("alertdialog");
        // Escape leaves an untouched editor at once; a draft it thinks
        // changed asks Discard first
        const leave = async () => {
          await page.keyboard.press("Escape");
          await Promise.race([modal.waitFor({ state: "hidden" }), ask.waitFor()]);
          assert.equal(await ask.count(), 0, "no Discard asked");
        };
        await leave();
        await page.locator(".row.provider", { hasText: "Solo" }).click();
        await page.locator(".editor input.key-rate-factor").waitFor();
        await leave();
        await page.locator(".row.provider", { hasText: "Relay" }).click();
        await unfold(page);
        await factor(page, "bbbbbbbbbb").waitFor();
        assert.equal(await factor(page, "bbbbbbbbbb").inputValue(), "1.5");
        assert.equal(posts.length, 0);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: one key's factor under the top-up rate; none without one`, async (t) => {
        const { page, errors, posts, w } = await open(t, "Solo");
        const box = page.locator(".editor input.key-rate-factor");
        await box.waitFor();
        assert.equal(await box.getAttribute("placeholder"), "1");
        assert(await page.locator(".editor .solo-factor", { hasText: w["Cost factor"] }).isVisible(), "labelled");
        await top(page).fill("");
        assert(!(await box.isVisible()), "no top-up rate hides it");
        await top(page).fill("0.07");
        await box.fill("1.25");
        let saved = await save(page, posts);
        assert.deepEqual(saved.body.keyRateFactors, { aaaaaaaaaa: 1.25 });

        await page.locator(".row.provider", { hasText: "Plain" }).click();
        await unfold(page);
        await rate(page, "bbbbbbbbbb").waitFor();
        assert(!(await factor(page, "aaaaaaaaaa").isVisible()), "no top-up rate, no factor column");
        assert(!(await page.locator(".editor .key-rate-head").isVisible()));
        saved = await save(page, posts);
        assert.deepEqual(saved.body.keyRateFactors, {});
        assert.deepEqual(errors, []);
      });
    }
  }
}
