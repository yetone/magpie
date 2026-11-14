// A gateway key is given the management API (/v1/magpie/admin/, #6307,
// ljayic on X) from its row's menu, under Advanced: off, from this
// computer or from any. A key that can manage magpie says so in its row.
// The sharing key, a read-only key and Magpie Browser's can't hold it,
// so their menus don't offer it.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

const words = {
  en: { more: "Key actions", adv: "Advanced", head: "Management API", off: "Off", local: "From this computer", any: "From any computer", tag: "Manages magpie", madeLocal: "Server can manage magpie from this computer", madeOff: "Server can't manage magpie" },
  zh: { more: "密钥操作", adv: "高级", head: "管理 API", off: "关闭", local: "仅限本机", any: "任何电脑均可", tag: "可管理 magpie", madeLocal: "Server 现在可在本机管理 magpie", madeOff: "Server 不再能管理 magpie" },
  "zh-TW": { more: "金鑰操作", adv: "進階", head: "管理 API", off: "關閉", local: "僅限本機", any: "任何電腦均可", tag: "可管理 magpie", madeLocal: "Server 現在可在本機管理 magpie", madeOff: "Server 不再能管理 magpie" },
  ja: { more: "キーの操作", adv: "詳細設定", head: "管理 API", off: "オフ", local: "このコンピュータから", any: "どのコンピュータからでも", tag: "magpie を管理", madeLocal: "Server はこのコンピュータから magpie を管理できます", madeOff: "Server は magpie を管理できなくなりました" },
  de: { more: "Schlüsselaktionen", adv: "Erweitert", head: "Verwaltungs-API", off: "Aus", local: "Von diesem Computer", any: "Von jedem Computer", tag: "Verwaltet magpie", madeLocal: "Server kann magpie von diesem Computer aus verwalten", madeOff: "Server kann magpie nicht mehr verwalten" },
};

const keys = () => [
  { id: "ops", name: "Ops", masked: "sk-magpie-key-…000000", admin: "any" },
  { id: "server", name: "Server", masked: "sk-magpie-key-…222222" },
  { id: "laptop", name: "Laptop", masked: "sk-magpie-key-…111111", readOnly: true },
  { id: "magpie", name: "Magpie", masked: "sk-magpie-key-…444444", lan: true },
  { id: "ext", name: "Magpie Browser", masked: "sk-magpie-key-…555555", readOnly: true, browser: true },
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const [lang, w] of Object.entries(words)) {
      test(`${engine} ${width} ${lang}: a gateway key is given the management API and has it taken away`, async (t) => {
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
        const tag = (id) => row(id).locator(".key-admin");
        const item = (name) => page.locator(".row-menu [role=menuitem]").filter({ hasText: name });
        const level = (name) => page.locator(".proto-menu .pm-item").filter({ has: page.locator(".pm-name", { hasText: new RegExp(`^${name}$`) }) });
        const menu = async (id) => {
          await row(id).hover();
          await row(id).getByRole("button", { name: w.more, exact: true }).click();
          await page.locator(".row-menu").waitFor();
        };
        const levels = async (id) => {
          await menu(id);
          await item(w.adv).click();
          await page.locator(".proto-menu").waitFor();
          assert.equal((await page.locator(".proto-menu .pm-head").textContent()).trim(), w.head);
        };
        const admins = () => events.filter((e) => e.action === "admin-key").map((e) => e.body);

        // a key that can manage magpie says so always, not only on hover
        await page.mouse.move(0, 0);
        assert.equal((await tag("ops").textContent()).trim(), w.tag);
        assert.ok((await tag("ops").boundingBox()).width > 0, "the tag is hidden");
        assert.ok((await tag("ops").getAttribute("title")).length > 10, "the tag says what it means");
        assert.equal(await tag("server").count(), 0);

        const view = page.locator("#view-gateway");
        const scrolled = async () => [await view.evaluate((v) => v.scrollTop), await page.evaluate(() => window.scrollY)];
        const before = await scrolled();

        // folded: the levels aren't in the row menu itself
        await menu("server");
        assert.equal(await page.locator(".row-menu").getByText(w.local, { exact: true }).count(), 0);
        await page.keyboard.press("Escape");
        await levels("server");
        assert.equal(await level(w.off).getAttribute("aria-checked"), "true");
        assert.equal(await level(w.any).getAttribute("aria-checked"), "false");
        await level(w.local).click();
        await page.waitForFunction(() => !!document.querySelector('#gatewayKeys .acc[data-key="server"] .key-admin'));
        assert.deepEqual(admins(), [{ key: "server", admin: "local" }]);
        assert.equal((await page.locator("#status").textContent()).trim(), w.madeLocal);

        await levels("server");
        assert.equal(await level(w.local).getAttribute("aria-checked"), "true");
        await level(w.off).click();
        await page.waitForFunction(() => !document.querySelector('#gatewayKeys .acc[data-key="server"] .key-admin'));
        assert.deepEqual(admins().at(-1), { key: "server", admin: "" });
        assert.equal((await page.locator("#status").textContent()).trim(), w.madeOff);

        // the level a key has is ticked
        await levels("ops");
        assert.equal(await level(w.any).getAttribute("aria-checked"), "true");
        await page.keyboard.press("Escape");

        // keys that can't hold it aren't offered it
        for (const id of ["laptop", "magpie", "ext"]) {
          await menu(id);
          assert.equal(await item(w.adv).count(), 0, `${id} offers the management API`);
          await page.keyboard.press("Escape");
          assert.equal(await tag(id).count(), 0);
        }
        assert.equal(admins().length, 2);
        assert.deepEqual(await scrolled(), before, "a click moved the page");

        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert.ok(over <= 0, `the page scrolls sideways by ${over}px`);
        const rowBox = await row("ops").boundingBox(), b = await tag("ops").boundingBox();
        assert.ok(b.x >= rowBox.x && b.x + b.width <= rowBox.x + rowBox.width + 1, "the tag runs out of its row");
        assert.equal(await page.locator("select").count(), 0, "no native select");
        const borders = await page.locator("#gatewayKeys .key-admin").evaluateAll((els) => els.map((e) => {
          const c = getComputedStyle(e);
          return { left: c.borderLeftStyle, right: c.borderRightStyle, leftColor: c.borderLeftColor, rightColor: c.borderRightColor };
        }));
        for (const b of borders) assert(b.left === "none" || (b.left === b.right && b.leftColor === b.rightColor), `a coloured left border: ${JSON.stringify(b)}`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
