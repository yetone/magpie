// Run with Node's test runner and Playwright on the module path; see README.md.
// A gateway key's model picker has a search box (it narrows by a model's
// name or its id), takes a regular expression typed as "re:<pattern>"
// and Enter, and the key's row menu gives the key's own names for models
// ("Model names", one "name = provider/model" a line, sent as
// "aliases-key"). tsingchao on X. In every language, Chromium and WebKit,
// wide and narrow.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

const words = {
  en: { label: "Models this key may use", search: "Search models, or type re: and a pattern", names: "Model names", save: "Save", add: "Add a name", saved: "Model names saved", model: "Model mini" },
  zh: { label: "此密钥可用的模型", search: "搜索模型，或输入 re: 加正则", names: "模型名", save: "保存", add: "添加名称", saved: "模型名称已保存", model: "Model mini" },
  "zh-TW": { label: "此金鑰可用的模型", search: "搜尋模型，或輸入 re: 加正規表示式", names: "模型名稱", save: "儲存", add: "新增名稱", saved: "模型名稱已儲存", model: "Model mini" },
  ja: { label: "このキーで使えるモデル", search: "モデルを検索、または re: と正規表現を入力", names: "モデル名", save: "保存", add: "名前を追加", saved: "モデル名を保存しました", model: "Model mini" },
  de: { label: "Modelle, die dieser Schlüssel nutzen darf", search: "Modelle suchen, oder re: und ein Muster eingeben", names: "Modellnamen", save: "Speichern", add: "Name hinzufügen", saved: "Modellnamen gespeichert", model: "Model mini" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 440]) {
    for (const [lang, w] of Object.entries(words)) {
      test(`${engine} ${width} ${lang}: a key's models are searched, matched by a pattern and named`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const events = [], errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", fixture(lang, "light", events, { lan: true }));
        await page.goto("http://magpie.test/?view=gateway");
        await page.locator("#gatewayKeys .acc[data-key]").last().waitFor();
        const row = page.locator('#gatewayKeys .acc[data-key="work"]');
        const sent = (a) => events.filter((e) => e.action === a).map((e) => e.body);
        const until = async (fn) => {
          for (let i = 0; i < 150 && !fn(); i++) await new Promise((r) => setTimeout(r, 20));
        };
        // the search box narrows by name, and by id
        await row.hover();
        await row.getByRole("button", { name: w.label, exact: true }).click();
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        const q = menu.locator(".sc-q");
        assert.equal(await q.getAttribute("placeholder"), w.search);
        const shown = async () => (await menu.locator(".pm-item:not([hidden]) .pm-name").allTextContents()).map((s) => s.trim());
        await q.fill("mini");
        assert.deepEqual(await shown(), [w.model]);
        await q.fill("openai/gpt");
        assert.deepEqual(await shown(), ["GPT-5"]);
        // a pattern typed as re: and Enter is added, and closes the menu
        await q.fill("re:^gpt-");
        await q.press("Enter");
        await menu.waitFor({ state: "detached" });
        await until(() => sent("models-key").length);
        assert.deepEqual(sent("models-key"), [{ key: "work", models: ["re:^gpt-"] }]);
        await page.waitForFunction(() => document.querySelector('#gatewayKeys .acc[data-key="work"] .key-models.set')?.textContent.trim() === "re:^gpt-");

        // the key's names for models, a row each: a name typed and a model
        // picked in the app's menu. A row with half of it can't be saved.
        const open = async () => {
          await row.hover();
          await row.locator(".gw-key-more").click();
          await page.locator(".row-menu").getByRole("menuitem", { name: w.names, exact: true }).click();
        };
        await open();
        const modal = page.locator("#modal");
        assert.equal(await modal.locator("textarea").count(), 0);
        const save = modal.getByRole("button", { name: w.save, exact: true });
        const pairs = modal.locator(".pair");
        await pairs.first().waitFor();
        assert.equal(await pairs.count(), 1);
        await pairs.nth(0).locator("input").fill("oops");
        assert.equal(await save.isDisabled(), true);
        const choose = async (n, find, name) => {
          await pairs.nth(n).locator(".alias-pick").click();
          const pm = page.locator(".proto-menu");
          await pm.waitFor();
          await pm.locator(".sc-q").fill(find);
          await pm.locator(".pm-item:not([hidden])").filter({ has: page.locator(".pm-name", { hasText: new RegExp("^" + name + "$") }) }).click();
          await pm.waitFor({ state: "detached" });
        };
        await choose(0, "openai/gpt", "GPT-5");
        assert.equal(await save.isDisabled(), false);
        await pairs.nth(0).locator("input").fill("gpt-4");
        await modal.getByRole("button", { name: w.add, exact: true }).click();
        assert.equal(await pairs.count(), 2);
        await pairs.nth(1).locator("input").fill("fast");
        assert.equal(await save.isDisabled(), true);
        await choose(1, "mini", "Model mini");
        // a row with neither is left out; a name's model can be picked again
        await modal.getByRole("button", { name: w.add, exact: true }).click();
        assert.equal(await pairs.count(), 3);
        assert.equal(await save.isDisabled(), false);
        await choose(1, "relay/m", "Model");
        await pairs.nth(2).locator(".side button").click();
        assert.equal(await pairs.count(), 2);
        await save.click();
        await until(() => sent("aliases-key").length);
        assert.deepEqual(sent("aliases-key"), [{ key: "work", aliases: { "gpt-4": "openai/gpt-5", fast: "relay/m" } }]);
        await modal.waitFor({ state: "hidden" });
        // opened again, the names are in it
        await open();
        await pairs.nth(1).waitFor();
        assert.deepEqual(await pairs.locator("input").evaluateAll((l) => l.map((i) => i.value)), ["gpt-4", "fast"]);
        assert.deepEqual(await pairs.locator(".alias-pick").evaluateAll((l) => l.map((b) => b.title)), ["openai/gpt-5", "relay/m"]);
        await page.keyboard.press("Escape");
        await page.waitForFunction(() => document.documentElement.scrollWidth <= window.innerWidth);
        assert.deepEqual(errors, []);
      });
    }
  }
}
