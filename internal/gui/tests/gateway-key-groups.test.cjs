// A gateway key can be held to routing groups (Magic_zero on Discord):
// the key's models menu lists the groups, first, with "Every routing
// group", and a group picked is sent as "group/<id>".
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a gateway key is held to the routing groups picked for it`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 600 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(6000);
      page.setDefaultNavigationTimeout(60000);
      const events = [], errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", fixture(lang, "light", events, { lan: true, groups: true, limits: { server: { models: ["group/fast"] } } }));
      const zh = lang === "zh";
      const w = zh
        ? { label: "此密钥可用的模型", all: "全部模型", none: "无模型", groups: "全部路由组", relay: "Relay 的全部模型" }
        : { label: "Models this key may use", all: "All models", none: "No model", groups: "Every routing group", relay: "Every Relay model" };
      await page.goto("http://magpie.test/?view=gateway");
      await page.locator("#gatewayKeys .acc[data-key]").last().waitFor();
      const row = (id) => page.locator(`#gatewayKeys .acc[data-key="${id}"]`);
      const badge = (id) => row(id).getByRole("button", { name: w.label, exact: true });
      // a key held to one group says which
      assert.equal((await badge("server").textContent()).trim(), "group/fast");
      const view = page.locator("#view-gateway");
      const scrolled = async () => [await view.evaluate((v) => v.scrollTop), await page.evaluate(() => window.scrollY)];
      const at = await scrolled();
      await row("work").hover();
      await badge("work").click();
      const menu = page.locator(".proto-menu");
      await menu.waitFor();
      assert.equal(await page.locator("select").count(), 0, "no native select");
      // the groups come first, after All models and No model, then each
      // provider's models
      const names = (await menu.locator(".pm-item").allTextContents()).map((s) => s.trim());
      const at0 = (s) => names.findIndex((n) => n.startsWith(s));
      assert(at0(w.all) === 0, names.join(" | "));
      assert(at0(w.none) === 1, names.join(" | "));
      assert(at0(w.groups) === 2, names.join(" | "));
      assert(at0("Coding") > at0(w.groups) && at0("Fast") > at0("Coding"), names.join(" | "));
      assert(at0(w.relay) > at0("Fast"), names.join(" | "));
      assert.match(await menu.locator(".pm-item", { hasText: "Coding" }).textContent(), /group\/coding/);
      await menu.locator(".pm-item", { hasText: "Coding" }).click();
      await menu.locator(".pm-item", { hasText: "GPT-5" }).click();
      await page.keyboard.press("Escape");
      await menu.waitFor({ state: "detached" });
      assert.deepEqual(events.filter((e) => e.action === "models-key").map((e) => e.body), [{ key: "work", models: ["group/coding", "openai/gpt-5"] }]);
      // the key held to group/fast has it ticked; Every routing group picks group/*
      await badge("server").click();
      await menu.waitFor();
      assert.deepEqual((await menu.locator(".pm-item.on").allTextContents()).map((s) => s.trim()), ["Fastgroup/fast"]);
      await menu.locator(".pm-item", { hasText: w.groups }).click();
      await page.keyboard.press("Escape");
      await menu.waitFor({ state: "detached" });
      assert.deepEqual(events.filter((e) => e.action === "models-key").at(-1).body, { key: "server", models: ["group/*", "group/fast"] });
      assert.deepEqual(await scrolled(), at, "a click moved the page");
      // A red here under load (#1974) could not be made again: every rule
      // these match, and app.css's own button reset, set border: 0, so a
      // style other than none means app.css wasn't applied to the element
      // or the element isn't one these rules know. The message says which:
      // each element's tag, class, place and style, and each stylesheet with
      // how many rules it holds.
      const lefts = await page.locator("#gatewayKeys .key-models, .proto-menu .pm-item").evaluateAll((els) => {
        const sheets = [...document.styleSheets].map((s) => { try { return (s.href || "<style>").replace(location.origin, "") + ":" + s.cssRules.length; } catch (e) { return s.href + ":" + e.name; } });
        const bad = els.map((e) => ({ e, s: getComputedStyle(e) })).filter(({ s }) => s.borderLeftStyle !== "none")
          .map(({ e, s }) => ({ tag: e.tagName, cls: e.className, in: e.parentElement?.className, connected: e.isConnected, style: s.borderLeftStyle, width: s.borderLeftWidth, color: s.borderLeftColor }));
        return { n: els.length, bad, sheets };
      });
      assert.deepEqual(lefts.bad, [], "a coloured left border: " + JSON.stringify(lefts));
      assert.deepEqual(errors, []);
    });
  }
}
