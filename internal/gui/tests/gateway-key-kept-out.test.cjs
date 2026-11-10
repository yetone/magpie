// Run with Node's test runner and Playwright on the module path; see README.md.
// A gateway key may keep some accounts out instead of naming the ones it
// uses (LeiWang_2000 on X), so an account signed in later is used too:
// picked in the app's own menu from the key row's menu, sent as
// "accounts-key" with "<provider>/!<account>" entries when the menu
// closes, shown in a badge that opens the same menu; an account the key
// was held to and is now kept out leaves the held ones, and "No account
// kept out" takes them all off. In every language, Chromium and WebKit,
// wide and narrow.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

const words = {
  en: { item: "Keep accounts out", label: "Accounts this key keeps out", none: "No account kept out", one: "Not relay/spare@example.com", two: "2 accounts kept out", title: /Every account but: relay\/me@example\.com, relay\/spare@example\.com/ },
  zh: { item: "排除账号", label: "这个密钥排除的账号", none: "不排除账号", one: "排除 relay/spare@example.com", two: "排除 2 个账号", title: /除这些账号外都可用：/ },
  "zh-TW": { item: "排除帳號", label: "這個金鑰排除的帳號", none: "不排除帳號", one: "排除 relay/spare@example.com", two: "排除 2 個帳號", title: /除這些帳號外都可用：/ },
  ja: { item: "アカウントを除外", label: "このキーが除外するアカウント", none: "除外なし", one: "relay/spare@example.com を除外", two: "2 件のアカウントを除外", title: /これら以外のすべてのアカウント：/ },
  de: { item: "Konten ausschließen", label: "Konten, die dieser Schlüssel ausschließt", none: "Kein Konto ausgeschlossen", one: "Ohne relay/spare@example.com", two: "2 Konten ausgeschlossen", title: /Alle Konten außer:/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 440]) {
    for (const [lang, w] of Object.entries(words)) {
      test(`${engine} ${width} ${lang}: a gateway key keeps out the accounts picked, and uses every other`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const events = [], errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", fixture(lang, "light", events, { lan: true, keyHolds: { server: { accounts: ["relay/me@example.com"] } } }));
        await page.goto("http://magpie.test/?view=gateway");
        await page.locator("#gatewayKeys .acc[data-key]").last().waitFor();
        const row = (id) => page.locator(`#gatewayKeys .acc[data-key="${id}"]`);
        const badge = (id) => row(id).getByRole("button", { name: w.label, exact: true });
        const sent = () => events.filter((e) => e.action === "accounts-key").map((e) => e.body);
        const until = async (fn) => {
          for (let i = 0; i < 150 && !fn(); i++) await new Promise((r) => setTimeout(r, 20));
        };
        const view = page.locator("#view-gateway");
        const scrolled = async () => [await view.evaluate((v) => v.scrollTop), await page.evaluate(() => window.scrollY)];
        const at = await scrolled();
        // a key that keeps none out has no badge; the row's menu picks
        await page.mouse.move(0, 0);
        assert.equal(await row("work").locator(".key-out").count(), 0);
        await row("work").hover();
        await row("work").locator(".gw-key-more").click();
        await page.locator(".row-menu").getByRole("menuitem", { name: w.item, exact: true }).click();
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        assert.equal(await page.locator("select").count(), 0, "no native select");
        assert.equal((await menu.locator(".pm-head").textContent()).trim(), w.label);
        assert.deepEqual((await menu.locator(".pm-item.on").allTextContents()).map((s) => s.trim()), [w.none]);
        const box = await menu.boundingBox();
        assert(box.x >= 0 && box.x + box.width <= width + 1, "the menu inside the window");
        await menu.locator(".pm-item", { hasText: "spare@example.com" }).click();
        assert.equal(sent().length, 0, "a pick was sent before the menu closed");
        await page.keyboard.press("Escape");
        await menu.waitFor({ state: "detached" });
        await until(() => sent().length);
        assert.deepEqual(sent(), [{ key: "work", accounts: ["relay/!spare@example.com"] }]);
        await page.waitForFunction(() => document.querySelector('#gatewayKeys .acc[data-key="work"] .key-out'));
        assert.equal((await badge("work").textContent()).trim(), w.one);
        // kept out is not held to: the models badge still says every model
        assert.equal(await row("work").locator(".key-models.set").count(), 0, "keeping one out held the key's models");

        // the badge opens the same menu, the one kept out ticked; a second
        events.length = 0;
        await badge("work").click();
        await menu.waitFor();
        assert.equal(await menu.locator(".pm-item.on").count(), 1);
        await menu.locator(".pm-item", { hasText: "me@example.com" }).first().click();
        await page.keyboard.press("Escape");
        await menu.waitFor({ state: "detached" });
        await until(() => sent().length);
        assert.deepEqual(sent(), [{ key: "work", accounts: ["relay/!me@example.com", "relay/!spare@example.com"] }]);
        await page.waitForFunction((two) => document.querySelector('#gatewayKeys .acc[data-key="work"] .key-out')?.textContent.trim() === two, w.two);
        assert.match(await badge("work").getAttribute("title"), w.title);

        // a key held to an account and now keeping it out uses it no more
        events.length = 0;
        await row("server").hover();
        await row("server").locator(".gw-key-more").click();
        await page.locator(".row-menu").getByRole("menuitem", { name: w.item, exact: true }).click();
        await menu.waitFor();
        await menu.locator(".pm-item", { hasText: "me@example.com" }).first().click();
        await page.keyboard.press("Escape");
        await menu.waitFor({ state: "detached" });
        await until(() => sent().length);
        assert.deepEqual(sent(), [{ key: "server", accounts: ["relay/!me@example.com"] }]);

        // "No account kept out" takes them all off
        events.length = 0;
        await badge("work").click();
        await menu.waitFor();
        await menu.locator(".pm-item", { hasText: w.none }).click();
        await menu.waitFor({ state: "detached" });
        await until(() => sent().length);
        assert.deepEqual(sent(), [{ key: "work", accounts: [] }]);
        await page.waitForFunction(() => !document.querySelector('#gatewayKeys .acc[data-key="work"] .key-out'));

        assert.deepEqual(await scrolled(), at, "a click moved the page");
        assert(!(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth)), "the page scrolls sideways");
        const lefts = await page.locator("#gatewayKeys .key-out").evaluateAll((els) => els.map((e) => getComputedStyle(e).borderLeftStyle));
        assert(lefts.every((s) => s === "none"), "a coloured left border");
        assert.deepEqual(errors, []);
      });
    }
  }
}
