// Run with Node's test runner and Playwright on the module path; see README.md.
// ClinePass's DeepSeek models pinned to DeepSeek's own API (White Immortal
// on Discord), and its GLM models to Z.ai's (sdytman): the editor of a Cline provider has the tick, as saved; Save
// posts pinUpstream as ticked. The Cline plugin's provider, an account's
// editor, has it too (ARNO on Discord). Another provider's editor has no
// such tick. In English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const cline = {
  id: "clinepass", name: "ClinePass", icon: "cline", preset: "clinepass", cline: true, pinUpstream: false,
  chat: "https://api.cline.bot/api/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "cline-pass/deepseek-v4-pro", name: "DeepSeek V4 Pro", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "clp_…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", maxConcurrency: null,
};
// the Cline plugin's provider (ARNO on Discord): an account's editor, pinned
// as saved
const plugin = {
  id: "cline-plugin", name: "Cline", icon: "cline", cline: true, pinUpstream: true,
  chat: "plugin://cline/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "cline-pass/deepseek-v4-pro", name: "DeepSeek V4 Pro", on: true }], agents: [], fallback: [], headers: {},
  key: { set: false, masked: "", optional: true }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", maxConcurrency: null, ready: true,
  account: { agent: "cline-plugin", agentName: "Cline", agentIcon: "cline", builtin: "", user: "a@cline", logins: [{ user: "a@cline", active: true, on: true }] },
};
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", maxConcurrency: null,
};

function serve(lang, posts) {
  const providers = { providers: [cline, plugin, relay], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/provider/") && route.request().method() === "POST") {
      posts.push({ action: url.pathname.slice("/api/provider/".length), body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = { en: { tick: "DeepSeek and GLM models only from their makers' own APIs", save: "Save" }, zh: { save: "保存" } };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a Cline provider's DeepSeek models are pinned from its editor`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posts = [];
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      const zh = await page.evaluate(() => [
        "DeepSeek and GLM models only from their makers' own APIs", "Upstream",
        "Since 2026-11-06 Cline refuses a pinned DeepSeek or GLM request with 403, so magpie sends them unpinned and Cline picks the host; only glm-5.3-flash still asks Z.ai",
      ].map((k) => I18N.zh[k]));
      assert(zh.every(Boolean), "every string has its Chinese");
      for (const l of ["zh-TW", "ja", "de"]) {
        const has = await page.evaluate(([l, k]) => !!I18N[l][k], [l, "Since 2026-11-06 Cline refuses a pinned DeepSeek or GLM request with 403, so magpie sends them unpinned and Cline picks the host; only glm-5.3-flash still asks Z.ai"]);
        assert(has, `the Upstream note has its ${l}`);
      }
      if (lang === "zh") w.tick = zh[0];

      await page.locator(".row.provider", { hasText: "ClinePass" }).click();
      // Upstream is under Advanced
      { const adv = page.locator(".editor details.advanced"); await adv.waitFor({ state: "attached" }); if (!(await adv.evaluate((d) => d.open))) await adv.locator(":scope > summary").click(); }
      const tick = page.locator(".editor label.pin-upstream");
      await tick.waitFor();
      assert.equal((await tick.textContent()).trim(), w.tick);
      assert.equal(await tick.locator("input").isChecked(), false, "not pinned as saved");
      // the note says what the tick does since Cline refused providerOptions (#5442)
      const note = await tick.locator("input").evaluate((i) => (i.getAttribute("aria-describedby") || "").split(" ").map((id) => document.getElementById(id)?.textContent).filter(Boolean).join(" "));
      assert.equal(note, lang === "zh" ? zh[2] : "Since 2026-11-06 Cline refuses a pinned DeepSeek or GLM request with 403, so magpie sends them unpinned and Cline picks the host; only glm-5.3-flash still asks Z.ai");
      await tick.locator("input").check();
      const n = posts.length;
      await page.locator(".editor .bar").getByRole("button", { name: w.save, exact: true }).click();
      for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
      const saved = posts.at(-1);
      assert.equal(saved.action, "save");
      assert.equal(saved.body.id, "clinepass");
      assert.equal(saved.body.pinUpstream, true);

      // the Cline plugin's provider has it too, ticked as saved; unticked,
      // Save posts it off
      await page.locator(".row.provider", { hasText: /^Cline(?!Pass)/ }).first().click();
      await page.locator(".editor input.price-rate").waitFor();
      const ptick = page.locator(".editor label.pin-upstream");
      assert.equal((await ptick.textContent()).trim(), w.tick);
      assert.equal(await ptick.locator("input").isChecked(), true, "pinned as saved");
      await ptick.locator("input").uncheck();
      const m = posts.length;
      await page.locator(".editor .bar").getByRole("button", { name: w.save, exact: true }).click();
      for (let i = 0; i < 50 && posts.length === m; i++) await page.waitForTimeout(50);
      assert.equal(posts.at(-1).action, "save");
      assert.equal(posts.at(-1).body.id, "cline-plugin");
      assert.equal(posts.at(-1).body.pinUpstream, false);

      await page.locator(".row.provider", { hasText: "Relay" }).click();
      // Relay sets nothing under Advanced, so its editor opens folded
      await page.locator(".editor details.advanced:not([open]) input.price-rate").waitFor({ state: "attached" });
      assert.equal(await page.locator(".editor label.pin-upstream").count(), 0, "only a Cline provider has it");
      assert.deepEqual(errors, []);
    });
  }
}
