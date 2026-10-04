// Run with Node's test runner and Playwright on the module path; see README.md.
// The models picked in one provider's editor stay with that provider (#464:
// Claude models picked on one relay showed up in another relay's editor, and
// neither its Forget nor its Refresh took them away). A Refresh or a Forget
// redraws the list once the vendor answered; one left for another provider's
// editor before the list came back put its picks into that editor, which
// then kept them as picks of its own. Now the other editor shows its own
// picks only, and a Refresh or Forget there keeps them. In the editor the
// Refresh or Forget was pressed in, the picks still outlive it: one made
// before the Save, and an id added by hand that the vendor doesn't list,
// which stays a pick (Added by hand) rather than being dropped. Nothing
// scrolls the page. In English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const fetched = new Date(Date.now() - 3600e3).toISOString();
const relay = (id, name, models, on) => ({
  id, name, icon: "generic", host: id + ".example.com", chat: `https://${id}.example.com/v1`, responses: "", anthropic: "", catalog: "",
  models: models.map((m) => ({ id: m, name: m, on: on.includes(m) })), chosen: on, fetched, agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
});
const claude = relay("claude-relay", "Claude Relay", ["claude-opus-4-6", "claude-sonnet-4-6", "claude-haiku-4-5"], ["claude-opus-4-6"]);
const gpt = relay("gpt-relay", "GPT Relay", ["gpt-5", "gpt-5-mini"], ["gpt-5"]);

function serve(lang, gate, posts) {
  const providers = { providers: [claude, gpt], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (req.method() === "POST" && url.pathname.startsWith("/api/")) posts.push({ path: url.pathname, body: req.postDataJSON() || {} });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") {
      // a list asked for while the gate is shut comes back once it opens
      if (gate.shut) { gate.waiting++; await gate.shut; }
      return json(providers);
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/provider/models") return json({ count: 3, provider: claude });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { refresh: "Refresh", forget: "Forget", own: "Added by hand" },
  zh: { refresh: "刷新", forget: "清除", own: "手动添加" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a provider's picks never go to another provider's editor`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-provider-picks-stay.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const gate = { shut: null, waiting: 0 };
      let release = () => {};
      const shut = () => { gate.waiting = 0; gate.shut = new Promise((r) => { release = () => { gate.shut = null; r(); }; }); };
      const posts = [];
      await page.route("**/*", serve(lang, gate, posts));
      await page.goto("http://magpie.test/?view=providers");

      const editor = page.locator("#modal:not([hidden]) .editor");
      const open = async (name) => {
        await page.locator(".row.provider", { hasText: name }).click();
        await editor.locator(".ehead b", { hasText: name }).waitFor();
        await editor.locator(".mchips .mchip").first().waitFor();
      };
      const picks = () => editor.locator(".mchips .mchip.on").evaluateAll((cs) => cs.map((c) => c.textContent).sort());
      const button = (name) => editor.locator(".mfoot").getByRole("button", { name, exact: true });
      const until = async (f) => { for (let i = 0; i < 100 && !(await f()); i++) await page.waitForTimeout(30); assert(await f()); };

      // Claude Relay: one model more picked, not saved yet; then its Refresh
      // or Forget, and the editor left for GPT Relay's before the list is in
      for (const [action, label] of [["models", w.refresh], ["unfetch", w.forget]]) {
        await open("Claude Relay");
        await editor.locator(".mchips .mchip", { hasText: "claude-sonnet-4-6" }).click();
        assert.deepEqual(await picks(), ["claude-opus-4-6", "claude-sonnet-4-6"]);
        shut();
        const n = posts.length;
        await button(label).click();
        await until(() => posts.slice(n).some((p) => p.path === "/api/provider/" + action) && gate.waiting > 0);
        await page.keyboard.press("Escape");
        await page.locator("dialog.action-confirm[open] button").last().click();
        await page.locator("#modal").waitFor({ state: "hidden" });
        await open("GPT Relay");
        release();
        await page.waitForTimeout(400);
        assert.deepEqual(await picks(), ["gpt-5"], `${action}: GPT Relay's editor got Claude Relay's picks`);
        assert.equal(await editor.locator(".mchips .mchip.own").count(), 0, `${action}: no other provider's model shown as one added by hand`);

        // GPT Relay's own Refresh and Forget keep its picks, and only them
        for (const own of [w.refresh, w.forget]) {
          await button(own).click();
          await page.waitForTimeout(300);
          assert.deepEqual(await picks(), ["gpt-5"], `${own} on GPT Relay`);
        }
        await page.keyboard.press("Escape");
        await page.locator("#modal").waitFor({ state: "hidden" });
      }

      // in Claude Relay's editor the picks outlive its own Refresh and
      // Forget: one made before the Save, and an id its vendor doesn't
      // list, typed in, which stays picked as one added by hand
      await open("Claude Relay");
      await editor.locator(".mchips .mchip", { hasText: "claude-haiku-4-5" }).click();
      const add = editor.locator(".mfoot input");
      await add.fill("claude-mine-1");
      await add.press("Enter");
      const mine = editor.locator(".mchips .mchip.on.own", { hasText: "claude-mine-1" });
      await mine.waitFor();
      assert.equal((await mine.getAttribute("title")).split("\n")[0], w.own);
      const view = page.locator("#view-providers");
      const top = await view.evaluate((v) => v.scrollTop);
      for (const label of [w.refresh, w.forget]) {
        await button(label).click();
        await page.waitForTimeout(400);
        assert.deepEqual(await picks(), ["claude-haiku-4-5", "claude-mine-1", "claude-opus-4-6"], `after ${label}`);
        assert.equal(await mine.count(), 1, `the id added by hand stays after ${label}`);
      }
      assert.equal(await view.evaluate((v) => v.scrollTop), top, "the page moved");

      // a Save sends exactly those
      await editor.locator(".bar").getByRole("button", { name: lang === "zh" ? "保存" : "Save", exact: true }).click();
      await until(() => posts.some((p) => p.path === "/api/provider/save"));
      assert.deepEqual([...posts.find((p) => p.path === "/api/provider/save").body.models].sort(), ["claude-haiku-4-5", "claude-mine-1", "claude-opus-4-6"]);
      assert.deepEqual(errors, []);
    });
  }
}
