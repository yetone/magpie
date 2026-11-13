const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

// magpie draws both themes itself, and says so: a browser that repaints
// light pages dark (Chrome's Auto Dark Theme, Safari's Adaptive Dark Mode)
// skips a page whose root takes dark too, and leaves an element that is
// light only alone (xjp on X). The page under the root takes the theme
// shown, so Settings' Light stays light on a dark system.
const words = (v) => v.trim().split(/\s+/).sort().join(" ");
const want = { system: "dark light", light: "light only", dark: "dark" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const theme of ["system", "light", "dark"]) {
    test(`${engine} ${theme}: the page declares both themes, the body the one shown`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const context = await browser.newContext({ viewport: { width: 1000, height: 700 }, reducedMotion: "reduce", colorScheme: "dark" });
      const page = await context.newPage();
      page.setDefaultTimeout(6000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", fixture("en", theme, []));
      await page.goto("http://magpie.test/?view=gateway" + (theme === "system" ? "" : "&theme=" + theme));
      await page.locator("#gatewayKeys").waitFor({ state: "attached" });
      const got = await page.evaluate(() => ({
        meta: document.querySelector('meta[name="color-scheme"]')?.content || "",
        root: getComputedStyle(document.documentElement).colorScheme,
        body: getComputedStyle(document.body).colorScheme,
        theme: document.documentElement.dataset.theme || "system",
      }));
      assert.equal(got.theme, theme);
      assert.equal(words(got.meta), "dark light");
      assert.equal(words(got.root), "dark light");
      assert.equal(words(got.body), want[theme]);
      assert.deepEqual(errors, []);
    });
  }
}
