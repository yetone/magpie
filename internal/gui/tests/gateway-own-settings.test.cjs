// Run with Node's test runner and Playwright on the module path; see README.md.
// The Gateway page's own setting, Recent calls at a time, was set in
// Plugins › Built-in, two pages from the calls it is about, and the page
// didn't say where its port and allow list are (the scattered settings
// audit after netube99 on X). It is now under the calls, drawn from the
// plugin's schema and saved as picked, with a row that opens Settings ›
// Network and sharing at the gateway's rows, under a Settings head of its
// own. Every language, Chromium and
// WebKit, at 1100 and 420 wide, nothing past the window's edge.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");
const { strings } = require("./strings.cjs");

const { I18N } = strings();
const tr = (lang, s) => (lang === "en" ? s : I18N[lang]?.[s] ?? s);

const SCHEMA = {
  type: "object",
  properties: { callsPage: { type: "integer", title: "Recent calls at a time", description: "How many of the gateway's recent calls the page shows, and how many more each Show more adds", enum: [10, 20, 50, 100], default: 20 } },
  "x-order": ["callsPage"],
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: the Gateway page has its own settings and leads to Settings`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const config = { callsPage: 20 }, posts = [];
        const gateway = () => ({ id: "gateway", name: "Gateway page", sub: "", on: true, chosen: true, source: "preset", state: "active", inUse: 0,
          setAt: "network #portList", page: "gateway", schema: SCHEMA, config: { ...config } });
        const base = fixture(lang, "light", []);
        await page.route("**/*", async (route) => {
          const req = route.request(), url = new URL(req.url());
          if (url.pathname === "/api/plugins/builtin" && req.method() === "POST") {
            const b = req.postDataJSON();
            posts.push(b);
            Object.assign(config, b.config);
            return route.fulfill({ json: [gateway()] });
          }
          if (url.pathname === "/api/settings") {
            return route.fulfill({ json: { lang, theme: "light", lan: false, lanURLs: [], port: 3999, fx: { rate: 7.2, at: new Date().toISOString() }, builtins: [gateway()] } });
          }
          return base(route);
        });
        await page.goto("http://magpie.test/?view=gateway");
        const own = page.locator("#gwOwn");
        const set = own.locator('[data-setting="callsPage"]');
        await set.waitFor();
        assert.equal((await set.locator(".name").textContent()).trim(), tr(lang, "Recent calls at a time"));
        // under the calls it is about
        assert.ok(await page.evaluate(() => document.querySelector("#activity").compareDocumentPosition(document.querySelector("#gwOwn")) & Node.DOCUMENT_POSITION_FOLLOWING));
        // a section of its own under a Settings head, not a card against the
        // calls' (the owner, 11-09)
        const head = page.locator("#gwOwnHead");
        assert.equal((await head.textContent()).trim(), tr(lang, "Settings"));
        const gap = await page.evaluate(() => document.querySelector("#gwOwnHead").getBoundingClientRect().top - document.querySelector("#activity").getBoundingClientRect().bottom);
        assert.ok(gap >= 10, "a gap above the head: " + gap);
        assert.equal((await set.locator(".proto.pick").textContent()).trim(), "20");
        await set.locator(".proto.pick").click();
        await page.locator(".proto-menu .pm-item", { hasText: "50" }).click();
        await page.waitForFunction(() => document.querySelector('#gwOwn [data-setting="callsPage"] .proto.pick')?.textContent.trim() === "50");
        assert.deepEqual(posts, [{ id: "gateway", config: { callsPage: 50 } }]);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1), "fits");

        const go = own.locator("button.text").last();
        assert.equal((await go.textContent()).trim(), tr(lang, "Settings") + " › " + tr(lang, "Network and sharing"));
        await go.click();
        await page.locator("#view-settings #portList .row.flash").waitFor();
        assert.deepEqual(errors, []);
      });
    }
  }
}
