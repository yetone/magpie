const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/users.cjs");

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: create users, issue caller keys and filter their usage`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(6000);
      const events = [], errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", fixture(lang, "light", events));
      const w = lang === "zh"
        ? { createUser: "创建用户", createKey: "创建 Key", user: "用户名称", key: "调用 Key 名称", remove: "移除", copy: "复制调用 Key", disable: "停用 Key", enable: "启用 Key", userOff: "停用用户", userOn: "启用用户", requests: "请求", usage: "用量" }
        : { createUser: "Create user", createKey: "Create key", user: "User name", key: "Caller key name", remove: "Remove", copy: "Copy caller key", disable: "Disable key", enable: "Enable key", userOff: "Disable user", userOn: "Enable user", requests: "Requests", usage: "Usage" };
      await page.goto("http://magpie.test/?view=gateway");
      await page.locator(".gateway-user").last().waitFor();
      await page.locator("#addGatewayUser").click();
      // Invalid input stays editable, and repeated Enter cannot submit twice.
      await page.getByRole("button", { name: w.createUser, exact: true }).click();
      await page.waitForFunction(() => document.querySelector("#gatewayUsers .adding .primary")?.disabled === false);
      await page.getByRole("textbox", { name: w.user, exact: true }).fill("Charlie");
      await page.getByRole("textbox", { name: w.user, exact: true }).press("Enter");
      await page.locator(".gateway-user", { hasText: "Charlie" }).waitFor();
      assert.equal(await page.locator(".gateway-user").count(), 3);
      let charlie = page.locator(".gateway-user", { hasText: "Charlie" });
      await charlie.getByRole("button", { name: "＋ " + w.createKey, exact: true }).click();
      await page.getByRole("textbox", { name: w.key, exact: true }).fill("Tablet");
      await page.getByRole("button", { name: w.createKey, exact: true }).click();
      await charlie.locator(".gateway-key").waitFor();
      await charlie.getByRole("button", { name: "＋ " + w.createKey, exact: true }).click();
      await page.getByRole("textbox", { name: w.key, exact: true }).fill("Desktop");
      await page.getByRole("button", { name: w.createKey, exact: true }).click();
      await charlie.locator(".gateway-key").nth(1).waitFor();
      assert.equal(await charlie.locator(".gateway-key").count(), 2);
      const tablet = charlie.locator(".gateway-key", { hasText: "Tablet" });
      await tablet.getByRole("button", { name: w.copy, exact: true }).click();
      await page.waitForFunction(() => document.querySelector(".gateway-key .copy.done"));
      assert(events.some((e) => e.action === "clipboard" && e.body.text === "sk-magpie-user-test-copy"));
      await tablet.getByRole("button", { name: w.disable, exact: true }).click();
      await tablet.getByRole("button", { name: w.enable, exact: true }).waitFor();
      await tablet.getByRole("button", { name: w.enable, exact: true }).click();
      await tablet.getByRole("button", { name: w.disable, exact: true }).waitFor();
      await charlie.getByRole("button", { name: w.userOff, exact: true }).click();
      await charlie.getByRole("button", { name: w.userOn, exact: true }).waitFor();
      await charlie.getByRole("button", { name: w.userOn, exact: true }).click();
      await tablet.getByRole("button", { name: "Tablet", exact: true }).click();
      await charlie.locator(".rename-in").fill("Travel");
      await charlie.locator(".rename-in").press("Enter");
      await charlie.getByRole("button", { name: "Travel", exact: true }).waitFor();
      await charlie.locator(".gateway-key", { hasText: "Travel" }).getByRole("button", { name: w.remove, exact: true }).click();
      await page.waitForFunction(() => [...document.querySelectorAll(".gateway-user")].find((u) => u.textContent.includes("Charlie"))?.querySelectorAll(".gateway-key").length === 1);

      await page.locator("#nav").getByRole("button", { name: w.usage, exact: true }).click();
      await page.locator("#usageUsers .row").last().waitFor();
      assert.equal(await page.locator("#usageUsers .row").count(), 3);
      assert.equal(await page.locator("#usageCallerKeys .row").count(), 3);
      assert.match(await page.locator("#usageUsers").textContent(), /Alice/);
      assert.match(await page.locator("#usageCallerKeys").textContent(), /Laptop/);
      await page.locator("#usageTab").getByRole("button", { name: w.requests, exact: true }).click();
      await page.locator("#ledUser").click();
      await page.locator(".sess-menu .pm-item", { hasText: "Alice" }).click();
      await page.waitForFunction(() => document.querySelectorAll("#ledWrap tbody tr").length === 2);
      await page.locator("#ledCallerKey").click();
      await page.locator(".sess-menu .pm-item", { hasText: "Alice · Laptop" }).click();
      await page.waitForFunction(() => document.querySelectorAll("#ledWrap tbody tr").length === 1);
      assert.match(await page.locator("#ledWrap").textContent(), /Alice.*Laptop/);
      await page.locator("#ledExport").click();
      await page.waitForTimeout(150);
      const exported = events.find((e) => e.action === "export");
      assert(exported?.query.includes("user=alice") && exported.query.includes("callerKey=laptop"), "CSV retains both filters");
      await page.reload();
      await page.locator("#nav").getByRole("button", { name: lang === "zh" ? "网关" : "Gateway", exact: true }).click();
      await page.locator(".gateway-user", { hasText: "Charlie" }).waitFor();
      charlie = page.locator(".gateway-user", { hasText: "Charlie" });
      await charlie.locator(".acc").first().getByRole("button", { name: w.remove, exact: true }).click();
      await page.waitForFunction(() => document.querySelectorAll(".gateway-user").length === 2);
      await page.setViewportSize({ width: 560, height: 740 });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth), false);
      assert.deepEqual(errors, []);
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-gateway-users.png`) });
      }
    });
  }
}
