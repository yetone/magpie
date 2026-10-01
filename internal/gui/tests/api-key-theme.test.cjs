const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

async function palette(page, selectors) {
  return page.evaluate((selectors) => {
    const root = getComputedStyle(document.documentElement);
    const rgb = (name) => {
      const hex = root.getPropertyValue(name).trim();
      return `rgb(${[1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16)).join(", ")})`;
    };
    const colors = Object.fromEntries(["bg", "card", "card-2", "fg", "fg-2", "muted", "ctl-fg", "pill", "line", "accent", "accent-fg"]
      .map((name) => [name, rgb("--" + name)]));
    const elements = Object.fromEntries(Object.entries(selectors).map(([name, selector]) => {
      const s = getComputedStyle(document.querySelector(selector));
      return [name, { color: s.color, bg: s.backgroundColor, border: s.borderTopColor }];
    }));
    return { colors, elements };
  }, selectors);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const theme of (process.env.THEME ? [process.env.THEME] : ["light", "dark", "system"])) {
    test(`${engine} ${theme}: API key controls follow the global palette`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const context = await browser.newContext({ viewport: { width: 1000, height: 760 }, reducedMotion: "reduce", colorScheme: "light" });
      const page = await context.newPage();
      page.setDefaultTimeout(6000);
      const events = [], errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", fixture("en", theme, events, { lan: true }));
      await page.goto("http://magpie.test/?view=gateway");
      await page.locator('#gatewayKeys .acc[data-key="server"] .dot').click();
      await page.locator('#gatewayKeys .acc.off[data-key="server"]').waitFor();
      await page.locator("#addGatewayKey").click();
      await page.locator('#gatewayKeys .acc[data-key="laptop"] .rename').click();
      await page.locator("#gatewayKeys .rename-in").waitFor();
      await page.mouse.move(500, 20);
      const gatewaySelectors = { card: "#gatewayKeys .accts", enabled: '#gatewayKeys .acc[data-key="work"] .dot',
        disabled: "#gatewayKeys .acc.off .dot", masked: "#gatewayKeys .acc .plan", form: "#gatewayKeys .adding",
        input: "#gatewayKeys .adding input", rename: "#gatewayKeys .rename-in", create: "#gatewayKeys .primary", rotate: '#gatewayKeys .acc[data-key="work"] .text', keyPick: "#connectKey" };
      const gatewayPaints = [];
      for (const scheme of ["light", "dark"]) {
        await page.emulateMedia({ colorScheme: scheme });
        await page.waitForTimeout(200);
        const { colors: c, elements: e } = await palette(page, gatewaySelectors);
        assert.equal(e.card.bg, c.card);
        assert.equal(e.card.border, c.line);
        assert.equal(e.enabled.bg, c.fg);
        assert.equal(e.enabled.color, c.card);
        assert.equal(e.disabled.color, c.muted);
        assert.equal(e.masked.bg, c.pill);
        assert.equal(e.masked.color, c.muted);
        assert.equal(e.form.bg, c["card-2"]);
        for (const input of [e.input, e.rename]) {
          assert.equal(input.bg, c.card, "key inputs must not use the browser's default background");
          assert.equal(input.color, c.fg);
        }
        assert.equal(e.rotate.color, c["ctl-fg"]); // a text button, in a control's quiet text (#477)
        assert.equal(e.keyPick.bg, "rgba(0, 0, 0, 0)");
        assert.equal(e.create.bg, c.accent);
        assert.equal(e.create.color, c["accent-fg"]);
        gatewayPaints.push(c.bg);
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${theme}-${scheme}-keys.png`) });
        }
      }
      assert.equal(gatewayPaints[0] === gatewayPaints[1], theme !== "system", "only System follows an OS palette change");

      await page.locator('#gatewayKeys .acc[data-key="work"]').getByRole("button", { name: "Rotate key", exact: true }).click();
      await page.locator("#modal .lib-confirm").waitFor();
      for (const scheme of ["light", "dark"]) {
        await page.emulateMedia({ colorScheme: scheme });
        await page.waitForTimeout(200);
        const { colors: c, elements: e } = await palette(page, {
          dialog: "#modal .dialog", message: "#modal .lib-confirm", confirm: "#modal .primary",
        });
        assert.equal(e.dialog.bg, c.card);
        assert.equal(e.message.color, c["fg-2"]);
        assert.equal(e.confirm.bg, c.accent);
        assert.equal(e.confirm.color, c["accent-fg"]);
        if (process.env.ARTIFACT_DIR) {
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${theme}-${scheme}-confirm.png`) });
        }
      }
      await page.locator("#modal").getByRole("button", { name: "Cancel", exact: true }).click();
      await page.locator("#modal").waitFor({ state: "hidden" });
      assert.equal(events.filter((e) => e.action === "rotate-key").length, 0);

      await page.goto("http://magpie.test/?view=settings&tab=network");
      const on = page.locator("#lanList").getByRole("button", { name: "On", exact: true });
      await on.waitFor();
      const bounds = await on.boundingBox();
      await page.mouse.move(500, 400);
      await page.mouse.wheel(0, bounds.y - 250);
      await on.click();
      await page.locator("#lanList .lan-url").waitFor();
      await page.mouse.move(500, 20);
      for (const scheme of ["light", "dark"]) {
        await page.emulateMedia({ colorScheme: scheme });
        await page.waitForTimeout(200);
        const { colors: c, elements: e } = await palette(page, { card: "#lanList", text: "#lanList .row.pref:last-child .name",
          key: "#lanList .lan-url", copy: "#lanList .row.pref:last-child .copy" });
        assert.equal(e.card.bg, c.card);
        assert.equal(e.text.color, c.fg);
        assert.equal(e.key.color, c["fg-2"]);
        assert.equal(e.copy.color, c.muted);
      }
      if (theme === "system") {
        await page.mouse.move(500, 400);
        await page.mouse.wheel(0, -5000);
        for (const choice of ["Dark", "Light", "System"]) {
          await page.locator("#setTab-general").click();
          await page.locator("#themeSegs").getByRole("button", { name: choice, exact: true }).click();
          await page.waitForFunction((want) => (document.documentElement.dataset.theme || "system") === want, choice.toLowerCase());
          await page.waitForFunction(() => !document.documentElement.classList.contains("theming"));
          await page.locator("#setTab-network").click();
          const { colors: c, elements: e } = await palette(page, { card: "#lanList", key: "#lanList .lan-url" });
          assert.equal(e.card.bg, c.card);
          assert.equal(e.key.color, c["fg-2"]);
        }
        await page.reload();
        await page.locator("#themeSegs button.on").waitFor({ state: "attached" });
        assert.equal(await page.locator("#themeSegs button.on").textContent(), "System");
      }
      assert.deepEqual(errors, []);
    });
  }
}
