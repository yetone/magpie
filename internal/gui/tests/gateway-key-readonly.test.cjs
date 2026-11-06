// A read-only gateway key (Steven on Discord: an ESP32 desk display that
// polls quotas and usage and uses no model) is made so from its row's menu,
// says so in its row, and is given full access the same way; the sharing
// key, which magpie gives its own agents, can't be made read-only. Magpie
// Browser's key says it is the browser's and has no access to change.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

const words = {
  en: { browser: "Browser only", more: "Key actions", make: "Make read-only", full: "Give full access", tag: "Read only", made: "Server is read-only", given: "Server has full access", why: "The sharing key is given to agents, so it can't be read-only" },
  zh: { browser: "仅浏览器", more: "密钥操作", make: "设为只读", full: "恢复完整权限", tag: "只读", made: "Server 已设为只读", given: "Server 已恢复完整权限", why: "共享密钥会交给 Agent 使用，不能设为只读" },
  "zh-TW": { browser: "僅瀏覽器", more: "金鑰操作", make: "設為唯讀", full: "恢復完整權限", tag: "唯讀", made: "Server 已設為唯讀", given: "Server 已恢復完整權限", why: "共享金鑰會交給 Agent 使用，不能設為唯讀" },
  ja: { browser: "ブラウザ専用", more: "キーの操作", make: "読み取り専用にする", full: "フルアクセスに戻す", tag: "読み取り専用", made: "Server を読み取り専用にしました", given: "Server をフルアクセスに戻しました", why: "共有キーはエージェントに渡されるため、読み取り専用にはできません" },
  de: { browser: "Nur Browser", more: "Schlüsselaktionen", make: "Nur Lesezugriff geben", full: "Vollen Zugriff geben", tag: "Nur Lesen", made: "Server hat jetzt nur Lesezugriff", given: "Server hat wieder vollen Zugriff", why: "Der Freigabeschlüssel wird an Agenten gegeben und kann daher nicht auf Nur Lesen gestellt werden" },
};

const keys = () => [
  { id: "laptop", name: "Laptop", masked: "sk-magpie-key-…111111", readOnly: true },
  { id: "server", name: "Server", masked: "sk-magpie-key-…222222" },
  { id: "magpie", name: "Magpie", masked: "sk-magpie-key-…444444", lan: true },
  // the key the Magpie Browser extension's zip was given
  { id: "ext", name: "Magpie Browser", masked: "sk-magpie-key-…555555", readOnly: true, browser: true },
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const [lang, w] of Object.entries(words)) {
      test(`${engine} ${width} ${lang}: a gateway key is made read-only and given full access again`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const events = [], errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", fixture(lang, "light", events, { lan: true, keys: keys() }));
        await page.goto("http://magpie.test/?view=gateway");
        await page.locator("#gatewayKeys .acc[data-key]").last().waitFor();
        const row = (id) => page.locator(`#gatewayKeys .acc[data-key="${id}"]`);
        const tag = (id) => row(id).locator(".key-readonly");
        const item = (name) => page.locator(".row-menu [role=menuitem]").filter({ hasText: name });
        const menu = async (id) => {
          await row(id).hover();
          await row(id).getByRole("button", { name: w.more, exact: true }).click();
          await page.locator(".row-menu").waitFor();
        };
        // read-only says so always, not only on hover; a full key says nothing
        await page.mouse.move(0, 0);
        assert.equal((await tag("laptop").textContent()).trim(), w.tag);
        assert.ok((await tag("laptop").boundingBox()).width > 0, "the read-only tag is hidden");
        assert.ok((await tag("laptop").getAttribute("title")).length > 10, "the tag says what read-only means");
        assert.equal(await tag("server").count(), 0);

        const view = page.locator("#view-gateway");
        const scrolled = async () => [await view.evaluate((v) => v.scrollTop), await page.evaluate(() => window.scrollY)];
        const before = await scrolled();
        await menu("server");
        assert.equal(await item(w.full).count(), 0);
        await item(w.make).click();
        await page.waitForFunction(() => !!document.querySelector('#gatewayKeys .acc[data-key="server"] .key-readonly'));
        assert.deepEqual(events.filter((e) => e.action === "access-key").map((e) => e.body), [{ key: "server", readOnly: true }]);
        assert.equal((await page.locator("#status").textContent()).trim(), w.made);
        assert.equal((await tag("server").textContent()).trim(), w.tag);

        await menu("server");
        assert.equal(await item(w.make).count(), 0);
        await item(w.full).click();
        await page.waitForFunction(() => !document.querySelector('#gatewayKeys .acc[data-key="server"] .key-readonly'));
        assert.deepEqual(events.filter((e) => e.action === "access-key").at(-1).body, { key: "server", readOnly: false });
        assert.equal((await page.locator("#status").textContent()).trim(), w.given);

        // the sharing key: the item is there, off, and says why
        await menu("magpie");
        const off = item(w.make);
        assert.equal(await off.isDisabled(), true);
        assert.equal((await off.locator(".rm-why").textContent()).trim(), w.why);
        await page.keyboard.press("Escape");
        assert.equal(events.filter((e) => e.action === "access-key" && e.body.key === "magpie").length, 0);
        assert.deepEqual(await scrolled(), before, "a click moved the page");

        // Magpie Browser's key says it is the browser's, not read-only,
        // and its access isn't changed from the menu
        assert.equal((await tag("ext").textContent()).trim(), w.browser);
        assert.ok((await tag("ext").getAttribute("title")).length > 10, "the tag says what a browser key is");
        await menu("ext");
        assert.equal(await item(w.make).count(), 0);
        assert.equal(await item(w.full).count(), 0);
        await page.keyboard.press("Escape");
        assert.equal(events.filter((e) => e.action === "access-key" && e.body.key === "ext").length, 0);

        // the row still fits: nothing runs past the window's edge
        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert.ok(over <= 0, `the page scrolls sideways by ${over}px`);
        const rowBox = await row("laptop").boundingBox(), b = await tag("laptop").boundingBox();
        assert.ok(b.x >= rowBox.x && b.x + b.width <= rowBox.x + rowBox.width + 1, "the tag runs out of its row");
        assert.equal(await page.locator("select").count(), 0, "no native select");
        // a border all round is the badge's own; a left one alone is a stripe
        const borders = await page.locator("#gatewayKeys .key-readonly").evaluateAll((els) => els.map((e) => {
          const c = getComputedStyle(e);
          return { left: c.borderLeftStyle, right: c.borderRightStyle, leftColor: c.borderLeftColor, rightColor: c.borderRightColor };
        }));
        assert.ok(borders.length > 0);
        for (const b of borders) assert(b.left === "none" || (b.left === b.right && b.leftColor === b.rightColor), `a coloured left border: ${JSON.stringify(b)}`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
