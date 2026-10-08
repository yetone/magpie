const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture, confirmKeyAction } = require("./fixtures/caller-keys.cjs");

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: manage named caller keys and filter their usage`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(6000);
      await page.addInitScript(() => {
        window.callerFocus = [];
        const focus = HTMLElement.prototype.focus;
        HTMLElement.prototype.focus = function (options) {
          if (this.closest("#gatewayKeys") || this.closest("#modal")) {
            callerFocus.push({ input: this.tagName === "INPUT", preventScroll: options?.preventScroll === true });
          }
          return focus.call(this, options);
        };
      });
      const events = [], errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", fixture(lang, "light", events, { lan: true }));
      const w = lang === "zh"
        ? { create: "创建", name: "网关密钥名称", remove: "移除", copy: "复制网关密钥", disable: "停用密钥", enable: "启用密钥", requests: "请求", usage: "用量" }
        : { create: "Create", name: "Gateway key name", remove: "Remove", copy: "Copy gateway key", disable: "Disable key", enable: "Enable key", requests: "Requests", usage: "Usage" };
      await page.goto("http://magpie.test/?view=gateway");
      await page.locator("#gatewayKeys .acc[data-key]").last().waitFor();
      assert.equal(await page.locator("#gatewayKeys .accts").count(), 1);
      assert.equal(await page.locator("#gatewayKeysBlock").isVisible(), true);
      assert.equal(await page.locator("#gatewayKeys .acc.add").count(), 0, "creation is offered once, in the header");
      await page.locator("#addGatewayKey").click();
      assert.equal(await page.evaluate(() => callerFocus.at(-1)?.preventScroll), true, "creating a key focuses without scrolling");
      await page.getByRole("button", { name: w.create, exact: true }).click();
      await page.waitForFunction(() => document.querySelector("#gatewayKeys .adding .primary")?.disabled === false);
      await page.getByRole("textbox", { name: w.name, exact: true }).fill("Tablet");
      await page.getByRole("textbox", { name: w.name, exact: true }).press("Enter");
      let tablet = page.locator("#gatewayKeys .acc[data-key]", { hasText: "Tablet" });
      await tablet.waitFor();
      assert.equal(await page.locator("#gatewayKeys .acc[data-key]").count(), 4);
      await tablet.getByRole("button", { name: w.copy, exact: true }).click();
      await page.waitForFunction(() => document.querySelector("#gatewayKeys .copy.done"));
      assert(events.some((e) => e.action === "clipboard" && e.body.text === "fixture-created-1"));
      await tablet.getByRole("button", { name: w.disable, exact: true }).click();
      await tablet.getByRole("button", { name: w.enable, exact: true }).waitFor();
      await tablet.getByRole("button", { name: w.enable, exact: true }).click();
      await tablet.getByRole("button", { name: w.disable, exact: true }).waitFor();
      await tablet.getByRole("button", { name: "Tablet", exact: true }).click();
      assert.equal(await page.evaluate(() => callerFocus.at(-1)?.preventScroll), true, "renaming a key focuses without scrolling");
      await page.locator("#gatewayKeys .rename-in").fill("Travel");
      await page.locator("#gatewayKeys .rename-in").press("Enter");
      tablet = page.locator("#gatewayKeys .acc[data-key]", { hasText: "Travel" });
      await tablet.waitFor();
      await page.locator("#nav").getByRole("button", { name: w.usage, exact: true }).click();
      await page.locator("#usageKeys .row").last().waitFor();
      assert.equal(await page.locator("#usageKeys .row").count(), 3);
      assert.match(await page.locator("#usageKeys").textContent(), /Laptop/);
      assert.equal(await page.locator("#usageKeysHead").textContent(), lang === "zh" ? "网关密钥" : "Gateway keys");
      assert.equal(await page.locator("#usageCallerKeys").count(), 0, "there is only one API key section");
      await page.locator("#usageTab").getByRole("button", { name: w.requests, exact: true }).click();
      await page.locator("#ledKey").click();
      await page.locator(".sess-menu .pm-item", { hasText: "Laptop" }).click();
      await page.waitForFunction(() => document.querySelectorAll("#ledWrap tbody tr").length === 1);
      assert.match(await page.locator("#ledWrap").textContent(), /Laptop/);
      await page.locator("#ledExport").click();
      await page.waitForTimeout(150);
      assert(events.some((e) => e.action === "export" && e.query.includes("callerKey=laptop") && !e.query.includes("key=") && !e.query.includes("user=")));
      await page.reload();
      await page.locator("#prefs").click();
      await page.locator("#setTab-network").click();
      const share = page.locator("#lanList").getByRole("button", { name: lang === "zh" ? "开启" : "On", exact: true });
      await share.waitFor();
      // Real wheel input lets the page remember the reader's scroll position.
      const bounds = await share.boundingBox();
      await page.mouse.move(500, 400);
      await page.mouse.wheel(0, bounds.y - 250);
      await share.click();
      await page.locator("#lanList .lan-address-row").waitFor();
      assert.equal(await page.locator("#lanList").getByRole("button", { name: w.copy, exact: true }).count(), 0,
        "Settings does not duplicate key management");
      await page.locator("#nav").getByRole("button", { name: lang === "zh" ? "网关" : "Gateway", exact: true }).click();
      await page.waitForFunction(() => document.querySelectorAll("#gatewayKeys .acc[data-key]").length === 4);
      let lanRow = page.locator("#gatewayKeys .acc[data-key]", { has: page.locator(".rename", { hasText: /^Laptop$/ }) });
      const lanID = await lanRow.getAttribute("data-key");
      const before = await lanRow.locator(".plan").textContent();
      await lanRow.getByRole("button", { name: w.copy, exact: true }).click();
      await page.waitForFunction(() => document.querySelector("#gatewayKeys .copy.done"));
      assert(events.some((e) => e.action === "clipboard" && e.body.text === "fixture-laptop"));
      const rotateLabel = lang === "zh" ? "轮换密钥" : "Rotate key";
      const cancelLabel = lang === "zh" ? "取消" : "Cancel";
      await lanRow.getByRole("button", { name: rotateLabel, exact: true }).click();
      assert.equal(await page.evaluate(() => callerFocus.at(-1)?.preventScroll), true, "confirmation focuses without scrolling");
      await page.locator("#modal").getByRole("button", { name: cancelLabel, exact: true }).click();
      await page.locator("#modal").waitFor({ state: "hidden" });
      assert.equal(events.filter((e) => e.action === "rotate-key").length, 0);
      await confirmKeyAction(page, lanRow, rotateLabel);
      await page.waitForFunction((masked) => document.querySelector('#gatewayKeys .acc[data-key="laptop"] .plan')?.textContent !== masked, before);
      assert.equal(await lanRow.getAttribute("data-key"), lanID, "rotation keeps the key's identity");
      await lanRow.getByRole("button", { name: w.copy, exact: true }).click();
      await page.waitForFunction(() => document.querySelector("#gatewayKeys .copy.done"));
      assert(events.some((e) => e.action === "clipboard" && e.body.text === "fixture-rotated-1"));
      await lanRow.getByRole("button", { name: w.remove, exact: true }).click();
      await page.keyboard.press("Escape");
      await page.locator("#modal").waitFor({ state: "hidden" });
      assert.equal(events.filter((e) => e.action === "remove-key").length, 0);
      await confirmKeyAction(page, lanRow, w.remove);
      await page.waitForFunction(() => document.querySelectorAll("#gatewayKeys .acc[data-key]").length === 3);
      await page.locator("#prefs").click();
      await page.locator("#setTab-network").click();
      await page.locator("#lanList .lan-address-row").waitFor();
      await page.locator("#lanList").getByRole("button", { name: lang === "zh" ? "关闭" : "Off", exact: true }).click();
      await page.locator("#lanList .lan-address-row").waitFor({ state: "detached" });
      await share.click();
      await page.locator("#lanList .lan-address-row").waitFor();
      await page.locator("#nav").getByRole("button", { name: lang === "zh" ? "网关" : "Gateway", exact: true }).click();
      await page.waitForFunction(() => document.querySelectorAll("#gatewayKeys .acc[data-key]").length === 3);
      assert.equal(await page.locator(`#gatewayKeys .acc[data-key="${lanID}"]`).count(), 0,
        "sharing does not restore a removed key");
      assert.equal(await page.locator("#gatewayKeys .rename", { hasText: /^Magpie$/ }).count(), 0,
        "sharing does not add a default key");
      tablet = page.locator("#gatewayKeys .acc[data-key]", { hasText: "Travel" });
      await tablet.waitFor();
      await confirmKeyAction(page, tablet, w.remove);
      await page.waitForFunction(() => document.querySelectorAll("#gatewayKeys .acc[data-key]").length === 2);
      await page.setViewportSize({ width: 560, height: 740 });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth), false);
      assert.deepEqual(errors, []);
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-gateway-keys.png`) });
      }
    });
  }
}
