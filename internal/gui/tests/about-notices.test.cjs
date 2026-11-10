const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");
const { openPart } = require("./reader.cjs");

// Every build carries moejs's third-party notices (THIRD_PARTY_NOTICES,
// embedded in the binary); Settings › About's Open-source licenses shows
// them whole, scrolled inside the dialog, in every language and at the tray
// panel's width.
const words = {
  en: "Open-source licenses", zh: "开源许可", "zh-TW": "開源授權", ja: "オープンソースライセンス", de: "Open-Source-Lizenzen",
};
const text = "magpie third-party notices\n" + "x".repeat(300) + "\n" + "line\n".repeat(400) + "4. V8 (ported code)\n";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: About shows the third-party notices`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", fixture(lang, "light", []));
        await page.route("**/api/notices", (r) => r.fulfill({ json: { text } }));
        await page.goto("http://magpie.test/");
        await page.locator("#prefs").click();
        await openPart(page, "about");
        const row = page.locator("#about .row", { has: page.locator(".who", { hasText: words[lang] }) });
        await row.locator("button.text").click();
        const pre = page.locator("#modal .notices-text");
        await pre.waitFor();
        assert.equal(await pre.textContent(), text);
        assert.equal(await page.locator("#modal .ehead b").textContent(), words[lang]);
        // the long text scrolls in the dialog, which stays inside the window
        const fits = await page.evaluate(() => {
          const d = document.querySelector("#modal").firstElementChild.getBoundingClientRect();
          const body = document.querySelector("#modal .ebody");
          return { inside: d.right <= innerWidth && d.bottom <= innerHeight, scrolls: body.scrollHeight > body.clientHeight, wide: document.documentElement.scrollWidth <= innerWidth };
        });
        assert.deepEqual(fits, { inside: true, scrolls: true, wide: true });
        // the text takes the dialog's whole width, not the editor grid's narrow first column
        const span = await page.evaluate(() => {
          const p = document.querySelector("#modal .notices-text").getBoundingClientRect(), b = document.querySelector("#modal .ebody").getBoundingClientRect();
          return p.width / b.width;
        });
        assert.ok(span > 0.85, `the notices take ${Math.round(span * 100)}% of the dialog's width`);
        await page.locator("#modal .bar button.primary").click();
        await page.locator("#modal").waitFor({ state: "hidden" });
        assert.deepEqual(errors, []);
      });
    }
  }
}
