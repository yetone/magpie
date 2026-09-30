// Global screenshot privacy: real pages, independent webviews, shared settings.
// Run with Node's test runner and Playwright on the module path; see README.md.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const assets = path.resolve(__dirname, "../assets");
const ADDRESS = "demo.one@example.com";
const TEXT = "ZCode: abc***gh@abcd.com · Codex: demo.one@example.com · o'connor@example.org · 张三@示例.公司 · ***@***.com";
const COMMANDS = ["npx -y @playwright/mcp@0.0.41", "mcp-server-fetch@2025.1.1", "owner/repo@v1.2.3"];
const quotas = [{ provider: "codex", name: "ChatGPT", icon: "openai", user: ADDRESS, plan: "Plus", windows: [{ name: "5 hours", used: 35 }, { name: "7 days", used: 62 }] }];
const providers = { providers: [{ id: "codex", name: "ChatGPT", icon: "openai", models: [], agents: [], fallback: [], headers: {}, keyList: [], account: { agent: "codex", agentName: "Codex", user: ADDRESS, plan: "plus", logins: [{ user: ADDRESS, plan: "plus", active: true, on: true }] } }], presets: [], excluded: [], gateway: { running: true, window: true } };
const usage = { calls: 5, errors: 0, input: 12000, output: 4000, cache_read: 500, cache_write: 100, reasoning: 200, cost: 0.03, unpriced: 0, bucket: "day", series: [{ label: "Sep 29", input: 6000, output: 2000, calls: 3, cost: 0.02 }, { label: "Sep 30", input: 6000, output: 2000, calls: 2, cost: 0.01 }], agents: [{ name: "Codex", calls: 5, input: 12000, output: 4000, cost: 0.03 }], models: [{ name: "gpt-6-sol", calls: 5, input: 12000, output: 4000, cost: 0.03 }] };

function backend(lang, theme, initial = false) {
  const b = { on: initial, posts: [], fail: false, waiters: new Set() };
  b.release = () => { for (const done of b.waiters) done(); };
  b.serve = async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data }).catch(() => {});
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme, web: false, privacyMode: b.on })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/settings/privacy") {
      if (req.method() === "POST") {
        if (b.fail) return route.fulfill({ status: 500, json: { error: "Cannot save settings" } });
        b.on = req.postDataJSON().on; b.posts.push(b.on); b.release();
      } else if (url.searchParams.get("wait") === "1" && url.searchParams.get("on") === (b.on ? "1" : "0")) {
        await new Promise((resolve) => {
          const timer = setTimeout(done, 1500);
          function done() { clearTimeout(timer); b.waiters.delete(done); resolve(); }
          b.waiters.add(done);
        });
      }
      return json({ on: b.on });
    }
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme, privacyMode: b.on } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/usage") return json(usage);
    if (url.pathname === "/api/settings") return json({ lang, theme, privacyMode: b.on, version: "test", dir: "/tmp/magpie", gateway: "http://127.0.0.1:3425", visionModels: [], imageGenModels: [], fx: { rate: 7.2, stale: false } });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
  return b;
}

const masked = (p, on) => p.waitForFunction((on) => document.querySelector("#privacyMode").getAttribute("aria-pressed") === String(on) && !document.querySelector("#privacyMode").disabled, on);
const noEmail = async (p, selector) => assert.doesNotMatch(await p.locator(selector).textContent(), /demo\.one|example|abc\*|gh@|connor|张三|示例/);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: shared screenshot privacy`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) for (const theme of ["light", "dark"]) {
      await t.test(`${lang} ${theme}: both windows, redraws, restore and restart`, async () => {
        const b = backend(lang, theme), errors = [], contexts = [];
        const open = async (panel) => {
          // Separate browser storage represents independently hosted webviews.
          const c = await browser.newContext({ viewport: { width: panel ? 440 : 1000, height: panel ? 560 : 720 }, reducedMotion: "reduce", colorScheme: theme });
          contexts.push(c);
          const p = await c.newPage(); p.setDefaultTimeout(5000);
          p.on("pageerror", (e) => errors.push(e.message));
          await p.route("**/*", b.serve);
          await p.goto("http://magpie.test/?" + (panel ? "mode=panel" : "view=usage"));
          await p.locator(panel ? "#panelQuota .pq-user" : "#subscriptionUsage .user").first().waitFor({ state: "attached" });
          if (panel) await p.locator('#ptabs [data-ptab="usage"]').click();
          return p;
        };
        try {
          const main = await open(false), panel = await open(true);
          await main.evaluate(({ text, commands }) => {
            const d = document.createElement("div"); d.id = "sample"; d.textContent = text; d.title = "abc***gh@abcd.com"; d.setAttribute("aria-label", "demo.one@example.com");
            document.querySelector("#view-usage").append(d);
            const input = document.createElement("input"); input.id = "emailField"; input.value = "demo.one@example.com"; d.after(input);
            const select = document.createElement("select"); select.id = "emailSelect"; const option = document.createElement("option"); option.textContent = "demo.one@example.com"; select.append(option); input.after(select);
            const cmds = document.createElement("div"); cmds.id = "commands"; cmds.textContent = commands.join(" · "); select.after(cmds);
            for (const [i, command] of commands.entries()) { const field = document.createElement("input"); field.id = "command" + i; field.value = command; cmds.append(field); }
          }, { text: TEXT, commands: COMMANDS });
          await panel.evaluate(() => { Object.defineProperty(document, "hidden", { configurable: true, get: () => true }); document.dispatchEvent(new Event("visibilitychange")); });
          await main.locator("#usageMask").click();
          await masked(main, true); await masked(panel, true);
          await panel.evaluate(() => { delete document.hidden; document.dispatchEvent(new Event("visibilitychange")); });
          assert.equal(await main.locator("#usageMask").getAttribute("aria-label"), lang === "zh" ? "隐私模式" : "Privacy mode");
          assert.equal(await main.locator("#commands").textContent(), COMMANDS.join(" · "));
          for (const [i, command] of COMMANDS.entries()) {
            assert.equal(await main.locator("#command" + i).inputValue(), command);
            assert.equal(await main.locator("#command" + i).evaluate((e) => getComputedStyle(e).webkitTextSecurity), "none");
          }
          assert.equal(await main.locator("#sample .pii").count(), 5);
          assert.equal(await main.locator("#sample .pii").first().evaluate((e) => getComputedStyle(e).filter), "blur(2.2px)");
          assert.equal(await main.locator("#sample [data-raw]").count(), 0);
          await noEmail(main, "#subscriptionUsage"); await noEmail(panel, "#panelQuota"); await noEmail(main, "#sample");
          assert.equal(await main.locator("#sample").getAttribute("title"), "••••••••@••••.•••");
          assert.doesNotMatch(await main.locator("#sample").getAttribute("aria-label"), /demo|example/);
          assert.equal(await main.locator("#emailField").inputValue(), ADDRESS, "masking must not alter what is submitted");
          assert.equal(await main.locator("#emailField").evaluate((e) => getComputedStyle(e).webkitTextSecurity), "disc");
          assert.equal(await main.locator("#emailSelect").inputValue(), ADDRESS, "an option's implicit value must stay intact");
          assert.equal(await panel.evaluate(() => localStorage.getItem("magpie.maskEmails")), null, "sync works without shared storage");
          // Existing tooltips can change in place, and transient UI lives outside main.
          await main.evaluate(() => {
            const s = document.querySelector("#sample"); s.title = "new.person@another.example"; s.textContent = "Updated new.person@another.example";
            const pop = document.createElement("div"); pop.id = "transientEmail"; pop.textContent = "pop.person@example.com"; document.body.append(pop);
          });
          await main.waitForFunction(() => !document.querySelector("#sample").title.includes("new.person"));
          assert.doesNotMatch(await main.locator("#sample").textContent(), /new\.person|another/);
          await noEmail(main, "#transientEmail");
          await main.evaluate(() => { document.querySelector("#sample .pii").firstChild.data = "latest.person@updated.example"; });
          await main.waitForFunction(() => !document.querySelector("#sample").textContent.includes("latest.person"));
          // Open another page while masked: subscription names stay hidden.
          await main.locator('[data-view="providers"]').click();
          await main.locator(".row.provider").first().waitFor();
          await noEmail(main, "#providers");
          await main.locator('[data-view="usage"]').click();
          await masked(main, true);
          if (process.env.ARTIFACT_DIR) {
            await main.evaluate(() => { for (const id of ["sample", "emailField", "emailSelect", "transientEmail", "commands"]) document.getElementById(id).hidden = true; });
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await main.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${theme}-privacy-window.png`) });
            await panel.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${theme}-privacy-panel.png`) });
          }
          // Turning off in the panel restores the current, not stale, values in both.
          await panel.locator("#privacyMode").click(); await masked(panel, false); await masked(main, false);
          assert.equal(await main.locator("#sample").textContent(), "Updated latest.person@updated.example");
          assert.equal(await main.locator("#sample").getAttribute("title"), "new.person@another.example");
          assert.equal(await main.locator("#emailSelect").inputValue(), ADDRESS);
          assert.equal(await main.locator("#emailSelect option").getAttribute("value"), null);
          assert((await panel.locator("#panelQuota").textContent()).includes(ADDRESS));
          // Save errors leave the existing setting and visible toggle in agreement.
          b.fail = true; await main.locator("#usageMask").click(); await masked(main, false);
          await main.locator("#status.err").waitFor(); assert.equal(b.on, false); b.fail = false;
          await main.locator("#usageMask").click(); await masked(main, true); await masked(panel, true);
          await main.reload(); await masked(main, true);
          await main.locator("#subscriptionUsage .user").first().waitFor(); await noEmail(main, "#subscriptionUsage");
          // Narrow header, keyboard toggle, and stale general-settings application.
          await main.setViewportSize({ width: 560, height: 630 });
          assert(await main.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), "the narrow header fits before switching language");
          await main.evaluate(() => applyPrefs({ lang: "zh", theme: "dark", privacyMode: false }));
          await masked(main, true);
          assert.equal(await main.locator("#usageMask").getAttribute("aria-label"), "隐私模式");
          assert(await main.locator("#usageMask").isVisible());
          assert(!(await main.locator("#privacyMode").isVisible()));
          assert.equal(await main.locator(".top.cramped .seg button").first().evaluate((e) => getComputedStyle(e).paddingLeft), "8px");
          assert(await main.evaluate(() => document.querySelector(".top").scrollWidth <= document.querySelector(".top").clientWidth));
          await main.locator("#usageMask").focus(); await main.keyboard.press("Space"); await masked(main, false); await masked(panel, false);
          assert.deepEqual(b.posts, [true, false, true, false]);
          assert.deepEqual(errors, []);
        } finally { b.release(); for (const c of contexts) await c.close(); }
      });
    }
    await t.test("Routing and Settings share the toggle; idle refresh does not scan", async () => {
      const b = backend("en", "light"), c = await browser.newContext();
      try {
        const p = await c.newPage(); await p.route("**/*", b.serve);
        await p.goto("http://magpie.test/?view=routing");
        await p.evaluate((text) => { const s = document.createElement("div"); s.id = "routingEmail"; s.textContent = text; s.title = "abc***gh@abcd.com"; document.querySelector("#view-routing").append(s); }, TEXT);
        await p.locator("#rtMask").click(); await masked(p, true);
        await noEmail(p, "#routingEmail");
        assert.equal(await p.locator("#rtMask").getAttribute("aria-label"), "Privacy mode");
        assert.equal(await p.locator("#routingEmail .pii").count(), 5);
        assert.equal(await p.locator("#routingEmail").getAttribute("title"), "••••••••@••••.•••");
        await p.evaluate(() => { const s = document.createElement("span"); s.id = "later"; s.textContent = "x***y@z.cn"; document.querySelector("#routingEmail").append(s); });
        await p.waitForFunction(() => document.querySelector("#later .pii"));
        await p.locator("#rtMask").click(); await masked(p, false);
        assert.equal(await p.locator("#routingEmail").textContent(), TEXT + "x***y@z.cn");
        assert.equal(await p.locator("#routingEmail .pii").count(), 0);
        assert.equal(await p.evaluate(() => {
          let walks = 0; const original = document.createTreeWalker;
          document.createTreeWalker = function (...args) { walks++; return original.apply(this, args); };
          try { for (let i = 0; i < 10; i++) window.refreshPrivacy(); } finally { document.createTreeWalker = original; }
          return walks;
        }), 0, "repeated preference refresh while off must not walk the page");
        await p.locator("#prefs").click();
        await p.locator("#privacySetting").click(); await masked(p, true);
        assert.equal(await p.locator("#privacySetting").getAttribute("aria-label"), "Privacy mode");
        assert.equal(await p.locator("#rtMask").getAttribute("aria-pressed"), "true");
        await noEmail(p, "#routingEmail");
        await p.locator("#trayUsagePick button").click();
        await p.locator(".proto-menu .pii").waitFor();
        await noEmail(p, ".proto-menu");
        if (process.env.ARTIFACT_DIR) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-settings-privacy-picker.png`) });
        await p.keyboard.press("Escape");
        await p.locator("#privacySetting").click(); await masked(p, false);
        assert.equal(await p.locator("#privacySetting").getAttribute("aria-label"), "Privacy mode");
        assert.deepEqual(b.posts, [true, false, true, false]);
      } finally { b.release(); await c.close(); }
    });
    for (const initial of [false, true]) await t.test(`migrate the existing Hide emails preference (shared=${initial})`, async () => {
      const b = backend("en", "light", initial), c = await browser.newContext();
      try {
        await c.addInitScript(() => localStorage.setItem("magpie.maskEmails", "1"));
        const p = await c.newPage(); await p.route("**/*", b.serve);
        await p.goto("http://magpie.test/?view=usage"); await masked(p, true);
        assert.equal(b.on, true); assert.deepEqual(b.posts, initial ? [] : [true]);
        assert.equal(await p.evaluate(() => localStorage.getItem("magpie.maskEmails")), null);
      } finally { b.release(); await c.close(); }
    });
  });
}
