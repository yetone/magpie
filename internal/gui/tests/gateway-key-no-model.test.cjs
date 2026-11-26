// A gateway key for MCP servers only (ifitscolorful on X): "No model" in
// the key's models menu holds it to no model ("-", access.NoModels), sent
// as "models-key" when the menu closes; a model ticked beside it since
// drops it, as "No MCP server" is dropped beside a server.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

const words = {
  en: { label: "Models this key may use", none: "No model", note: "MCP servers only", title: /may use no model, only MCP servers/, done: "Server may use no model, only MCP servers" },
  zh: { label: "此密钥可用的模型", none: "无模型", note: "仅 MCP 服务器", title: /不可使用模型，只能使用 MCP 服务器/, done: "Server 不可使用模型，只能使用 MCP 服务器" },
  "zh-TW": { label: "此金鑰可用的模型", none: "無模型", note: "僅 MCP 伺服器", title: /不可使用模型，只能使用 MCP 伺服器/, done: "Server 不可使用模型，只能使用 MCP 伺服器" },
  ja: { label: "このキーで使えるモデル", none: "モデルなし", note: "MCP サーバーのみ", title: /モデルを使えず、MCP サーバーのみ/, done: "Server はモデルを使えず、MCP サーバーのみ使えます" },
  de: { label: "Modelle, die dieser Schlüssel nutzen darf", none: "Kein Modell", note: "Nur MCP-Server", title: /kein Modell nutzen, nur MCP-Server/, done: "Server darf kein Modell nutzen, nur MCP-Server" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, w] of Object.entries(words)) {
    test(`${engine} ${lang}: a gateway key is held to no model, for MCP servers only`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 700 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(6000);
      page.setDefaultNavigationTimeout(60000);
      const events = [], errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", fixture(lang, "light", events, { lan: true, emptyProvider: true, limits: { server: { models: ["openai/*", "relay/m"] } } }));
      await page.goto("http://magpie.test/?view=gateway");
      await page.locator("#gatewayKeys .acc[data-key]").last().waitFor();
      const row = (id) => page.locator(`#gatewayKeys .acc[data-key="${id}"]`);
      const badge = (id) => row(id).getByRole("button", { name: w.label, exact: true });
      const sent = () => events.filter((e) => e.action === "models-key").map((e) => e.body);
      const menu = page.locator(".proto-menu");
      const noModel = menu.locator(`.pm-item:has(.pm-name:text-is("${w.none}"))`);

      // a key held to two models is held to none
      await row("server").hover();
      await badge("server").click();
      await menu.waitFor();
      assert.equal(await noModel.count(), 1);
      assert.equal((await noModel.locator(".pm-note").textContent()).trim(), w.note);
      assert.equal(await noModel.evaluate((e) => e.classList.contains("on")), false);
      await noModel.click();
      assert.equal(sent().length, 0, "a pick was sent before the menu closed");
      await page.keyboard.press("Escape");
      await menu.waitFor({ state: "detached" });
      assert.deepEqual(sent(), [{ key: "server", models: ["-"] }]);
      await page.waitForFunction((none) => document.querySelector('#gatewayKeys .acc[data-key="server"] .key-models')?.textContent.trim() === none, w.none);
      assert.match(await badge("server").getAttribute("title"), w.title);
      assert.equal(await badge("server").evaluate((b) => b.classList.contains("set")), true);
      await page.waitForFunction((done) => document.querySelector("#status")?.textContent === done, w.done);

      // reopened, "No model" is the one ticked; a model ticked beside it drops it
      await badge("server").click();
      await menu.waitFor();
      assert.equal(await menu.locator(".pm-item.on").count(), 1);
      assert.equal(await noModel.evaluate((e) => e.classList.contains("on")), true);
      await menu.locator(".pm-item", { hasText: "GPT-5" }).first().click();
      await page.keyboard.press("Escape");
      await menu.waitFor({ state: "detached" });
      assert.deepEqual(sent().at(-1), { key: "server", models: ["openai/gpt-5"] });
      await page.waitForFunction(() => document.querySelector('#gatewayKeys .acc[data-key="server"] .key-models')?.textContent.trim() === "openai/gpt-5");

      // a free key ticked "No model" beside nothing is held to none too
      await row("work").hover();
      await badge("work").click();
      await menu.waitFor();
      await noModel.click();
      await page.keyboard.press("Escape");
      await menu.waitFor({ state: "detached" });
      assert.deepEqual(sent().at(-1), { key: "work", models: ["-"] });
      await page.waitForFunction((none) => document.querySelector('#gatewayKeys .acc[data-key="work"] .key-models')?.textContent.trim() === none, w.none);
      assert.equal(await page.locator("select").count(), 0, "no native select");
      assert.deepEqual(errors, []);
    });
  }
}
