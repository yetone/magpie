// A gateway key's limit on some models (zola_xynb on X): the limit
// editor's Models picks them in the app's menu, every model of a provider
// at once or one by one, and none picked is the whole key, as a limit set
// before there was a pick. The badge, the line and the tabs name what a
// limit counts. In every language, both engines, wide and narrow.
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
    limit: { period: "month", cost: 20, models: ["openai/*"] },
    used: { period: "month", start: new Date(Date.now() - 3 * day).toISOString(), reset: new Date(Date.now() + 20 * day).toISOString(), calls: 8,
      tokens: 300000, cost: 4, costLimit: 20, tokensLeft: 0, costLeft: 16, spent: false, models: ["openai/*"] },
  },
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: a key's limit on some models`, async (t) => {
        const tr = (k, vars = {}) => {
          const s = lang === "en" ? k : I18N[lang][k];
          assert.equal(typeof s, "string", `${lang} has ${k}`);
          return s.replace(/\{(\w+)\}/g, (_, v) => String(vars[v]));
        };
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const events = [], errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", fixture(lang, "light", events, { lan: true, limits: limits(), groups: true }));
        await page.goto("http://magpie.test/?view=gateway");
        await page.locator("#gatewayKeys .acc[data-key]").last().waitFor();
        const row = (id) => page.locator(`#gatewayKeys .acc[data-key="${id}"]`);
        const sent = (action) => events.filter((e) => e.action === action);
        const fits = async (what) => assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth), false, what + " scrolls sideways");
        const tap = async (loc) => {
          await page.mouse.move(width / 2, 400);
          for (let i = 0; i < 20 && !(await loc.evaluate((e) => { const r = e.getBoundingClientRect(); return r.top >= 0 && r.bottom <= (document.querySelector("footer.foot")?.getBoundingClientRect().top ?? innerHeight); })); i++) {
            await page.mouse.wheel(0, 120);
            await page.waitForTimeout(50);
          }
          await loc.click();
        };

        // the badge and the line name the models the limit counts
        const badge = row("laptop").locator(".amodels.limit");
        assert((await badge.textContent()).includes("openai/*"), "badge says " + await badge.textContent());
        const line = row("laptop").locator(".key-limit");
        assert((await line.textContent()).startsWith("openai/*"), "line says " + await line.textContent());
        await fits("the rows");

        // the editor's Models, the app's menu, never a native select
        await badge.click();
        const ed = row("laptop").locator(".key-limit-ed");
        await ed.waitFor();
        assert.equal(await ed.locator("select").count(), 0);
        const pick = ed.getByRole("button", { name: tr("Models this limit counts"), exact: true });
        assert.equal((await pick.textContent()).trim(), "openai/*");
        assert.equal(await pick.evaluate((e) => getComputedStyle(e).borderLeftStyle === getComputedStyle(e).borderRightStyle), true);
        await fits("the editor");
        await tap(pick);
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        assert.equal((await menu.locator(".pm-head").textContent()).trim(), tr("Models this limit counts"));
        // a routing group isn't offered: a call is counted by the model that served it
        assert.equal(await menu.getByText("group/coding", { exact: true }).count(), 0);
        const item = (name) => menu.locator(".pm-item").filter({ has: page.getByText(name, { exact: true }) });
        assert.equal(await item(tr("Every {provider} model", { provider: "OpenAI" })).getAttribute("aria-checked"), "true");
        // one Relay model more, and the window as it was
        await item("relay/m-mini").click();
        await page.keyboard.press("Escape");
        await menu.waitFor({ state: "detached" });
        assert.equal((await pick.textContent()).trim(), tr("{n} models", { n: 2 }));
        assert.equal(await ed.locator(".munsaved").isHidden(), false, "a pick is unsaved until Save");
        await tap(ed.getByRole("button", { name: tr("Save"), exact: true }));
        await ed.waitFor({ state: "detached" });
        assert.deepEqual(sent("limit-key").map((e) => e.body), [{ key: "laptop", limit: { period: "month", tokens: 0, cost: 20, cacheReads: false, models: ["relay/m-mini", "openai/*"] } }]);
        assert((await badge.textContent()).includes(tr("{n} models", { n: 2 })));

        // All models: the whole key again, sent with no models
        await badge.click();
        await ed.waitFor();
        await tap(pick);
        await menu.waitFor();
        await item(tr("All models")).click();
        await menu.waitFor({ state: "detached" });
        assert.equal((await pick.textContent()).trim(), tr("All models"));
        await tap(ed.getByRole("button", { name: tr("Save"), exact: true }));
        await ed.waitFor({ state: "detached" });
        assert.deepEqual(sent("limit-key").at(-1).body, { key: "laptop", limit: { period: "month", tokens: 0, cost: 20, cacheReads: false } });
        assert.equal((await badge.textContent()).includes("openai/*"), false);
        assert.equal((await line.textContent()).includes(tr("{n} models", { n: 2 })), false);
        assert.deepEqual(errors, []);
      });
    }
  }
}
