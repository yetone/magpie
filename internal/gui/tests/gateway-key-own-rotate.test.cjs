// Rotating a gateway key can give it a value of the user's own in place of
// the one magpie makes (isaac_bai on Discord: one easy to remember and
// pass on), typed hidden, checked as a new key's is; left empty, magpie
// makes one as before.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture, keyAction } = require("./fixtures/caller-keys.cjs");

const words = {
  en: { rotate: "Rotate key", own: "Gateway key value", placeholder: "Your own key (optional)", label: "Gateway key", changed: "Gateway key changed. Clients that send this key reach magpie now.", rotated: "Gateway key rotated. Update clients on other computers.", short: "Use a key between 8 and 256 characters" },
  zh: { rotate: "轮换密钥", own: "网关密钥的值", placeholder: "自定义密钥（可选）", label: "网关密钥", changed: "网关密钥已修改，发送这个密钥的客户端现在就能连接 magpie。", rotated: "网关密钥已轮换，请更新其他电脑上的客户端。", short: "密钥长度需在 8 到 256 个字符之间" },
  "zh-TW": { rotate: "輪換金鑰", own: "閘道金鑰的值", placeholder: "自定義金鑰（可選）", label: "閘道金鑰", changed: "閘道金鑰已修改，傳送這個金鑰的用戶端現在就能連線 magpie。", rotated: "閘道金鑰已輪換，請更新其他電腦上的用戶端。", short: "金鑰長度需在 8 到 256 個字元之間" },
  ja: { rotate: "キーを再発行", own: "ゲートウェイキーの値", placeholder: "独自のキー（任意）", label: "ゲートウェイキー", changed: "ゲートウェイキーを変更しました。このキーを送るクライアントはそのまま magpie に接続できます。", rotated: "ゲートウェイキーを更新しました。他のコンピュータのクライアントを更新してください。", short: "キーは 8〜256 文字にしてください" },
  de: { rotate: "Schlüssel erneuern", own: "Wert des Zugangsschlüssels", placeholder: "Eigener Schlüssel (optional)", label: "Zugangsschlüssel", changed: "Zugangsschlüssel geändert. Clients, die diesen Schlüssel senden, erreichen magpie jetzt.", rotated: "Zugangsschlüssel erneuert. Aktualisieren Sie die Clients auf anderen Computern.", short: "Der Schlüssel muss 8 bis 256 Zeichen lang sein" },
};

const said = (page, m) => page.waitForFunction((m) => document.body.textContent.includes(m), m);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1000, 400]) {
      test(`${engine} ${lang} ${width}px: rotate a gateway key to a value of one's own`, async (t) => {
        const w = words[lang];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const events = [], errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", fixture(lang, "light", events, { lan: true }));
        await page.goto("http://magpie.test/?view=gateway");
        const row = page.locator('#gatewayKeys .acc[data-key="laptop"]');
        await row.waitFor();
        await keyAction(page, row, w.rotate);
        const modal = page.locator("#modal");
        const own = modal.locator('input[type="password"]');
        await own.waitFor();
        assert.equal(await own.getAttribute("placeholder"), w.placeholder);
        assert.equal(await own.getAttribute("aria-label"), w.own);
        assert.equal(await modal.locator(".editor .ebody > label").textContent(), w.label);
        // the field fits the dialog, wide enough to type in, at either width
        const box = await own.boundingBox(), dlg = await modal.locator(".editor").boundingBox();
        assert(box.width >= 140, `field ${box.width}px wide`);
        assert(box.x >= dlg.x && box.x + box.width <= dlg.x + dlg.width + 0.5, "field inside the dialog");
        // a short value is refused and the dialog stays, with what was typed
        await own.fill("short");
        await own.press("Enter");
        await said(page, w.short);
        assert(await modal.isVisible(), "the dialog stays");
        assert.equal(await own.inputValue(), "short");
        assert.equal(events.filter((e) => e.action === "rotate-key").length, 1);
        await own.fill("family-easy-to-share");
        await modal.getByRole("button", { name: w.rotate, exact: true }).click();
        await modal.waitFor({ state: "hidden" });
        assert.deepEqual(events.filter((e) => e.action === "rotate-key").at(-1).body, { key: "laptop", secret: "family-easy-to-share" });
        await said(page, w.changed);
        await page.waitForFunction(() => document.querySelector('#gatewayKeys .acc[data-key="laptop"]')?.textContent.includes("…hare"));
        assert.doesNotMatch(await page.locator("#gatewayKeys").textContent(), /family-easy/);
        // the next rotation starts empty: magpie makes the key
        await keyAction(page, row, w.rotate);
        assert.equal(await own.inputValue(), "");
        await modal.getByRole("button", { name: w.rotate, exact: true }).click();
        await modal.waitFor({ state: "hidden" });
        assert.deepEqual(events.filter((e) => e.action === "rotate-key").at(-1).body, { key: "laptop" });
        await said(page, w.rotated);
        // Remove asks with no field
        await row.locator(".gw-key-more").click();
        await page.locator(".row-menu [role=menuitem]").last().click();
        await modal.locator(".editor").waitFor();
        assert.equal(await modal.locator("input").count(), 0);
        assert.deepEqual(errors, []);
      });
    }
  }
}
