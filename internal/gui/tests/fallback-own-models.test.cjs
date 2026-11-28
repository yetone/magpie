// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider's Fallback picker offers its own other models too, when it has
// more than one: an aggregator whose popular model cools down goes on to
// another of its models (big fish on Discord: "备用只能是从其他的供应商选择").
// One with a single model isn't offered itself, which would be no fallback.
// Picking one of its own lists it, and Save sends it. Every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const base = { chat: "", responses: "", anthropic: "", catalog: "", agents: [], fallback: [], headers: {}, keyList: [], key: { set: true }, ready: true };
const agg = { ...base, id: "agg", name: "Agg", icon: "generic", models: [{ id: "hot", name: "Hot", on: true }, { id: "cold", name: "Cold", on: true }, { id: "off", name: "Off", on: false }] };
const solo = { ...base, id: "solo", name: "Solo", icon: "generic", models: [{ id: "s1", name: "S1", on: true }] };

function serve(lang, saved) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    const providers = { providers: [agg, solo], presets: [], excluded: [], gateway: { running: true, window: true } };
    if (req.method() === "POST" && url.pathname.startsWith("/api/provider")) {
      saved.push(req.postDataJSON());
      return json(providers);
    }
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function openFallback(page, name) {
  await page.locator(".row.provider", { hasText: name }).first().click();
  // Fallback is under Advanced
  const adv = page.locator(".editor details.advanced");
  await adv.waitFor({ state: "attached" });
  if (!(await adv.evaluate((d) => d.open))) await adv.locator(":scope > summary").click();
  const box = page.locator(".editor .fallback");
  await box.locator("input").focus();
  return box;
}

const save = { en: "Save", zh: "保存", "zh-TW": "儲存", ja: "保存", de: "Speichern" };

const offered = (box) => box.locator(".mchips .mchip").evaluateAll((cs) => cs.map((c) => c.title));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: the Fallback picker offers the provider's own other models`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const saved = [];
      await page.route("**/*", serve(lang, saved));
      await page.goto("http://magpie.test/?view=providers");

      const box = await openFallback(page, "Agg");
      await box.locator(".mchips .mchip").first().waitFor();
      assert.deepEqual(await offered(box), ["agg/hot", "agg/cold", "solo/s1"]);
      await box.locator(".mchips .mchip", { hasText: "Cold" }).click();
      await assert.doesNotReject(box.locator(".fbl .fbrow", { hasText: "Cold" }).waitFor());
      assert.deepEqual(await offered(box), ["agg/hot", "solo/s1"]);
      const sent = page.waitForRequest((r) => r.method() === "POST" && new URL(r.url()).pathname.startsWith("/api/provider"));
      await page.getByRole("button", { name: save[lang], exact: true }).click();
      await sent;
      assert.deepEqual(saved.at(-1).fallback, ["agg/cold"]);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a provider with one model isn't offered as its own fallback`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, []));
      await page.goto("http://magpie.test/?view=providers");
      const box = await openFallback(page, "Solo");
      await box.locator(".mchips .mchip").first().waitFor();
      assert.deepEqual(await offered(box), ["agg/hot", "agg/cold"]);
      assert.deepEqual(errors, []);
    });
  }
}
