// Run with Node's test runner and Playwright on the module path; see README.md.
// The provider editor's model picks are a draft until Save (#578, #614:
// Sun1090 clicked Free only, closed the editor without saving, and found
// it applied; Select none and Save came back as 24 models ticked). With no
// pick saved the editor opens with none ticked, the vendor's first 24 drawn
// as served: it took those served for picks, drew them ticked, and the next
// Save wrote them as picks. Free only then Cancel or Escape writes nothing
// and the editor opens as it was; Free only and Save writes the free ones;
// Select none and Save writes none, and the editor opens with none ticked
// again. In English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const fetched = new Date(Date.now() - 3600e3).toISOString();
const ids = Array.from({ length: 30 }, (_, i) => `vendor/model-${i}`);
const free = new Set(["vendor/model-3", "vendor/model-10", "vendor/model-27"]);

// the provider as magpie answers it for these picks: with none, the first
// 24 of the vendor's list are on (served), as Exposed has it
function routerFor(chosen) {
  const on = new Set(chosen.length ? chosen : ids.slice(0, 24));
  return {
    id: "router", name: "Router", icon: "generic", host: "router.example.com", chat: "https://router.example.com/v1", responses: "", anthropic: "", catalog: "",
    models: ids.map((id) => ({ id, name: id, on: on.has(id), free: free.has(id) })), chosen, fetched, agents: [], fallback: [], headers: {},
    key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
  };
}

function serve(lang, saves) {
  let chosen = [];
  const providers = () => ({ providers: [routerFor(chosen)], presets: [], excluded: [], gateway: { running: true, window: true } });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers());
    if (url.pathname === "/api/provider/save") {
      const body = req.postDataJSON();
      saves.push(body.models);
      chosen = body.models;
      return json(providers());
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { free: "Free only", none: "Select none", save: "Save", cancel: "Cancel" },
  zh: { free: "只选免费", none: "全不选", save: "保存", cancel: "取消" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: model picks are written by Save only, and none saved opens as none`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-provider-draft-picks.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const saves = [];
      await page.route("**/*", serve(lang, saves));
      await page.goto("http://magpie.test/?view=providers");
      const editor = page.locator("#modal:not([hidden]) .editor");
      const open = async () => {
        await page.locator(".row.provider", { hasText: "Router" }).click();
        await editor.locator(".mchips .mchip").first().waitFor();
      };
      const picked = () => editor.locator(".mchips .mchip.on").evaluateAll((cs) => cs.map((c) => c.firstChild.textContent));
      const served = () => editor.locator(".mchips .mchip.auto").count();
      const button = (name) => editor.getByRole("button", { name, exact: true });

      // none saved: none ticked, the first 24 drawn as served
      await open();
      assert.deepEqual(await picked(), [], "opened with no pick saved");
      assert.equal(await served(), 24);

      // Free only, then Cancel: nothing written, opened again as it was
      await button(w.free).click();
      assert.deepEqual(await picked(), [...free]);
      await button(w.cancel).click();
      await page.locator("dialog.action-confirm[open] button").last().click();
      await editor.waitFor({ state: "detached" });
      await open();
      assert.deepEqual(await picked(), [], "Free only undone by Cancel");
      assert.equal(await served(), 24);

      // Free only, then Escape: the same
      await button(w.free).click();
      await page.keyboard.press("Escape");
      await page.locator("dialog.action-confirm[open] button").last().click();
      await editor.waitFor({ state: "detached" });
      await open();
      assert.deepEqual(await picked(), [], "Free only undone by Escape");
      assert.deepEqual(saves, []);

      // Free only and Save writes the free ones
      await button(w.free).click();
      await button(w.save).click();
      await editor.waitFor({ state: "detached" });
      assert.deepEqual(saves, [[...free]]);
      await open();
      assert.deepEqual(await picked(), [...free]);
      assert.equal(await served(), 0);

      // Select none and Save writes none, and none is what opens again
      await button(w.none).click();
      assert.deepEqual(await picked(), []);
      assert.equal(await served(), 24);
      await button(w.save).click();
      await editor.waitFor({ state: "detached" });
      assert.deepEqual(saves[1], []);
      await open();
      assert.deepEqual(await picked(), [], "Select none saved opens with none ticked");
      assert.equal(await served(), 24);

      // saved again untouched, it stays none: the served aren't made picks
      await button(w.save).click();
      await editor.waitFor({ state: "detached" });
      assert.deepEqual(saves[2], []);
      assert.deepEqual(errors, []);
    });
  }
}
