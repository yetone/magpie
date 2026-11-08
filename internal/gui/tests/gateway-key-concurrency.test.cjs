// A gateway key's requests at once (young02036 on Discord), set as an
// account's are on the Providers page: a badge on the key's row, shown
// always while set and on hover while not, opens the app's menu (no
// native select): no limit, a number, or Other… typed in place. It sends
// "concurrency-key"; a value that isn't a whole number up to 1000 is
// refused and sends nothing.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

const words = {
  en: { label: "Requests at once", none: "No limit", off: "No limit at once", at: (n) => `${n} at once`, other: "Other…" },
  zh: { label: "同时请求数", none: "不限制", off: "并发不限", at: (n) => `并发 ${n}`, other: "其他…" },
  "zh-TW": { label: "同時請求數", none: "不限制", off: "並行不限", at: (n) => `並行 ${n}`, other: "其他…" },
  ja: { label: "同時リクエスト数", none: "上限なし", off: "同時数 無制限", at: (n) => `同時 ${n}`, other: "その他…" },
  de: { label: "Gleichzeitige Anfragen", none: "Kein Limit", off: "Gleichzeitig unbegrenzt", at: (n) => `${n} gleichzeitig`, other: "Andere…" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const [lang, w] of Object.entries(words)) {
      test(`${engine} ${width} ${lang}: a key's requests at once are set from the app's menu`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const events = [], errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", fixture(lang, "light", events, { lan: true, keyHolds: { server: { maxConcurrency: 3 }, laptop: { readOnly: true } } }));
        await page.goto("http://magpie.test/?view=gateway");
        await page.locator("#gatewayKeys .acc[data-key]").last().waitFor();
        const row = (id) => page.locator(`#gatewayKeys .acc[data-key="${id}"]`);
        const badge = (id) => row(id).locator(".key-conc");
        const item = (name) => page.locator(".proto-menu .pm-item", { has: page.locator(".pm-name", { hasText: new RegExp(`^${name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`) }) });
        const sent = () => events.filter((e) => e.action === "concurrency-key").map((e) => e.body);
        const reads = async (id, text) => page.waitForFunction(([id, text]) => document.querySelector(`#gatewayKeys .acc[data-key="${id}"] .key-conc`)?.textContent === text, [id, text]);
        const view = page.locator("#view-gateway");
        const scrolled = async () => [await view.evaluate((v) => v.scrollTop), await page.evaluate(() => window.scrollY)];
        const before = await scrolled();

        // set: said always, not only on hover; a read-only key has none
        await page.mouse.move(0, 0);
        assert.equal(await badge("server").textContent(), w.at(3));
        assert.equal(await badge("server").evaluate((b) => getComputedStyle(b).opacity), "1");
        assert.equal(await badge("server").getAttribute("aria-label"), w.label);
        assert.equal(await badge("laptop").count(), 0, "a read-only key uses no model");
        // unset: on hover only
        assert.equal(await badge("work").evaluate((b) => getComputedStyle(b).opacity), "0");
        await row("work").hover();
        assert.equal(await badge("work").textContent(), w.off);

        // a number from the menu
        await badge("work").click();
        await page.locator(".proto-menu").waitFor();
        assert.equal(await page.locator(".proto-menu .pm-head").textContent(), w.label);
        await item("2").click();
        await reads("work", w.at(2));
        assert.deepEqual(sent(), [{ key: "work", maxConcurrency: 2 }]);

        // Other…: a bad value is refused and sends nothing, a good one is kept
        await row("server").hover();
        await badge("server").click();
        await item(w.other).click();
        let box = row("server").locator("input.alane-in");
        await box.fill("2000");
        await box.press("Enter");
        await reads("server", w.at(3));
        assert.equal(sent().length, 1, "a value over 1000 was sent");
        await row("server").hover();
        await badge("server").click();
        await item(w.other).click();
        box = row("server").locator("input.alane-in");
        await box.fill("7");
        await box.press("Enter");
        await reads("server", w.at(7));
        assert.deepEqual(sent().at(-1), { key: "server", maxConcurrency: 7 });
        // 7 is offered in the menu now, and No limit turns it off
        await row("server").hover();
        await badge("server").click();
        assert.equal(await item("7").count(), 1);
        await item(w.none).click();
        await reads("server", w.off);
        assert.deepEqual(sent().at(-1), { key: "server", maxConcurrency: 0 });

        assert.deepEqual(await scrolled(), before, "a click moved the page");
        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert.ok(over <= 0, `the page scrolls sideways by ${over}px`);
        await row("work").hover();
        const rowBox = await row("work").boundingBox(), b = await badge("work").boundingBox();
        assert.ok(b.x >= rowBox.x && b.x + b.width <= rowBox.x + rowBox.width + 1, "the badge runs out of its row");
        assert.equal(await page.locator("select").count(), 0, "no native select");
        const lefts = await page.locator("#gatewayKeys .key-conc").evaluateAll((els) => els.map((e) => getComputedStyle(e).borderLeftStyle));
        assert(lefts.every((s) => s === "none"), "a coloured left border");
        assert.deepEqual(errors, []);
      });
    }
  }
}
