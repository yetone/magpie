// The models a gateway key may use (#882): picked in the app's own menu
// on the key's row, sent when the menu closes, "All models" taking the
// restriction off.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a gateway key is held to the models picked for it`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 420 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(6000);
      page.setDefaultNavigationTimeout(60000);
      const events = [], errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", fixture(lang, "light", events, { lan: true, limits: { server: { models: ["openai/*", "relay/m"] } } }));
      const zh = lang === "zh";
      const w = zh
        ? { label: "此密钥可用的模型", all: "全部模型", two: "2 个模型", relay: "Relay 的全部模型", only: /仅限这些模型/ }
        : { label: "Models this key may use", all: "All models", two: "2 models", relay: "Every Relay model", only: /Only these models/ };
      await page.goto("http://magpie.test/?view=gateway");
      await page.locator("#gatewayKeys .acc[data-key]").last().waitFor();
      const row = (id) => page.locator(`#gatewayKeys .acc[data-key="${id}"]`);
      const badge = (id) => row(id).getByRole("button", { name: w.label, exact: true });
      // a held key says how many it may use, always; a free one "All models"
      assert.equal((await badge("server").textContent()).trim(), w.two);
      assert.match(await badge("server").getAttribute("title"), w.only);
      assert.equal(await badge("server").evaluate((b) => b.classList.contains("set")), true);
      assert.equal((await badge("work").textContent()).trim(), w.all);
      // nothing drawn with a coloured left border
      const lefts = await page.locator("#gatewayKeys .key-models").evaluateAll((els) => els.map((e) => getComputedStyle(e).borderLeftStyle));
      assert(lefts.every((s) => s === "none"), "a badge has a left border");
      const view = page.locator("#view-gateway");
      const scrolled = async () => [await view.evaluate((v) => v.scrollTop), await page.evaluate(() => window.scrollY)];
      await page.mouse.move(500, 300);
      await page.mouse.wheel(0, 120);
      await page.waitForTimeout(100);
      const at = await scrolled();
      // the app's menu, not a native select
      await row("work").hover();
      await badge("work").click();
      const menu = page.locator(".proto-menu");
      await menu.waitFor();
      assert.equal(await page.locator("select").count(), 0, "no native select");
      assert.equal((await menu.locator(".pm-head").textContent()).trim(), w.label);
      assert.deepEqual(await scrolled(), at, "opening the menu moved the page");
      assert.equal(await menu.locator(".pm-item.on").count(), 1, "All models is ticked for a free key");
      // a short window: the menu stays in it, scrolling, rather than running off it
      // (reduced motion still runs a .01ms transition, so it is waited for)
      await page.waitForFunction(() => {
        const r = document.querySelector(".proto-menu").getBoundingClientRect();
        return r.top >= 0 && r.bottom <= innerHeight;
      }, null, { timeout: 2000 }).catch(async () => assert.fail(`the menu runs off the window: ${JSON.stringify(await menu.boundingBox())}`));
      // picks stay in the menu, sent only when it closes
      await menu.locator(".pm-item", { hasText: w.relay }).click();
      await menu.locator(".pm-item", { hasText: "GPT-5" }).click();
      assert.equal(events.filter((e) => e.action === "models-key").length, 0, "a pick was sent before the menu closed");
      assert.equal(await menu.isVisible(), true, "a pick closed the menu");
      await page.keyboard.press("Escape");
      await menu.waitFor({ state: "detached" });
      assert.deepEqual(events.filter((e) => e.action === "models-key").map((e) => e.body), [{ key: "work", models: ["relay/*", "openai/gpt-5"] }]);
      await page.waitForFunction(() => document.querySelector('#gatewayKeys .acc[data-key="work"] .key-models.set'));
      assert.equal((await badge("work").textContent()).trim(), w.two);
      assert.deepEqual(await scrolled(), at, "a pick moved the page");
      // All models takes it off
      await badge("server").click();
      await menu.waitFor();
      assert.equal(await menu.locator(".pm-item.on").count(), 2);
      await menu.locator(".pm-item", { hasText: w.all }).first().click();
      await menu.waitFor({ state: "detached" });
      assert.deepEqual(events.filter((e) => e.action === "models-key").at(-1).body, { key: "server", models: [] });
      await page.waitForFunction(() => !document.querySelector('#gatewayKeys .acc[data-key="server"] .key-models.set'));
      assert.deepEqual(await scrolled(), at, "no click moved the page");
      // narrow: the row fits
      await page.setViewportSize({ width: 560, height: 740 });
      // the nav fits once the resize is handled: wait for it, and a real
      // overflow still fails at the timeout
      await page.waitForFunction(() => document.documentElement.scrollWidth <= window.innerWidth);
      assert.deepEqual(errors, []);
    });
  }
}
