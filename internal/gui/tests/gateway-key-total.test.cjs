// A gateway key's limit "in total" (@Cyrus_4433 on X): the window menu
// offers it, the row says "$0.00 / $300 · <total>" with no reset time, and
// Reset usage says counting starts again from now. In every language, both
// engines, and at a narrow width.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

const src = fs.readFileSync(path.resolve(__dirname, "../assets/i18n.js"), "utf8");
const I18N = new Function(src.slice(0, src.indexOf("\n};") + 3) + "; return I18N;")();

const day = 864e5;
const limits = () => ({
  laptop: {
    limit: { period: "total", cost: 300, since: new Date(Date.now() - 40 * day).toISOString() },
    used: { period: "total", start: new Date(Date.now() - 40 * day).toISOString(), reset: new Date(Date.now() + 36500 * day).toISOString(), calls: 90,
      tokens: 4000000, cost: 0, costLimit: 300, tokensLeft: 0, costLeft: 300, spent: false },
  },
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: a key's limit in total`, async (t) => {
      const tr = (k, vars = {}) => {
        const s = lang === "en" ? k : I18N[lang][k];
        assert.equal(typeof s, "string", `${lang} has ${k}`);
        return s.replace(/\{(\w+)\}/g, (_, v) => String(vars[v]));
      };
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 700 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(6000);
      page.setDefaultNavigationTimeout(60000);
      const events = [], errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", fixture(lang, "light", events, { lan: true, limits: limits() }));
      await page.goto("http://magpie.test/?view=gateway");
      await page.locator("#gatewayKeys .acc[data-key]").last().waitFor();
      const row = (id) => page.locator(`#gatewayKeys .acc[data-key="${id}"]`);
      const sent = (action) => events.filter((e) => e.action === action);
      const inTotal = tr("in total");
      if (lang === "zh") assert.equal(inTotal, "总额");

      // the row: used / limit · total, and no reset time
      const badge = row("laptop").locator(".amodels.limit");
      assert.equal((await badge.textContent()).trim(), `$0.00 / $300 · ${inTotal}`);
      assert.equal(await row("laptop").locator(".key-limit").first().textContent().then((s) => /\b(resets|重置|リセット|Reset)\b/i.test(s) && s.includes("2126")), false, "a total limit names a reset date");

      // the menu offers it last, and a new limit in total is sent as one
      await row("work").getByRole("button", { name: tr("Set a limit for this key"), exact: true }).click();
      const wed = row("work").locator(".key-limit-ed");
      await wed.waitFor();
      assert.equal(await wed.locator("select").count(), 0, "no native select");
      await wed.getByRole("textbox", { name: tr("Estimated cost limit, US$"), exact: true }).fill("300");
      await wed.getByRole("button", { name: tr("Limit window"), exact: true }).click();
      const items = page.locator(".proto-menu .pm-item");
      assert.equal((await items.last().textContent()).trim(), inTotal);
      await items.last().click();
      const win = wed.getByRole("button", { name: tr("Limit window"), exact: true });
      assert.equal(await win.getAttribute("data-value"), "total");
      assert.equal((await win.textContent()).trim(), inTotal);
      assert.equal(await wed.getByRole("textbox", { name: tr("Days in a cycle"), exact: true }).isVisible(), false);
      assert.equal(await wed.getByRole("textbox", { name: tr("Hours in a cycle"), exact: true }).isVisible(), false);
      // narrow: the editor fits
      await page.setViewportSize({ width: 500, height: 740 });
      await page.waitForTimeout(100);
      assert.equal(await wed.evaluate((e) => e.scrollWidth > e.clientWidth || e.getBoundingClientRect().right > window.innerWidth), false, "the editor overflows at 500px");
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth), false, "the page overflows at 500px");
      await page.setViewportSize({ width: 1000, height: 700 });
      await wed.getByRole("button", { name: tr("Save"), exact: true }).click();
      await wed.waitFor({ state: "detached" });
      assert.deepEqual(sent("limit-key").map((e) => e.body), [{ key: "work", limit: { period: "total", tokens: 0, cost: 300, cacheReads: false } }]);
      assert.match(await row("work").locator(".amodels.limit").textContent(), new RegExp(`\\$300 · ${inTotal}`));

      // Reset usage says counting starts again, not a window's end
      await row("laptop").getByRole("button", { name: tr("Change this key's limit"), exact: true }).click();
      const ed = row("laptop").locator(".key-limit-ed");
      await ed.waitFor();
      assert.equal((await ed.getByRole("button", { name: tr("Limit window"), exact: true }).textContent()).trim(), inTotal);
      await ed.getByRole("button", { name: tr("Reset usage"), exact: true }).click();
      const modal = page.locator("#modal");
      await modal.getByText(tr("Reset {name}'s usage?", { name: "Laptop" })).waitFor();
      assert((await modal.textContent()).includes(tr("The tokens and cost {name} has used go back to 0 together, and counting starts again from now.", { name: "Laptop" })));
      await modal.getByRole("button", { name: tr("Reset usage"), exact: true }).click();
      await modal.waitFor({ state: "hidden" });
      assert.deepEqual(sent("reset-limit-key").map((e) => e.body), [{ key: "laptop" }]);
      assert.deepEqual(errors, []);
    });
  }
}
