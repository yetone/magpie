const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");
const { openPart } = require("./reader.cjs");

// Settings › About's Report a problem copies the version, the system and
// the architecture, says so, then opens the feedback post on X. What it
// copied is never in the link (River_WLan on X).
const words = {
  en: ["Report a problem", "Details copied. Paste them in your reply"],
  zh: ["反馈问题", "信息已复制，请粘贴到你的回复里"],
  "zh-TW": ["回報問題", "資訊已複製，請貼到你的回覆裡"],
  ja: ["問題を報告", "情報をコピーしました。返信に貼り付けてください"],
  de: ["Problem melden", "Angaben kopiert. Fügen Sie sie in Ihre Antwort ein"],
};
const block = "magpie 0.1.9\nOS: macOS 26.2\nArch: arm64\nOpened in: app";
const post = "https://x.com/magpie_router/status/2119861020978188499";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: About reports a problem`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const events = [];
        await page.route("**/*", fixture(lang, "light", events));
        await page.route("**/api/feedback*", (r) => r.fulfill({ json: { text: block, url: post } }));
        await page.route("**/api/open", (r) => { events.push({ action: "open", body: r.request().postDataJSON() }); return r.fulfill({ status: 204 }); });
        await page.goto("http://magpie.test/");
        await page.locator("#prefs").click();
        await openPart(page, "about");
        const row = page.locator("#about .row", { has: page.locator(".who", { hasText: words[lang][0] }) });
        await row.waitFor();
        await row.locator("button.text").click();
        await page.locator("#status", { hasText: words[lang][1] }).waitFor();
        assert.equal(events.find((e) => e.action === "clipboard").body.text, block);
        const opened = events.find((e) => e.action === "open");
        assert.equal(opened.body.url, post);
        assert.equal(opened.body.url.includes(encodeURIComponent("magpie 0.1.9")), false);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "About wider than the window");
        assert.deepEqual(errors, []);
      });
    }
  }
}
