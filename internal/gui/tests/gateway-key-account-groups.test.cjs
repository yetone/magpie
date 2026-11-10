// Run with Node's test runner and Playwright on the module path; see README.md.
// Account groups (LeiWang_2000 on X): named sets of accounts, made in
// the Gateway page's "Account groups" editor (added, renamed in place,
// their accounts picked in the app's own menu, removed unless a key names
// one). A key's models menu offers each group to use ("@<id>"), and "Keep
// accounts out" each group to keep out ("!@<id>"), sent as
// "accounts-key"; the badges say the group by its name. In every
// language, Chromium and WebKit, wide and narrow.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

const words = {
  en: { open: "Account groups", name: "Account group name", accounts: "Accounts in this group", remove: "Remove group", none: "No account groups yet", fresh: "New account group name", add: "Add group", done: "Done", models: "Models this key may use", item: "Keep accounts out", out: "Accounts this key keeps out", note: "Account group", notOne: "Not Night" },
  zh: { open: "账号组", name: "账号组名称", accounts: "这个组的账号", remove: "删除账号组", none: "还没有账号组", fresh: "新账号组名称", add: "添加账号组", done: "完成", models: "此密钥可用的模型", item: "排除账号", out: "这个密钥排除的账号", note: "账号组", notOne: "排除 Night" },
  "zh-TW": { open: "帳號組", name: "帳號組名稱", accounts: "這個組的帳號", remove: "刪除帳號組", none: "還沒有帳號組", fresh: "新帳號組名稱", add: "新增帳號組", done: "完成", models: "此金鑰可用的模型", item: "排除帳號", out: "這個金鑰排除的帳號", note: "帳號組", notOne: "排除 Night" },
  ja: { open: "アカウントグループ", name: "アカウントグループ名", accounts: "このグループのアカウント", remove: "グループを削除", none: "アカウントグループはまだありません", fresh: "新しいグループ名", add: "グループを追加", done: "完了", models: "このキーで使えるモデル", item: "アカウントを除外", out: "このキーが除外するアカウント", note: "アカウントグループ", notOne: "Night を除外" },
  de: { open: "Kontogruppen", name: "Name der Kontogruppe", accounts: "Konten in dieser Gruppe", remove: "Gruppe entfernen", none: "Noch keine Kontogruppen", fresh: "Name der neuen Gruppe", add: "Gruppe hinzufügen", done: "Fertig", models: "Modelle, die dieser Schlüssel nutzen darf", item: "Konten ausschließen", out: "Konten, die dieser Schlüssel ausschließt", note: "Kontogruppe", notOne: "Ohne Night" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 440]) {
    for (const [lang, w] of Object.entries(words)) {
      test(`${engine} ${width} ${lang}: account groups are made in their editor and named by a key's lists`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const events = [], errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", fixture(lang, "light", events, { lan: true,
          accountGroups: [{ id: "gday", name: "Day", accounts: ["relay/me@example.com"] }],
          keyHolds: { server: { accounts: ["@gday"] } } }));
        await page.goto("http://magpie.test/?view=gateway");
        await page.locator("#gatewayKeys .acc[data-key]").last().waitFor();
        const row = (id) => page.locator(`#gatewayKeys .acc[data-key="${id}"]`);
        const sent = (action) => events.filter((e) => e.action === action).map((e) => e.body);
        const until = async (fn) => {
          for (let i = 0; i < 150 && !fn(); i++) await new Promise((r) => setTimeout(r, 20));
        };
        const view = page.locator("#view-gateway");
        const scrolled = async () => [await view.evaluate((v) => v.scrollTop), await page.evaluate(() => window.scrollY)];
        const at = await scrolled();
        const menu = page.locator(".proto-menu");

        // a key held to a group says it by its name
        const models = (id) => row(id).getByRole("button", { name: w.models, exact: true });
        assert.equal((await models("server").textContent()).trim(), "Day");

        // the editor: the group there, its account named
        await page.getByRole("button", { name: w.open, exact: true }).click();
        const ed = page.locator("#modal .acct-groups");
        await ed.waitFor();
        assert.equal(await page.locator("select").count(), 0, "no native select");
        const group = (id) => ed.locator(`.agp-row[data-group="${id}"]`);
        assert.equal(await group("gday").getByRole("textbox", { name: w.name, exact: true }).inputValue(), "Day");
        assert.equal((await group("gday").getByRole("button", { name: w.accounts, exact: true }).textContent()).trim(), "relay/me@example.com");
        const box = await ed.boundingBox();
        assert(box.x >= 0 && box.x + box.width <= width + 1, "the editor inside the window");

        // a group added, its accounts picked in the app's menu
        await ed.getByRole("textbox", { name: w.fresh, exact: true }).fill("Night");
        await ed.getByRole("button", { name: w.add, exact: true }).click();
        await until(() => sent("group-save").length);
        assert.deepEqual(sent("group-save"), [{ name: "Night", accounts: [] }]);
        await group("g1").waitFor();
        await group("g1").getByRole("button", { name: w.accounts, exact: true }).click();
        await menu.waitFor();
        assert.equal((await menu.locator(".pm-head").textContent()).trim(), w.accounts);
        await menu.locator(".pm-item", { hasText: "spare@example.com" }).click();
        await menu.locator(".pm-item", { hasText: "k-1a2b3c" }).click();
        assert.equal(sent("group-save").length, 1, "a pick was sent before the menu closed");
        await page.keyboard.press("Escape");
        await menu.waitFor({ state: "detached" });
        await until(() => sent("group-save").length === 2);
        assert.deepEqual(sent("group-save")[1], { id: "g1", name: "Night", accounts: ["relay/spare@example.com", "openai/k-1a2b3c"] });

        // renamed in place
        const dayName = group("gday").getByRole("textbox", { name: w.name, exact: true });
        await dayName.fill("Daytime");
        await dayName.press("Enter");
        await until(() => sent("group-save").length === 3);
        assert.deepEqual(sent("group-save")[2], { id: "gday", name: "Daytime", accounts: ["relay/me@example.com"] });

        // a group a key names stays, and says why
        await group("gday").getByRole("button", { name: w.remove, exact: true }).click();
        await until(() => sent("group-remove").length);
        assert.equal(await group("gday").count(), 1);
        await ed.getByRole("button", { name: w.done, exact: true }).click();
        await page.locator("#modal").waitFor({ state: "hidden" });
        assert.equal((await models("server").textContent()).trim(), "Daytime", "the badge follows the rename");

        // the models menu offers each group to use, after every account
        events.length = 0;
        await row("work").hover();
        await models("work").click();
        await menu.waitFor();
        const names = (await menu.locator(".pm-item").allTextContents()).map((s) => s.trim());
        const idx = (s) => names.findIndex((n) => n.startsWith(s));
        assert(idx("Night") > idx("k-1a2b3c") && idx("Daytime") > idx("k-1a2b3c"), names.join(" | "));
        assert.match(await menu.locator(".pm-item", { hasText: "Night" }).locator(".pm-note").textContent(), new RegExp(w.note));
        await menu.locator(".pm-item", { hasText: "Night" }).click();
        await page.keyboard.press("Escape");
        await menu.waitFor({ state: "detached" });
        await until(() => sent("accounts-key").length);
        assert.deepEqual(sent("accounts-key"), [{ key: "work", accounts: ["@g1"] }]);
        await page.waitForFunction(() => document.querySelector('#gatewayKeys .acc[data-key="work"] .key-models')?.textContent.trim() === "Night");

        // "Keep accounts out" offers each group to keep out; picked, it
        // takes the group off the ones used
        events.length = 0;
        await row("work").hover();
        await row("work").locator(".gw-key-more").click();
        await page.locator(".row-menu").getByRole("menuitem", { name: w.item, exact: true }).click();
        await menu.waitFor();
        assert.equal((await menu.locator(".pm-head").textContent()).trim(), w.out);
        await menu.locator(".pm-item", { hasText: "Night" }).click();
        await page.keyboard.press("Escape");
        await menu.waitFor({ state: "detached" });
        await until(() => sent("accounts-key").length);
        assert.deepEqual(sent("accounts-key"), [{ key: "work", accounts: ["!@g1"] }]);
        await page.waitForFunction((one) => document.querySelector('#gatewayKeys .acc[data-key="work"] .key-out')?.textContent.trim() === one, w.notOne);

        assert.deepEqual(await scrolled(), at, "a click moved the page");
        assert(!(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth)), "the page scrolls sideways");
        assert.deepEqual(errors, []);
      });
    }
  }
}
