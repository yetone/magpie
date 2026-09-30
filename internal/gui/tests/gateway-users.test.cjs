const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/users.cjs");

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: manage named caller keys and filter their usage`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(6000);
      const events = [], errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", fixture(lang, "light", events));
      const w = lang === "zh"
        ? { create: "创建密钥", name: "密钥名称", remove: "移除", copy: "复制 API 密钥", disable: "停用密钥", enable: "启用密钥", requests: "请求", usage: "用量" }
        : { create: "Create key", name: "API key name", remove: "Remove", copy: "Copy API key", disable: "Disable key", enable: "Enable key", requests: "Requests", usage: "Usage" };
      await page.goto("http://magpie.test/?view=gateway");
      await page.locator("#gatewayKeys .acc[data-key]").last().waitFor();
      assert.equal(await page.locator("#gatewayKeys .accts").count(), 1);
      await page.locator("#addGatewayKey").click();
      await page.getByRole("button", { name: w.create, exact: true }).click();
      await page.waitForFunction(() => document.querySelector("#gatewayKeys .adding .primary")?.disabled === false);
      await page.getByRole("textbox", { name: w.name, exact: true }).fill("Tablet");
      await page.getByRole("textbox", { name: w.name, exact: true }).press("Enter");
      let tablet = page.locator("#gatewayKeys .acc[data-key]", { hasText: "Tablet" });
      await tablet.waitFor();
      assert.equal(await page.locator("#gatewayKeys .acc[data-key]").count(), 4);
      await tablet.getByRole("button", { name: w.copy, exact: true }).click();
      await page.waitForFunction(() => document.querySelector("#gatewayKeys .copy.done"));
      assert(events.some((e) => e.action === "clipboard" && e.body.text === "sk-magpie-user-test-copy"));
      await tablet.getByRole("button", { name: w.disable, exact: true }).click();
      await tablet.getByRole("button", { name: w.enable, exact: true }).waitFor();
      await tablet.getByRole("button", { name: w.enable, exact: true }).click();
      await tablet.getByRole("button", { name: w.disable, exact: true }).waitFor();
      await tablet.getByRole("button", { name: "Tablet", exact: true }).click();
      await page.locator("#gatewayKeys .rename-in").fill("Travel");
      await page.locator("#gatewayKeys .rename-in").press("Enter");
      tablet = page.locator("#gatewayKeys .acc[data-key]", { hasText: "Travel" });
      await tablet.waitFor();
      await page.locator("#nav").getByRole("button", { name: w.usage, exact: true }).click();
      await page.locator("#usageKeys .row").last().waitFor();
      assert.equal(await page.locator("#usageKeys .row").count(), 3);
      assert.match(await page.locator("#usageKeys").textContent(), /Laptop/);
      assert.equal(await page.locator("#usageKeysHead").textContent(), lang === "zh" ? "API 密钥" : "API keys");
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
      const share = page.locator("#lanList").getByRole("button", { name: lang === "zh" ? "开启" : "On", exact: true });
      await share.waitFor();
      // Real wheel input lets the page remember the reader's scroll position.
      const bounds = await share.boundingBox();
      await page.mouse.move(500, 400);
      await page.mouse.wheel(0, bounds.y - 250);
      await share.click();
      await page.locator("#lanList .lan-address-row").waitFor();
      const keyRow = page.locator("#lanList .row.pref").last();
      const before = await keyRow.locator("code").textContent();
      assert.match(await keyRow.textContent(), /Local network/);
      await keyRow.getByRole("button", { name: w.copy, exact: true }).click();
      await page.waitForFunction(() => document.querySelector("#lanList .copy.done"));
      assert(events.some((e) => e.action === "clipboard" && e.body.text === "fixture-lan-1"));
      await keyRow.getByRole("button", { name: lang === "zh" ? "换新 Key" : "New key", exact: true }).click();
      await page.waitForFunction((masked) => {
        const code = document.querySelector("#lanList .row.pref:last-child code");
        return code && code.textContent !== masked;
      }, before);
      const rotated = await keyRow.locator("code").textContent();
      await keyRow.getByRole("button", { name: w.copy, exact: true }).click();
      await page.waitForFunction(() => document.querySelector("#lanList .copy.done"));
      assert(events.some((e) => e.action === "clipboard" && e.body.text === "fixture-lan-2"));
      assert(events.some((e) => e.action === "lan" && e.body.on && e.body.newKey));
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-lan-keys.png`) });
      }
      await page.setViewportSize({ width: 560, height: 740 });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth), false);
      if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-lan-keys-narrow.png`) });
      await page.setViewportSize({ width: 1000, height: 760 });
      await page.locator("#lanList").getByRole("button", { name: lang === "zh" ? "关闭" : "Off", exact: true }).click();
      await page.locator("#lanList .lan-address-row").waitFor({ state: "detached" });
      await share.click();
      await page.locator("#lanList .lan-address-row").waitFor();
      assert.equal(await keyRow.locator("code").textContent(), rotated, "toggling sharing must not rotate the key");
      await page.locator("#nav").getByRole("button", { name: lang === "zh" ? "网关" : "Gateway", exact: true }).click();
      await page.locator("#gatewayKeys .acc[data-key]").last().waitFor();
      assert.equal(await page.locator("#gatewayKeys .acc[data-key]").count(), 5);
      assert.equal(await page.locator("#gatewayKeys .acc[data-key]", { hasText: "Local network" }).locator(".plan").textContent(), rotated);
      assert(events.some((e) => e.action === "lan" && e.body.on === true && !e.body.newKey));
      tablet = page.locator("#gatewayKeys .acc[data-key]", { hasText: "Travel" });
      await tablet.waitFor();
      await tablet.getByRole("button", { name: w.remove, exact: true }).click();
      await page.waitForFunction(() => document.querySelectorAll("#gatewayKeys .acc[data-key]").length === 4);
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
