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
  en: { label: "Models this key may use", search: "Search models, or type re: and a pattern", names: "Model names", save: "Save", bad: "One name a line: name = provider/model", saved: "Model names saved", model: "Model mini" },
  zh: { label: "此密钥可用的模型", search: "搜索模型，或输入 re: 加正则", names: "模型名", save: "保存", bad: "每行一个：名称 = provider/model", saved: "模型名称已保存", model: "Model mini" },
  "zh-TW": { label: "此金鑰可用的模型", search: "搜尋模型，或輸入 re: 加正規表示式", names: "模型名稱", save: "儲存", bad: "每行一個：名稱 = provider/model", saved: "模型名稱已儲存", model: "Model mini" },
  ja: { label: "このキーで使えるモデル", search: "モデルを検索、または re: と正規表現を入力", names: "モデル名", save: "保存", bad: "1行に1つ：名前 = provider/model", saved: "モデル名を保存しました", model: "Model mini" },
  de: { label: "Modelle, die dieser Schlüssel nutzen darf", search: "Modelle suchen, oder re: und ein Muster eingeben", names: "Modellnamen", save: "Speichern", bad: "Ein Name pro Zeile: Name = provider/model", saved: "Modellnamen gespeichert", model: "Model mini" },
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

        // the key's names for models: a line that isn't one is said, not sent
        await row.hover();
        await row.locator(".gw-key-more").click();
        await page.locator(".row-menu").getByRole("menuitem", { name: w.names, exact: true }).click();
        const modal = page.locator("#modal");
        const ta = modal.locator("textarea");
        await ta.waitFor();
        await ta.fill("oops");
        await modal.getByRole("button", { name: w.save, exact: true }).click();
        await modal.getByText(w.bad).waitFor();
        assert.equal(sent("aliases-key").length, 0);
        await ta.fill("fast = relay/m\n\ngpt-4 = openai/gpt-5");
        await modal.getByRole("button", { name: w.save, exact: true }).click();
        await until(() => sent("aliases-key").length);
        assert.deepEqual(sent("aliases-key"), [{ key: "work", aliases: { fast: "relay/m", "gpt-4": "openai/gpt-5" } }]);
        await modal.waitFor({ state: "hidden" });
        // opened again, the names are in it
        await row.hover();
        await row.locator(".gw-key-more").click();
        await page.locator(".row-menu").getByRole("menuitem", { name: w.names, exact: true }).click();
        await ta.waitFor();
        assert.equal(await ta.inputValue(), "fast = relay/m\ngpt-4 = openai/gpt-5");
        await page.keyboard.press("Escape");
        await page.waitForFunction(() => document.documentElement.scrollWidth <= window.innerWidth);
        assert.deepEqual(errors, []);
      });
    }
  }
}
