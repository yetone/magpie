// A gateway key's service tier (JackyFill on X): a badge on the key's row,
// shown always while set and on hover while not, opens the app's menu (no
// native select): as the request asks, Standard, Fast or Ultrafast. It
// sends "tier-key"; a read-only key has none.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

const words = {
  en: { label: "Service tier", asks: "As the request asks", fast: "Fast", ultra: "Ultrafast", standard: "Standard" },
  zh: { label: "服务层级", asks: "按请求的指定", fast: "快速", ultra: "极速", standard: "标准" },
  "zh-TW": { label: "服務層級", asks: "依請求的指定", fast: "快速", ultra: "極速", standard: "標準" },
  ja: { label: "サービス階層", asks: "リクエストの指定どおり", fast: "高速", ultra: "超高速", standard: "標準" },
  de: { label: "Service-Stufe", asks: "Wie die Anfrage es verlangt", fast: "Schnell", ultra: "Ultrafast", standard: "Standard" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const [lang, w] of Object.entries(words)) {
      test(`${engine} ${width} ${lang}: a key's service tier is set from the app's menu`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const events = [], errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", fixture(lang, "light", events, { lan: true, keyHolds: { server: { serviceTier: "priority" }, laptop: { readOnly: true } } }));
        await page.goto("http://magpie.test/?view=gateway");
        await page.locator("#gatewayKeys .acc[data-key]").last().waitFor();
        const row = (id) => page.locator(`#gatewayKeys .acc[data-key="${id}"]`);
        const badge = (id) => row(id).locator(".key-tier");
        const item = (name) => page.locator(".proto-menu .pm-item", { has: page.locator(".pm-name", { hasText: new RegExp(`^${name}$`) }) });
        const sent = () => events.filter((e) => e.action === "tier-key").map((e) => e.body);
        const reads = (id, text) => page.waitForFunction(([id, text]) => document.querySelector(`#gatewayKeys .acc[data-key="${id}"] .key-tier`)?.textContent === text, [id, text]);

        // set: said always, not only on hover; a read-only key has none
        await page.mouse.move(0, 0);
        assert.equal(await badge("server").textContent(), w.fast);
        assert.equal(await badge("server").evaluate((b) => getComputedStyle(b).opacity), "1");
        assert.equal(await badge("server").getAttribute("aria-label"), w.label);
        assert.equal(await badge("laptop").count(), 0);
        // unset: on hover only
        assert.equal(await badge("work").evaluate((b) => getComputedStyle(b).opacity), "0");
        await row("work").hover();
        assert.equal(await badge("work").textContent(), w.label);

        await badge("work").click();
        await page.locator(".proto-menu").waitFor();
        assert.equal(await page.locator(".proto-menu .pm-head").textContent(), w.label);
        await item(w.ultra).click();
        await reads("work", w.ultra);
        assert.deepEqual(sent(), [{ key: "work", serviceTier: "ultrafast" }]);

        await row("server").hover();
        await badge("server").click();
        await item(w.asks).click();
        await reads("server", w.label);
        assert.deepEqual(sent().at(-1), { key: "server", serviceTier: "" });

        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert.ok(over <= 0, `the page scrolls sideways by ${over}px`);
        assert.equal(await page.locator("select").count(), 0, "no native select");
        assert.deepEqual(errors, []);
      });
    }
  }
}
