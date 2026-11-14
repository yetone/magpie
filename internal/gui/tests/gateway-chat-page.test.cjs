const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

// The gateway card opens DeepSeek's chat page, which the gateway serves
// with magpie's models (@yanghonghang on X): at the gateway's own address,
// and in the browser GUI at the address it is shared on.
const WORDS = { en: "Chat page", zh: "聊天页面", "zh-TW": "聊天頁面", ja: "チャットページ", de: "Chat-Seite" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const locale of Object.keys(WORDS)) {
    for (const width of [1100, 420]) {
      test(`${engine} ${locale} ${width}: the gateway card opens the chat page`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${locale}-${width}-gateway-chat-page.png`), fullPage: true });
          }
          await browser.close();
        });
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const opened = [];
        await page.route("**/*", fixture(locale, "dark", []));
        await page.route("**/api/open", (route) => { opened.push(route.request().postDataJSON().url); return route.fulfill({ status: 204 }); });
        await page.goto("http://magpie.test/?view=gateway");
        const chat = page.locator("#gateway .gw-chat");
        await chat.waitFor();
        assert.equal((await chat.textContent()).trim(), WORDS[locale]);
        assert.equal(await chat.textContent(), await page.evaluate(() => t("Chat page")));
        // on the card, on one line with Restart gateway, not past its edge
        const fit = await page.locator("#gateway").evaluate((card) => {
          const c = card.getBoundingClientRect(), chat = card.querySelector(".gw-chat"), fix = chat.nextElementSibling;
          const b = chat.getBoundingClientRect(), f = fix?.getBoundingClientRect();
          return { inside: b.left >= c.left && f.right <= c.right + 0.5, fix: fix?.classList.contains("gw-fix"), line: Math.abs(b.top - f.top) < 1,
            first: card.querySelector(".gw-fix") === fix };
        });
        assert(fit.inside, "the buttons stay on the card");
        assert(fit.fix && fit.line, "Restart gateway follows it on its line");
        assert(fit.first, "the card's first .gw-fix is still Restart gateway");
        await chat.click();
        await assert.doesNotReject(async () => { for (let i = 0; i < 50 && !opened.length; i++) await page.waitForTimeout(20); });
        assert.deepEqual(opened, ["http://127.0.0.1:3999/deepseek-chat/"]);
        assert.deepEqual(errors, []);
      });
    }
  }

  test(`${engine}: in the browser GUI, shared, the chat page opens at the shared address`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const page = await (await browser.newContext({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" })).newPage();
    t.after(() => browser.close());
    page.setDefaultTimeout(6000);
    await page.route("**/*", fixture("en", "light", [], { web: true, lan: true, publicURL: "https://magpie.example.com" }));
    await page.addInitScript(() => { window.__opened = []; window.open = (u) => { window.__opened.push(u); return null; }; });
    await page.goto("http://magpie.test/?view=gateway");
    await page.locator("#gateway .gw-chat").click();
    await page.waitForFunction(() => window.__opened.length > 0);
    assert.deepEqual(await page.evaluate(() => window.__opened), ["https://magpie.example.com/deepseek-chat/"]);
  });
}
