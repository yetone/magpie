// Run with Node's test runner and Playwright on the module path; see README.md.
// axelburks on Discord: "供应商里方舟的配置页，有图像模型选项能勾选模型，但是没
// 有视频模型选项不能勾选视频模型". A Volcengine Ark key's editor had its
// Seedream models under Image models, to tick, and its Seedance models
// nowhere. Now they are under Video models, each ticked, a click unticks it
// (and ticks it back), and Save writes the unticked ones as
// hiddenVideomakers, beside hiddenDrawers, neither one touching the other.
// Every language, wide and at a phone's width; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const fetched = new Date(Date.now() - 3600e3).toISOString();
const drawIds = ["doubao-seedream-5-0-pro-260628", "doubao-seedream-5-0-flash-260915"];
const filmIds = ["doubao-seedance-2-0-260128", "doubao-seedance-2-0-fast-260128", "doubao-seedance-1-0-pro-fast-251015"];

function providerFor(hidden, hiddenDraw) {
  return {
    id: "volcengine-2", name: "Volcengine Ark", icon: "volcengine", preset: "volcengine", host: "", chat: "https://ark.cn-beijing.volces.com/api/v3", responses: "", anthropic: "", catalog: "",
    models: [{ id: "doubao-seed-2-1-pro-260915", name: "Doubao Seed 2.1 Pro", on: true }], chosen: [], fetched, agents: [], fallback: [], headers: {},
    key: { set: true, masked: "ark-…one", optional: false }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", ready: true,
    draws: drawIds.length, drawIds, hiddenDrawers: hiddenDraw, filmIds, hiddenVideomakers: hidden,
  };
}

function serve(lang, saves) {
  let hidden = ["doubao-seedance-1-0-pro-fast-251015"], hiddenDraw = ["doubao-seedream-5-0-flash-260915"];
  const providers = () => ({ providers: [providerFor(hidden, hiddenDraw)], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true,textSize:100};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", textSize: 100 } });
    if (url.pathname === "/api/settings") return json({ lang, theme: "light", textSize: 100, fx: { rate: 7.2, at: new Date().toISOString(), stale: false } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers());
    if (url.pathname === "/api/provider/save") {
      const body = req.postDataJSON();
      saves.push({ videos: body.hiddenVideomakers, images: body.hiddenDrawers });
      if (body.hiddenVideomakers) hidden = body.hiddenVideomakers;
      if (body.hiddenDrawers) hiddenDraw = body.hiddenDrawers;
      return json(providers());
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [], models: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

const words = {
  en: { save: "Save", field: "Video models", hint: /Only the ticked ones are offered when making a video/ },
  zh: { save: "保存", field: "视频模型", hint: /生成视频时只提供已勾选的/ },
  "zh-TW": { save: "儲存", field: "影片模型", hint: /生成影片時只提供已勾選的/ },
  ja: { save: "保存", field: "動画モデル", hint: /動画を作るときは、チェックしたものだけが候補になります/ },
  de: { save: "Speichern", field: "Videomodelle", hint: /Beim Erstellen eines Videos werden nur die angehakten angeboten/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a provider's video models can be unticked, apart from its image models", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(words)) {
      const w = words[lang];
      for (const width of [1000, 360]) {
        await t.test(`${lang} at ${width}px: the editor unticks and ticks back a video model`, async () => {
          const saves = [];
          const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, saves));
          await page.goto("http://magpie.test/?view=providers");
          const editor = page.locator("#modal:not([hidden]) .editor");
          const openEditor = async () => {
            await page.locator(".row.provider", { hasText: "Volcengine Ark" }).first().click();
            await editor.locator(".mchips.videomakers .mchip").first().waitFor();
          };
          const ticked = (cls) => editor.locator(`.mchips.${cls} .mchip.on`).evaluateAll((cs) => cs.map((c) => c.textContent));
          const chip = (id) => editor.locator(".mchips.videomakers .mchip", { has: page.locator(`span:text-is("${id}")`) });
          await openEditor();
          await editor.getByText(w.field, { exact: true }).first().waitFor();
          await editor.getByText(w.hint).waitFor();
          assert.equal(await editor.locator(".mchips.videomakers .mchip").count(), 3);
          assert.deepEqual(await ticked("videomakers"), ["doubao-seedance-2-0-260128", "doubao-seedance-2-0-fast-260128"], "the saved hidden one opens unticked");
          assert.deepEqual(await ticked("drawers"), ["doubao-seedream-5-0-pro-260628"], "image models are their own");
          assert.equal(await editor.locator(".mchips.drawers .mchip", { hasText: "seedance" }).count(), 0);
          assert.equal(await chip("doubao-seedance-1-0-pro-fast-251015").getAttribute("aria-pressed"), "false");
          await chip("doubao-seedance-2-0-fast-260128").click();
          assert.deepEqual(await ticked("videomakers"), ["doubao-seedance-2-0-260128"]);
          await chip("doubao-seedance-1-0-pro-fast-251015").click();
          assert.deepEqual(await ticked("videomakers"), ["doubao-seedance-2-0-260128", "doubao-seedance-1-0-pro-fast-251015"]);
          assert.deepEqual(await ticked("drawers"), ["doubao-seedream-5-0-pro-260628"], "a video tick leaves the image models be");
          // the section and its chips fit the editor at a phone's width
          const fit = await editor.locator(".mchips.videomakers").evaluate((box) => {
            const ed = box.closest(".editor").getBoundingClientRect();
            return [...box.querySelectorAll(".mchip")].every((c) => { const r = c.getBoundingClientRect(); return r.left >= ed.left - 1 && r.right <= ed.right + 1; });
          });
          assert.equal(fit, true, "a chip runs out of the editor");
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth), 0);
          await editor.getByRole("button", { name: w.save, exact: true }).click();
          await editor.waitFor({ state: "detached" });
          assert.deepEqual(saves.at(-1), { videos: ["doubao-seedance-2-0-fast-260128"], images: ["doubao-seedream-5-0-flash-260915"] });
          await openEditor();
          assert.deepEqual(await ticked("videomakers"), ["doubao-seedance-2-0-260128", "doubao-seedance-1-0-pro-fast-251015"], "reopened as saved");
          assert.equal(await page.locator("select").count(), 0);
          assert.deepEqual(errors, []);
          await page.context().close();
        });
      }
    }
  });
}
