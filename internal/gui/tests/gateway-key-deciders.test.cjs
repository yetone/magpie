// Run with Node's test runner and Playwright on the module path; see README.md.
// A gateway key held to routing groups can be let ask a decision model
// too (Kautism on Discord: a key held to two groups, and Clef and Jev
// listed nowhere): the key's models menu lists them, each saying it is a
// decision model a routing group asks whatever the key may use, and a
// pick is sent beside the groups.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

const notes = {
  en: "Decision model: a routing group asks it whatever this key may use; pick it so the key may ask it itself",
  zh: "决策模型：路由组照常会问它，与此密钥能用哪些模型无关；勾选后此密钥也能直接问它",
  "zh-TW": "決策模型：路由組照常會問它，與此金鑰能用哪些模型無關；勾選後此金鑰也能直接問它",
  ja: "決定モデル：このキーで使えるモデルに関係なく、ルーティンググループは問い合わせます。選ぶとこのキーから直接問い合わせられます",
  de: "Entscheidungsmodell: Routing-Gruppen fragen es, egal was dieser Schlüssel nutzen darf; wähle es, damit der Schlüssel es selbst fragen darf",
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1000, 400]) {
    for (const lang of Object.keys(notes)) {
      test(`${engine} ${width}px ${lang}: a key held to groups is let ask Clef`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const events = [], errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", fixture(lang, "light", events, { lan: true, groups: true, deciders: true, limits: { server: { models: ["group/fast", "group/coding"] } } }));
        await page.goto("http://magpie.test/?view=gateway");
        const badge = page.locator('#gatewayKeys .acc[data-key="server"] .key-models');
        await badge.waitFor();
        await badge.click();
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        const clef = menu.locator(".pm-item", { hasText: "Clef" });
        const jev = menu.locator(".pm-item", { hasText: "Jev" });
        for (const [item, id] of [[clef, "cf/@cf/cloudflare/clef"], [jev, "ts/jev-latest"]]) {
          const note = (await item.locator(".pm-note").textContent()).trim();
          assert.equal(note, id + " · " + notes[lang]);
        }
        // a chat model says its id alone
        assert.equal((await menu.locator(".pm-item", { hasText: "GPT-5" }).locator(".pm-note").textContent()).trim(), "openai/gpt-5");
        // the note fits the menu: wrapped, not cut off past its edge
        const m = await menu.boundingBox(), n = await clef.locator(".pm-note").boundingBox();
        assert(n.x >= m.x - 1 && n.x + n.width <= m.x + m.width + 1, `the note runs past the menu: ${JSON.stringify({ m, n })}`);
        assert(m.x >= 0 && m.x + m.width <= width + 1, `the menu runs off the window: ${JSON.stringify(m)}`);
        await clef.click();
        await page.keyboard.press("Escape");
        await menu.waitFor({ state: "detached" });
        assert.deepEqual(events.filter((e) => e.action === "models-key").map((e) => e.body),
          [{ key: "server", models: ["group/coding", "group/fast", "cf/@cf/cloudflare/clef"] }]);
        assert.deepEqual(errors, []);
      });
    }
  }
}
