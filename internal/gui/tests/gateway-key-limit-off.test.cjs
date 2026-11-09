// Run with Node's test runner and Playwright on the module path; see README.md.
// A gateway key's limit is the Key limits built-in's (budget). Switched off,
// Save was refused with "turn the plugin on" and the editor didn't say where
// (the scattered settings audit after netube99 on X). Off, the editor says
// it is off in Plugins › Built-in and turns it on in place; on, it says
// nothing. Every language, Chromium and WebKit, at 1000 and 420 wide.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");
const { strings } = require("./strings.cjs");

const { I18N } = strings();
const tr = (lang, s) => (lang === "en" ? s : I18N[lang]?.[s] ?? s);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1000, 420]) {
      test(`${engine} ${lang} ${width}: a key's limit editor says Key limits is off and turns it on`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        let on = false;
        const builtins = () => [{ id: "budget", name: "Key limits", sub: "", on, chosen: true, source: "preset", state: on ? "active" : "off", inUse: 0 }];
        const base = fixture(lang, "light", [], { lan: true });
        await page.route("**/*", async (route) => {
          const req = route.request(), url = new URL(req.url());
          if (url.pathname === "/api/plugins/builtin" && req.method() === "POST") {
            const b = req.postDataJSON();
            posts.push(b);
            on = b.on;
            return route.fulfill({ json: builtins() });
          }
          if (url.pathname === "/api/settings" && req.method() === "GET") {
            // the fixture's own answer, with the built-ins added
            let got = {};
            await base({ request: () => req, fulfill: async (o) => { got = o.json; } });
            return route.fulfill({ json: { ...got, builtins: builtins() } });
          }
          return base(route);
        });
        await page.goto("http://magpie.test/?view=gateway");
        const row = page.locator('#gatewayKeys .acc[data-key="work"]');
        await row.waitFor();
        await row.getByRole("button", { name: tr(lang, "Set a limit for this key"), exact: true }).click();
        const ed = row.locator(".key-limit-ed");
        await ed.waitFor();
        const off = ed.locator(".klf-off");
        await off.waitFor();
        assert.equal((await off.locator("span").textContent()).trim(), tr(lang, "Off in Plugins › Built-in"));
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1), "fits");
        const ob = await off.boundingBox(), eb = await ed.boundingBox();
        assert.ok(ob.x >= eb.x - 1 && ob.x + ob.width <= eb.x + eb.width + 1, "inside the editor");
        await off.getByRole("button", { name: tr(lang, "Turn on"), exact: true }).click();
        await off.waitFor({ state: "hidden" });
        assert.deepEqual(posts, [{ id: "budget", on: true }]);
        assert.equal(await ed.isVisible(), true, "the editor stays open");
        // on: nothing said
        await ed.getByRole("button", { name: tr(lang, "Cancel"), exact: true }).click();
        await ed.waitFor({ state: "detached" });
        await row.getByRole("button", { name: tr(lang, "Set a limit for this key"), exact: true }).click();
        await ed.waitFor();
        assert.equal(await ed.locator(".klf-off").isVisible(), false, "on, the line is hidden");
        assert.deepEqual(errors, []);
      });
    }
  }
}
