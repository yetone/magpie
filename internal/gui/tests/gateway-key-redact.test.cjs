// A gateway key's masking (y0ngb1n on X): a badge on the key's row, shown
// always while set and on hover while not, opens the app's menu (no native
// select): as Settings say, On or Off. It sends "redact-key"; a read-only
// key has none.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

const words = {
  en: { label: "Masking", follow: "Follow Settings", on: "On", off: "Off", badgeOn: "Masking on", badgeOff: "Masking off" },
  zh: { label: "脱敏", follow: "跟随设置", on: "开启", off: "关闭", badgeOn: "脱敏：开", badgeOff: "脱敏：关" },
  "zh-TW": { label: "脫敏", follow: "跟隨設定", on: "開啟", off: "關閉", badgeOn: "脫敏：開", badgeOff: "脫敏：關" },
  ja: { label: "マスク", follow: "設定に従う", on: "オン", off: "オフ", badgeOn: "マスク オン", badgeOff: "マスク オフ" },
  de: { label: "Maskierung", follow: "Wie in den Einstellungen", on: "An", off: "Aus", badgeOn: "Maskierung an", badgeOff: "Maskierung aus" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const [lang, w] of Object.entries(words)) {
      test(`${engine} ${width} ${lang}: a key's masking is set from the app's menu`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const events = [], errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", fixture(lang, "light", events, { lan: true, keyHolds: { server: { redact: "off" }, laptop: { readOnly: true } } }));
        await page.goto("http://magpie.test/?view=gateway");
        await page.locator("#gatewayKeys .acc[data-key]").last().waitFor();
        const row = (id) => page.locator(`#gatewayKeys .acc[data-key="${id}"]`);
        const badge = (id) => row(id).locator(".key-redact");
        const item = (name) => page.locator(".proto-menu .pm-item", { has: page.locator(".pm-name", { hasText: new RegExp(`^${name}$`) }) });
        const sent = () => events.filter((e) => e.action === "redact-key").map((e) => e.body);
        const reads = (id, text) => page.waitForFunction(([id, text]) => document.querySelector(`#gatewayKeys .acc[data-key="${id}"] .key-redact`)?.textContent === text, [id, text]);

        // set: said always, not only on hover; a read-only key has none
        await page.mouse.move(0, 0);
        assert.equal(await badge("server").textContent(), w.badgeOff);
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
        await item(w.on).click();
        await reads("work", w.badgeOn);
        assert.deepEqual(sent(), [{ key: "work", redact: "on" }]);

        await row("server").hover();
        await badge("server").click();
        await item(w.follow).click();
        await reads("server", w.label);
        assert.deepEqual(sent().at(-1), { key: "server", redact: "" });

        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert.ok(over <= 0, `the page scrolls sideways by ${over}px`);
        assert.equal(await page.locator("select").count(), 0, "no native select");
        assert.deepEqual(errors, []);
      });
    }
  }
}
