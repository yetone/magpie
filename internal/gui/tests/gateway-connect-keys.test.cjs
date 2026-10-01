const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture, confirmKeyAction } = require("./fixtures/caller-keys.cjs");

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const locale of ["en", "zh"]) {
    test(`${engine} ${locale}: Connect uses the selected network address and caller key`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(6000);
      const events = [], errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", fixture(locale, "dark", events));
      const w = locale === "zh" ? { on: "开启", off: "关闭", gateway: "网关", disable: "停用密钥", enable: "启用密钥", remove: "移除", rotate: "轮换密钥", create: "创建", name: "网关密钥名称" }
        : { on: "On", off: "Off", gateway: "Gateway", disable: "Disable key", enable: "Enable key", remove: "Remove", rotate: "Rotate key", create: "Create", name: "Gateway key name" };
      const snippet = page.locator("#connect .snip");
      const pick = async (id, name) => {
        await page.locator(id).click();
        await page.locator(".proto-menu .pm-item", { hasText: name }).click();
      };
      const expectSecret = (secret) => page.waitForFunction((secret) => document.querySelector("#connect .snip")?.textContent.includes(secret), secret);
      const row = (id) => page.locator(`#gatewayKeys .acc[data-key="${id}"]`);
      await page.goto("http://magpie.test/?view=gateway");
      await snippet.waitFor();
      assert.equal(await page.locator("#connectAddress").count(), 0, "no LAN addresses before sharing is on");
      assert.equal(await page.locator("#gatewayKeysBlock").isVisible(), false, "most users never share");
      assert.equal(await page.locator("#connectKey").count(), 0, "local-only users see no gateway key picker");
      assert.equal(await page.locator("#connect").getByText(locale === "zh" ? "API 密钥" : "API key", { exact: true }).count(), 1);
      await expectSecret('OPENAI_API_KEY=magpie');
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${locale}-local-connect.png`) });
      }

      await page.locator("#prefs").click();

      await page.locator("#setTab-network").click();
      const on = page.locator("#lanList").getByRole("button", { name: w.on, exact: true });
      await on.waitFor();
      const bounds = await on.boundingBox();
      await page.mouse.move(500, 400);
      await page.mouse.wheel(0, bounds.y - 250);
      await on.click();
      await page.locator("#lanList .lan-address-row").waitFor();
      await page.locator("#nav").getByRole("button", { name: w.gateway, exact: true }).click();
      await page.locator("#gatewayKeysBlock").waitFor({ state: "visible" });
      await page.locator("#connectKey").click();
      assert.equal(await page.locator(".proto-menu .pm-name", { hasText: /^Magpie$/ }).count(), 1);
      assert.equal(await page.locator(".proto-menu .pm-name", { hasText: /^magpie$/ }).count(), 0,
        "the arbitrary local token must not look like a second named Magpie key");
      if (process.env.ARTIFACT_DIR) {
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${locale}-shared-key-picker.png`) });
      }
      await page.keyboard.press("Escape");
      const staleKeys = await page.locator("#gatewayKeys .acc[data-key]").evaluateAll((rows) => rows.map((r) => ({
        id: r.dataset.key, name: r.querySelector(".rename").textContent, masked: r.querySelector(".plan").textContent,
      })));
      let releaseRead, readStarted;
      const held = new Promise((resolve) => { releaseRead = resolve; });
      const started = new Promise((resolve) => { readStarted = resolve; });
      const lateList = async (route) => {
        readStarted();
        await held;
        await route.fulfill({ json: { keys: staleKeys } });
      };
      await page.route("**/api/caller-keys", lateList);
      await page.locator("#prefs").click();
      await page.locator("#setTab-network").click();
      await page.locator("#lanList").waitFor();
      await page.locator("#nav").getByRole("button", { name: w.gateway, exact: true }).click();
      await started;
      await row("server").getByRole("button", { name: w.disable, exact: true }).click();
      await page.locator('#gatewayKeys .acc.off[data-key="server"]').waitFor();
      const reply = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/caller-keys");
      releaseRead();
      await reply;
      await page.waitForTimeout(100);
      assert.equal(await page.locator('#gatewayKeys .acc.off[data-key="server"]').count(), 1,
        "a late list response cannot undo a key action");
      await page.unroute("**/api/caller-keys", lateList);
      await row("server").getByRole("button", { name: w.enable, exact: true }).click();
      await row("server").getByRole("button", { name: w.disable, exact: true }).waitFor();
      await pick("#connectKey", "Server");
      await expectSecret("fixture-server");

      await page.locator("#connectAddress").waitFor();
      await pick("#connectAddress", "10.0.0.10");
      await expectSecret("fixture-server");
      await row("work").getByRole("button", { name: w.disable, exact: true }).click();
      await page.locator('#gatewayKeys .acc.off[data-key="work"]').waitFor();
      await page.locator("#connectKey").click();
      assert.equal(await page.locator(".proto-menu .pm-name", { hasText: /^Work$/ }).count(), 0);
      assert.equal(await page.locator(".proto-menu .pm-name", { hasText: /^Magpie$/ }).count(), 1,
        "the default key is named Magpie");
      assert.equal(await page.locator(".proto-menu .pm-name", { hasText: /^magpie$/ }).count(), 0, "arbitrary loopback tokens cannot be selected for LAN");
      await page.keyboard.press("Escape");
      await row("server").getByRole("button", { name: w.disable, exact: true }).click();
      await expectSecret("fixture-laptop");
      await row("server").getByRole("button", { name: w.enable, exact: true }).click();
      await row("server").getByRole("button", { name: w.disable, exact: true }).waitFor();
      await pick("#connectKey", "Server");
      await expectSecret("fixture-server");

      for (const api of ["OpenAI", "Responses", "Anthropic", "Gemini"]) {
        await page.locator("#connect").getByRole("button", { name: api, exact: true }).click();
        const base = "http://10.0.0.10:3999" + (["OpenAI", "Responses"].includes(api) ? "/v1" : "");
        assert.equal(await page.locator("#connectAddress code").textContent(), base);
        for (const dialect of ["Shell", "curl", "Python", "Node"]) {
          await page.locator("#connect").getByRole("button", { name: dialect, exact: true }).click();
          const code = await snippet.textContent();
          assert(code.includes(base), `${api} ${dialect} must use the selected address`);
          assert(code.includes("fixture-server"), `${api} ${dialect} must use the selected key`);
          assert(!code.includes('"magpie"'), "named key examples never fall back to magpie");
        }
      }
      await row("server").locator(".rename").click();
      await page.locator("#gatewayKeys .rename-in").fill("Local network");
      await page.locator("#gatewayKeys .rename-in").press("Enter");
      await row("server").getByRole("button", { name: "Local network", exact: true }).waitFor();
      await page.locator("#connectKey").click();
      assert.equal(await page.locator(".proto-menu .pm-name", { hasText: /^Local network$/ }).count(), 1,
        "custom key names are not translated as interface labels");
      await page.keyboard.press("Escape");
      await confirmKeyAction(page, row("server"), w.rotate);
      await expectSecret("fixture-rotated-2");
      assert.equal(await row("server").locator(".rename").textContent(), "Local network");
      assert(!(await snippet.textContent()).includes("fixture-server"), "rotation refreshes the selected secret");
      const code = await snippet.textContent();
      await page.locator("#connect .snip-wrap .copy").click();
      await page.waitForFunction(() => document.querySelector("#connect .snip-wrap .copy.done"));
      assert(events.some((e) => e.action === "clipboard" && e.body.text === code));
      for (const width of [1000, 560, 390]) {
        await page.setViewportSize({ width, height: 760 });
        await page.waitForTimeout(200);
        if (width >= 560) {
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
        }
        // Below the native window's minimum, check the changed cards, not its header.
        assert.equal(await page.evaluate(() => ["#connect", "#gatewayKeys .accts"].every((s) => {
          const e = document.querySelector(s);
          return e.scrollWidth <= e.clientWidth;
        })), true);
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${locale}-connect-${width}.png`) });
        }
      }
      await page.setViewportSize({ width: 1000, height: 760 });
      await confirmKeyAction(page, row("server"), w.remove);
      await expectSecret("fixture-laptop");
      assert(!(await snippet.textContent()).includes("fixture-rotated-2"), "removal clears the selected credential");
      // A remote connection without enabled keys must not copy a broken command.
      for (const id of ["laptop", "work"]) {
        await confirmKeyAction(page, row(id), w.remove);
        await row(id).waitFor({ state: "detached" });
      }
      const lan = page.locator('#gatewayKeys .acc[data-key^="lan-key"]');
      await confirmKeyAction(page, lan, w.remove);
      await lan.waitFor({ state: "detached" });
      assert.equal(await page.locator("#connect .snip-wrap .copy").count(), 0);
      assert.equal(await page.locator("#connectKey").count(), 0);
      await page.locator("#addGatewayKey").click();
      await page.getByRole("textbox", { name: w.name, exact: true }).fill("Replacement");
      await page.getByRole("button", { name: w.create, exact: true }).click();
      await expectSecret("fixture-created-2");
      assert.equal(await page.locator("#connectKey code").textContent(), "Replacement");
      await page.locator("#prefs").click();
      await page.locator("#setTab-network").click();
      await page.locator("#lanList .lan-address-row").waitFor();
      await page.locator("#lanList").getByRole("button", { name: w.off, exact: true }).click();
      await page.locator("#lanList .lan-address-row").waitFor({ state: "detached" });
      await page.locator("#nav").getByRole("button", { name: w.gateway, exact: true }).click();
      await page.locator("#connectAddress").waitFor({ state: "detached" });
      assert.equal(await page.locator("#gatewayKeysBlock").isVisible(), false);
      assert.equal(await page.locator("#connectKey").count(), 0);
      assert.equal(await page.locator("#connect").getByText(locale === "zh" ? "API 密钥" : "API key", { exact: true }).count(), 1);
      assert((await snippet.textContent()).includes("http://127.0.0.1:3999"));
      assert(!(await snippet.textContent()).includes("fixture-created-2"), "sharing off clears the selected gateway credential");
      assert.deepEqual(errors, []);
    });
  }
}
