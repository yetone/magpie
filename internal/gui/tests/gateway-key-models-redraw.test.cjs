// Run with Node's test runner and Playwright on the module path; see README.md.
// A gateway key's models menu that is still loading when the key list is
// drawn again (a rename's reply, the providers' poll, coming back to the
// window): the click isn't dropped. The menu comes up at the redrawn badge,
// which is marked open, and what is picked is sent for that key.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1000, 560]) {
    test(`${engine} ${width}px: a key's models menu loading while the keys are drawn again`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(6000);
      const events = [], errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", fixture("en", "light", events, { lan: true }));
      // the models the menu lists, slow to come
      await page.route("**/api/caller-keys/models", async (route) => {
        await new Promise((r) => setTimeout(r, 500));
        await route.fallback();
      });
      await page.goto("http://magpie.test/?view=gateway");
      const badge = page.locator('#gatewayKeys .acc[data-key="work"] .key-models');
      await badge.waitFor();
      const menu = page.locator(".proto-menu");

      await badge.click();
      await page.evaluate(() => renderGatewayKeys());
      await menu.waitFor({ timeout: 2000 }).catch(() => assert.fail("the redraw dropped the click"));
      assert.equal(await badge.getAttribute("aria-expanded"), "true", "the redrawn badge isn't marked open");
      const b = await badge.boundingBox(), m = await menu.boundingBox();
      const up = await menu.evaluate((e) => e.classList.contains("up"));
      const gap = up ? b.y - (m.y + m.height) : m.y - (b.y + b.height);
      assert(Math.abs(gap - 5) <= 2, `the menu is ${gap}px from its badge`);

      // a pick is sent for that key as the menu closes
      await menu.locator(".pm-item", { hasText: "GPT-5" }).click();
      await page.keyboard.press("Escape");
      await menu.waitFor({ state: "detached" });
      await page.waitForFunction(() => document.querySelector('#gatewayKeys .acc[data-key="work"] .key-models')?.textContent.trim() === "openai/gpt-5");
      const sent = events.filter((e) => e.action === "models-key");
      assert.deepEqual(sent.map((e) => e.body), [{ key: "work", models: ["openai/gpt-5"] }]);
      assert.equal(await badge.getAttribute("aria-expanded"), "false");
      assert.deepEqual(errors, []);
    });
  }
}
