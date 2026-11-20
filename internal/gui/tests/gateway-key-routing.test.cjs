// A gateway key's group routing (justforlxz on X): a badge on the key's
// row, shown always while set and on hover while not, opens the app's
// menu (no native select): as the group routes, or one of the routings a
// group can take but Manual. It sends "routing-key"; a read-only key has
// none.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

const words = {
  en: { label: "Group routing", follow: "As the group routes", turn: "In turn", smart: "Quota first" },
  zh: { label: "分组路由", follow: "按分组自己的方式", turn: "轮流", smart: "额度优先" },
  "zh-TW": { label: "分組路由", follow: "依分組自己的方式", turn: "輪流", smart: "額度優先" },
  ja: { label: "グループのルーティング", follow: "グループの設定どおり", turn: "交互", smart: "枠優先" },
  de: { label: "Gruppen-Routing", follow: "Wie die Gruppe routet", turn: "Abwechselnd", smart: "Kontingent zuerst" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const [lang, w] of Object.entries(words)) {
      test(`${engine} ${width} ${lang}: a key's group routing is set from the app's menu`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const events = [], errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", fixture(lang, "light", events, { lan: true, keyHolds: { server: { routing: "rotate" }, laptop: { readOnly: true } } }));
        await page.goto("http://magpie.test/?view=gateway");
        await page.locator("#gatewayKeys .acc[data-key]").last().waitFor();
        const row = (id) => page.locator(`#gatewayKeys .acc[data-key="${id}"]`);
        const badge = (id) => row(id).locator(".key-routing");
        const item = (name) => page.locator(".proto-menu .pm-item", { has: page.locator(".pm-name", { hasText: new RegExp(`^${name}$`) }) });
        const sent = () => events.filter((e) => e.action === "routing-key").map((e) => e.body);
        const reads = (id, text) => page.waitForFunction(([id, text]) => document.querySelector(`#gatewayKeys .acc[data-key="${id}"] .key-routing`)?.textContent === text, [id, text]);

        // set: said always, not only on hover; a read-only key has none
        await page.mouse.move(0, 0);
        assert.equal(await badge("server").textContent(), w.turn);
        assert.equal(await badge("server").evaluate((b) => getComputedStyle(b).opacity), "1");
        assert.equal(await badge("server").getAttribute("aria-label"), w.label);
        assert.equal(await badge("laptop").count(), 0);
        // unset: on hover only
        assert.equal(await badge("work").evaluate((b) => getComputedStyle(b).opacity), "0");
        await row("work").hover();
        assert.equal(await badge("work").textContent(), w.label);

        await badge("work").click();
        await page.locator(".proto-menu").waitFor();
        assert.equal(await page.locator(".proto-menu .pm-head").textContent(), w.label);
        // a key has no pick of its own to send to: no Manual
        assert.equal(await item("Manual").count(), 0);
        await item(w.smart).click();
        await reads("work", w.smart);
        assert.deepEqual(sent(), [{ key: "work", routing: "smart" }]);

        await row("server").hover();
        await badge("server").click();
        await item(w.follow).click();
        await reads("server", w.label);
        assert.deepEqual(sent().at(-1), { key: "server", routing: "" });

        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert.ok(over <= 0, `the page scrolls sideways by ${over}px`);
        assert.equal(await page.locator("select").count(), 0, "no native select");
        assert.deepEqual(errors, []);
      });
    }
  }
}
