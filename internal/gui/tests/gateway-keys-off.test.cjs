const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

// Gateway keys are left out of Gateway while magpie isn't shared, and a
// tunnel to loopback (Cloudflare Tunnel) is refused until it is. DD on
// Discord looked for the keys to put a tunnel behind one and found none:
// the page says where they come from, and sharing is switched on right
// there, on the gateway's card. Shared, the line gives way to the keys.
// So it does with a web page listed in Settings › Network: such a page is
// refused without a key, shared or not, and the refusal sends the user
// here for one (the scattered settings audit after netube99 on X found
// the keys hidden from them).
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const locale of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${locale} ${width}: Gateway says where gateway keys come from while not shared`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        await page.addInitScript(() => { try { localStorage.setItem("magpie.gwConnectFolded", "0"); } catch {} }); // Connect opened, as a user who reads it
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", fixture(locale, "dark", [], { keys: [] }));
        await page.goto("http://magpie.test/?view=gateway");
        await page.locator("#connect .snip").waitFor();

        const hint = page.locator("#gatewayKeysOff");
        await hint.waitFor({ state: "visible" });
        assert.equal(await page.locator("#gatewayKeysBlock").isVisible(), false);
        const text = await hint.textContent();
        assert(text.includes("Cloudflare Tunnel"), text);
        if (locale !== "en") assert(!text.includes("other computers"), "translated: " + text);
        // the line fits the page
        const fits = await hint.evaluate((p) => p.getBoundingClientRect().right <= document.documentElement.clientWidth + 1);
        assert(fits, "the hint overflows at " + width);

        // shared from the card: the keys show and the line is gone
        const y = await page.evaluate(() => scrollY);
        await page.locator("#gatewayLan .segs button").last().click();
        await page.locator("#gatewayKeysBlock").waitFor({ state: "visible" });
        assert.equal(await hint.isVisible(), false);
        assert.equal(y, await page.evaluate(() => scrollY), "sharing moved the page");
        assert.deepEqual(errors, []);
      });
    }
  }
}

const { strings } = require("./strings.cjs");
const { I18N } = strings();
const tr = (lang, w) => (lang === "en" ? w : I18N[lang]?.[w] ?? w);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const locale of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${locale} ${width}: Gateway shows its keys, not shared, for a listed web page`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const open = async (options, settings, at = "?view=gateway") => {
          const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(6000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [], events = [];
          page.on("pageerror", (e) => errors.push(e.message));
          const base = fixture(locale, "light", events, options);
          await page.route("**/*", async (route) => {
            if (settings && new URL(route.request().url()).pathname === "/api/settings") {
              return route.fulfill({ json: { lang: locale, theme: "light", lan: false, lanURLs: [], fx: { rate: 7.2, at: new Date().toISOString() }, ...settings } });
            }
            return base(route);
          });
          await page.goto("http://magpie.test/" + at);
          return { page, errors, events };
        };
        const shown = async (page) => {
          await page.locator("#gatewayKeysBlock").waitFor({ state: "visible" });
          assert.equal(await page.locator("#gatewayKeysOff").isVisible(), false);
        };

        // keys made while shared, no web page: away with sharing, as before
        {
          const { page, errors } = await open({});
          await page.locator("#gatewayKeysOff").waitFor({ state: "visible" });
          assert.equal(await page.locator("#gatewayKeysBlock").isVisible(), false);
          assert.deepEqual(errors, []);
        }
        // no keys yet, a web page listed: the block, and a key is added there
        {
          const { page, errors, events } = await open({ keys: [] }, { corsOrigins: ["http://localhost:3000"] });
          await shown(page);
          await page.locator("#addGatewayKey").click();
          await page.getByRole("button", { name: tr(locale, "Create"), exact: true }).click();
          await page.waitForFunction(() => document.querySelector("#gatewayKeys .adding .primary")?.disabled === false);
          const name = page.getByRole("textbox", { name: tr(locale, "Gateway key name"), exact: true });
          await name.fill("Web app");
          await name.press("Enter");
          await page.locator("#gatewayKeys .acc[data-key]", { hasText: "Web app" }).waitFor();
          assert.ok(events.some((e) => e.action === "add-key" && e.body.name === "Web app"), JSON.stringify(events));
          assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1), "fits");
          assert.deepEqual(errors, []);
        }
        // Settings › Web pages leads to them
        {
          const { page, errors } = await open({ keys: [] }, { corsOrigins: ["http://localhost:3000"] }, "?view=settings&tab=network");
          const go = page.locator("#corsList .rule-row .feat-go button");
          await go.waitFor();
          assert.equal((await go.textContent()).trim(), tr(locale, "Gateway keys"));
          await go.click();
          await shown(page);
          assert.deepEqual(errors, []);
        }
      });
    }
  }
}
