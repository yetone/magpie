// SiliconFlow's regional preset: endpoints and console links follow the
// selection, saved accounts reopen at their region, and public links carry it.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const publicDir = path.resolve(__dirname, "../../../site/public");
const cn = { chat: "https://api.siliconflow.cn/v1", catalog: "siliconflow-cn", website: "https://cloud.siliconflow.cn", keysUrl: "https://cloud.siliconflow.cn/account/ak" };
const intl = { chat: "https://api.siliconflow.com/v1", catalog: "siliconflow", website: "https://cloud.siliconflow.com", keysUrl: "https://cloud.siliconflow.com/account/ak" };
const preset = {
  id: "siliconflow", name: "SiliconFlow", icon: "siliconcloud-color", kind: "relay", ...cn,
  regions: [{ id: "cn", name: "China", ...cn }, { id: "intl", name: "Global", ...intl }],
};
const words = {
  en: { regions: ["China", "Global"], key: "Get a key ↗", add: "Add", save: "Save" },
  zh: { regions: ["中国", "国际"], key: "获取密钥 ↗", add: "添加", save: "保存" },
};

async function asset(route, root, pathname) {
  const types = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" };
  try { await route.fulfill({ body: await fs.readFile(path.join(root, pathname)), contentType: types[path.extname(pathname)] }); }
  catch { await route.fulfill({ status: 404, body: "" }); }
}

function serve(lang, list, posts) {
  const providers = { providers: list, presets: [{ ...preset, added: list.length > 0 }], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (route.request().method() === "POST") posts.push({ action: url.pathname, body: route.request().postDataJSON() });
    if (url.pathname.startsWith("/api/provider/")) return json(providers);
    if (url.pathname.startsWith("/api/")) return json({});
    return asset(route, assets, url.pathname === "/" ? "index.html" : url.pathname);
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: SiliconFlow regional editor and import builder`, async (t) => {
    assert(["chromium", "webkit"].includes(engine));
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      for (const width of [900, 440]) {
        await t.test(`${lang} ${width}: add international`, async (t) => {
          const ctx = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
          t.after(() => ctx.close());
          const page = await ctx.newPage();
          page.setDefaultTimeout(15000);
          const posts = [], errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, [], posts));
          await page.goto("http://magpie.test/?view=providers");
          // With no providers the app opens the Add sheet automatically.
          await page.locator("#addSheet .find").fill("SiliconFlow");
          await page.locator('#addSheet .tile[data-pick="SiliconFlow"]').click();
          const ed = page.locator("#modal .editor");
          const regions = ed.locator(".segs.regions .opt");
          const site = ed.locator(".ehead .link.site");
          const key = ed.locator("input[type=password]").first();
          const w = words[lang];
          assert.deepEqual(await regions.allTextContents(), w.regions);
          assert.equal(await ed.locator(".segs.regions .opt.on").textContent(), w.regions[0]);
          assert.equal(await ed.locator("select").count(), 0);
          await key.fill("sk-international-test");
          await regions.nth(1).scrollIntoViewIfNeeded();
          const scroll = await page.evaluate(() => window.scrollY);
          for (const [index, config] of [[1, intl], [0, cn], [1, intl]]) {
            await regions.nth(index).click();
            assert.equal(await key.inputValue(), "sk-international-test");
            assert.equal(await site.getAttribute("data-url"), config.website);
            await ed.getByRole("button", { name: w.key, exact: true }).click();
            assert.equal(posts.filter((p) => p.action === "/api/open").at(-1)?.body.url, config.keysUrl);
            assert.equal(await page.evaluate(() => window.scrollY), scroll, "region clicks do not scroll the page");
          }
          await ed.locator(".bar").getByRole("button", { name: w.add, exact: true }).click();
          await page.locator("#modal").waitFor({ state: "hidden" });
          const saved = posts.find((p) => p.action === "/api/provider/save");
          assert(saved, "international provider saved");
          assert.equal(saved.body.preset, "siliconflow");
          assert.equal(saved.body.chat, intl.chat);
          assert.equal(saved.body.key, "sk-international-test");
          assert(!saved.body.responses && !saved.body.anthropic);
          assert.deepEqual(errors, []);
        });
      }
      await t.test(`${lang}: saved international reopens`, async (t) => {
        const ctx = await browser.newContext({ viewport: { width: 900, height: 800 }, reducedMotion: "reduce" });
        t.after(() => ctx.close());
        const page = await ctx.newPage();
        page.setDefaultTimeout(15000);
        const saved = { id: "siliconflow-2", preset: "siliconflow", name: "SiliconFlow Global", icon: preset.icon, ...intl,
          models: [], agents: [], fallback: [], headers: {}, key: { set: true, masked: "sk-…test" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "" };
        const posts = [];
        await page.route("**/*", serve(lang, [saved], posts));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator('#providers .row[data-id="siliconflow-2"]').click();
        const ed = page.locator("#modal .editor");
        assert.equal(await ed.locator(".segs.regions .opt.on").textContent(), words[lang].regions[1]);
        assert.equal(await ed.locator(".ehead .link.site").getAttribute("data-url"), intl.website);
        await ed.locator(".segs.regions .opt").nth(0).click();
        await ed.locator(".bar").getByRole("button", { name: words[lang].save, exact: true }).click();
        await page.locator("#modal").waitFor({ state: "hidden" });
        const result = posts.find((p) => p.action === "/api/provider/save");
        assert.equal(result?.body.id, saved.id);
        assert.equal(result?.body.chat, cn.chat);
      });
    }
    for (const lang of ["en", "zh", "ja"]) {
      await t.test(`${lang}: public import builder`, async (t) => {
        const ctx = await browser.newContext();
        t.after(() => ctx.close());
        const page = await ctx.newPage();
        const doc = lang === "en" ? "/docs/import.html" : `/docs/${lang}/import.html`;
        await page.route("**/*", (route) => asset(route, publicDir, new URL(route.request().url()).pathname));
        await page.goto("http://magpie.test" + doc);
        await page.locator('[data-mode="preset"]').click();
        await page.locator('#b select[name="preset"]').selectOption("siliconflow");
        await page.locator('#b select[name="region"]').selectOption("intl");
        const query = new URL(await page.locator("#o-app").textContent()).searchParams;
        assert.equal(query.get("preset"), "siliconflow");
        assert.equal(query.get("region"), "intl");
        await page.locator('#b select[name="region"]').selectOption("");
        assert.equal(new URL(await page.locator("#o-app").textContent()).searchParams.get("region"), null, "China remains the default");
      });
    }
  });
}
