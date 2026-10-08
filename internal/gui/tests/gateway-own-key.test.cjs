// A gateway key can be created with a value of the user's own, one their
// clients already send from another gateway (love1sbug on X).
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

const words = {
  en: { create: "Create", name: "Gateway key name", own: "Gateway key value", placeholder: "Your own key (optional)", made: "Clients that send this key reach magpie now", short: "Use a key between 8 and 256 characters" },
  zh: { create: "创建", name: "网关密钥名称", own: "网关密钥的值", placeholder: "自定义密钥（可选）", made: "发送这个密钥的客户端现在就能连接 magpie", short: "密钥长度需在 8 到 256 个字符之间" },
  ja: { create: "作成", name: "ゲートウェイキー名", own: "ゲートウェイキーの値", placeholder: "独自のキー（任意）", made: "このキーを送るクライアントはそのまま magpie に接続できます", short: "キーは 8〜256 文字にしてください" },
  de: { create: "Erstellen", name: null, own: "Wert des Zugangsschlüssels", placeholder: "Eigener Schlüssel (optional)", made: "Clients, die diesen Schlüssel senden, erreichen magpie jetzt", short: "Der Schlüssel muss 8 bis 256 Zeichen lang sein" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1000, 440]) {
      test(`${engine} ${lang} ${width}px: create a gateway key with a value of one's own`, async (t) => {
        const w = words[lang];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(6000);
        const events = [], errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", fixture(lang, "light", events, { lan: true }));
        await page.goto("http://magpie.test/?view=gateway");
        await page.locator("#gatewayKeys .acc[data-key]").last().waitFor();
        await page.locator("#addGatewayKey").click();
        const own = page.getByRole("textbox", { name: w.own, exact: true });
        const name = page.locator("#gatewayKeys .adding .kf input").first();
        assert.equal(await own.getAttribute("placeholder"), w.placeholder);
        assert.equal(await own.getAttribute("type"), "password");
        // both fields are wide enough to type in, side by side or stacked
        for (const box of [await name.boundingBox(), await own.boundingBox()]) assert(box.width >= 140, `field ${box.width}px wide`);
        await name.fill("Family");
        // a short value is refused and the form stays, with what was typed
        await own.fill("short");
        await own.press("Enter");
        await page.waitForFunction((m) => document.body.textContent.includes(m), w.short);
        assert.equal(await own.inputValue(), "short");
        await own.fill("cpa-family-key-0042");
        await page.getByRole("button", { name: w.create, exact: true }).click();
        const row = page.locator("#gatewayKeys .acc[data-key]", { hasText: "Family" });
        await row.waitFor();
        const sent = events.filter((e) => e.action === "add-key").at(-1).body;
        assert.deepEqual(sent, { name: "Family", secret: "cpa-family-key-0042" });
        assert.match(await row.textContent(), /…0042/);
        assert.doesNotMatch(await page.locator("#gatewayKeys").textContent(), /cpa-family/);
        await page.waitForFunction((m) => document.body.textContent.includes(m), w.made);
        // the next key starts empty: magpie makes it
        await page.locator("#addGatewayKey").click();
        assert.equal(await page.getByRole("textbox", { name: w.own, exact: true }).inputValue(), "");
        await page.locator("#gatewayKeys .adding .kf input").first().fill("Tablet");
        await page.getByRole("button", { name: w.create, exact: true }).click();
        await page.locator("#gatewayKeys .acc[data-key]", { hasText: "Tablet" }).waitFor();
        assert.deepEqual(events.filter((e) => e.action === "add-key").at(-1).body, { name: "Tablet" });
        assert.deepEqual(errors, []);
      });
    }
  }
}
