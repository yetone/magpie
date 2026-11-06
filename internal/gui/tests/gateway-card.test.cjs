const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

// The Gateway page's card (the owner: "一点都不精致、优雅", and the key
// row's copy "很奇怪的位置"): the gateway's mark and state, Share on local
// network switched on the card itself with the addresses it answers on,
// each to copy; a key's copy right beside its masked key, Disable key (or
// Enable key), Rotate key and Remove under its … (wei_VC on X: the tick
// alone wasn't seen as a switch, so a key was removed and added again);
// and Connect folded until it is opened.
const WORDS = {
  en: { share: "Share on local network", local: "This computer", lan: "Local network", more: "Key actions", off: "Disable key", on: "Enable key", readOnly: "Make read-only", rotate: "Rotate key", remove: "Remove" },
  zh: { share: "局域网共享", local: "本机", lan: "局域网", more: "密钥操作", off: "停用密钥", on: "启用密钥", readOnly: "设为只读", rotate: "轮换密钥", remove: "移除" },
  "zh-TW": { share: "區域網共享", local: "本機", lan: "區域網", more: "金鑰操作" },
  ja: { share: "ローカルネットワークで共有", local: "このコンピュータ", lan: "ローカルネットワーク", more: "キーの操作" },
  de: { share: "Im lokalen Netzwerk freigeben", local: "Dieser Computer", lan: "Lokales Netzwerk", more: "Schlüsselaktionen" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const locale of Object.keys(WORDS)) {
    for (const width of [1100, 420]) {
      test(`${engine} ${locale} ${width}: the gateway card shares on the network, and a key's copy sits by the key`, async (t) => {
        const w = WORDS[locale];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${locale}-${width}-gateway-card.png`), fullPage: true });
          }
          await browser.close();
        });
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const events = [];
        await page.route("**/*", fixture(locale, "dark", events));
        await page.goto("http://magpie.test/?view=gateway");
        const lanBox = page.locator("#gatewayLan");
        await lanBox.locator(".gw-share").waitFor();

        // the mark in its tile, the state beside it
        assert.equal(await page.locator("#gateway > .gw-mark.on svg .led").count(), 2, "the mark's two lights");
        assert.equal(await page.locator("#gateway .url").count(), 0, "no address pill in the status row");
        // the state centred on the title's line, not sat low on its baseline
        // (the owner: "没有水平对齐")
        const off = await page.locator("#gateway .name").evaluate((n) => {
          const r = document.createRange();
          r.selectNodeContents(n.firstChild);
          const a = r.getBoundingClientRect(), b = n.querySelector(".state").getBoundingClientRect();
          return Math.abs((a.top + a.bottom) / 2 - (b.top + b.bottom) / 2);
        });
        assert(off <= 0.25, `the state's middle is ${off}px off the title's`);
        // Connect folded by default, its base URL in its head
        assert.equal(await page.locator("#foldConnect").getAttribute("aria-expanded"), "false", "Connect starts folded");
        assert(await page.locator("#connect").evaluate((c) => c.hidden));

        // not shared: the choice says This computer, one address, the keys away
        assert.equal((await lanBox.locator(".gw-share .name").textContent()).trim(), w.share);
        const opts = lanBox.locator(".segs button");
        assert.deepEqual((await opts.allTextContents()).map((s) => s.trim()), [w.local, w.lan]);
        assert.equal(await lanBox.locator(".gw-url").count(), 1);
        assert.equal((await lanBox.locator(".gw-url code").textContent()).trim(), "http://127.0.0.1:3999");
        assert.equal((await lanBox.locator(".gw-url-label").textContent()).trim(), w.local);
        assert.equal(await page.locator("#gatewayKeysBlock").isVisible(), false);

        // shared from the card, without a scroll: every LAN address, each with a copy
        const y = await page.locator("#view-gateway").evaluate((v) => v.scrollTop);
        await opts.last().click();
        await page.locator("#gatewayKeysBlock").waitFor({ state: "visible" });
        await page.waitForFunction(() => document.querySelectorAll("#gatewayLan .gw-url").length === 3);
        assert.deepEqual(events.filter((e) => e.action === "lan").map((e) => e.body), [{ on: true }]);
        assert.deepEqual((await lanBox.locator(".gw-url code").allTextContents()).map((s) => s.trim()),
          ["http://127.0.0.1:3999", "http://192.168.1.10:3999", "http://10.0.0.10:3999"]);
        assert.deepEqual((await lanBox.locator(".gw-url-label").allTextContents()).map((s) => s.trim()), [w.local, w.lan, ""]);
        assert.equal(await lanBox.locator(".gw-url .copy").count(), 3);
        assert.equal(y, await page.locator("#view-gateway").evaluate((v) => v.scrollTop), "sharing moved the page");
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${locale}-${width}-gateway-card-shared.png`), fullPage: true });
        }
        await lanBox.locator(".gw-url").nth(1).locator(".copy").click();
        await page.waitForFunction(() => document.querySelector("#gatewayLan .copy.done"));
        assert(events.some((e) => e.action === "clipboard" && e.body.text === "http://192.168.1.10:3999"));

        // a key's row: the copy right after the masked key, the … at the end
        await page.waitForFunction(() => document.querySelectorAll("#gatewayKeys .acc[data-key]").length >= 3);
        const row = page.locator('#gatewayKeys .acc[data-key="work"]');
        const near = await row.evaluate((r) => {
          const plan = r.querySelector(".plan"), copy = plan.nextElementSibling, more = r.querySelector(".gw-key-more");
          const p = plan.getBoundingClientRect(), c = copy.getBoundingClientRect(), m = more.getBoundingClientRect(), b = r.getBoundingClientRect();
          return { isCopy: copy.classList.contains("copy"), gap: c.left - p.right, moreRight: b.right - m.right, last: r.lastElementChild === more, label: more.getAttribute("aria-label") };
        });
        assert(near.isCopy, "the copy follows the masked key");
        assert(near.gap >= 0 && near.gap <= 6, `the copy sits ${near.gap}px from the key`);
        assert(near.last && near.moreRight <= 14, "the … ends the row");
        assert.equal(near.label, w.more);
        assert.equal(await row.locator(".text.quiet").count(), 0, "no Rotate or Remove words in the row");
        await row.locator(".gw-key-more").click();
        const menu = page.locator(".row-menu");
        await menu.waitFor();
        const items = (await menu.getByRole("menuitem").allTextContents()).map((s) => s.trim());
        assert.equal(items.length, 5);
        if (w.rotate) assert.deepEqual(items.filter((s) => !/MCP/.test(s)), [w.off, w.readOnly, w.rotate, w.remove]);
        assert.deepEqual(items, await page.evaluate(() => [t("Disable key"), t("Make read-only"), t("MCP servers"), t("Rotate key"), t("Remove")]));
        // off from the menu, then on again: the key stays, only its switch moves
        await menu.getByRole("menuitem").first().click();
        await menu.waitFor({ state: "detached" });
        await page.locator('#gatewayKeys .acc.off[data-key="work"]').waitFor();
        await row.locator(".gw-key-more").click();
        await menu.waitFor();
        const again = (await menu.getByRole("menuitem").allTextContents()).map((s) => s.trim());
        if (w.on) assert.equal(again[0], w.on);
        assert.equal(again[0], await page.evaluate(() => t("Enable key")));
        await menu.getByRole("menuitem").first().click();
        await page.locator('#gatewayKeys .acc.in-use[data-key="work"]').waitFor();
        assert.deepEqual(events.filter((e) => /^(on|off|remove)-key$/.test(e.action)).map((e) => [e.action, e.body.key]), [["off-key", "work"], ["on-key", "work"]]);

        // nothing runs out of the page or a card, no coloured left border
        const over = await page.evaluate(() => [...document.querySelectorAll("#gateway, #gatewayLan *, #gatewayKeys .acc")]
          .filter((e) => e.scrollWidth > e.clientWidth + 1 && !e.matches("code, .gw-url-label, .plan, .rename, .n")).map((e) => e.className || e.tagName));
        assert.deepEqual(over, [], "a row overflows");
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1), "the page scrolls sideways");
        const borders = await page.evaluate(() => [...document.querySelectorAll(".gwcard *")].filter((e) => { const s = getComputedStyle(e); return parseFloat(s.borderLeftWidth) > 1 && s.borderLeftStyle !== "none"; }).map((e) => e.className));
        assert.deepEqual(borders, []);

        // back to This computer: one address again, the keys away
        await opts.first().click();
        await page.locator("#gatewayKeysBlock").waitFor({ state: "hidden" });
        await page.waitForFunction(() => document.querySelectorAll("#gatewayLan .gw-url").length === 1);
        assert.deepEqual(errors, []);
      });
    }
  }
}
