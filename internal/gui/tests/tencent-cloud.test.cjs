// Run with Node's test runner and Playwright on the module path; see README.md.
// Tencent Cloud as one provider (Jorben on Discord): the Token Plan and
// TokenHub's two pay-as-you-go presets are one Tencent Cloud preset whose
// regions are Plan · China, Pay as you go · China and Pay as you go · Global.
// The add sheet has one Tencent Cloud tile and no TokenHub; its editor picks
// the region with the segmented control, never a <select>, and a region
// picked takes its endpoints, its Get a key page and its docs link. A
// provider an older magpie saved under an old preset's id (tencent-tokenhub-cn)
// opens at its region and is saved under its own id. English and Chinese;
// no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const plan = { chat: "https://api.lkeap.cloud.tencent.com/plan/v3", anthropic: "https://api.lkeap.cloud.tencent.com/plan/anthropic",
  website: "https://cloud.tencent.com/document/product/1823/130060", keysUrl: "https://console.cloud.tencent.com/tokenhub/tokenplan" };
const cn = { chat: "https://tokenhub.tencentmaas.com/v1", responses: "https://tokenhub.tencentmaas.com/v1", anthropic: "https://tokenhub.tencentmaas.com",
  website: "https://cloud.tencent.com/document/product/1823/130078", keysUrl: "https://console.cloud.tencent.com/tokenhub/apikey", catalog: "tencent-tokenhub" };
const intl = { chat: "https://tokenhub-intl.tencentmaas.com/v1", responses: "https://tokenhub-intl.tencentmaas.com/v1", anthropic: "https://tokenhub-intl.tencentmaas.com",
  website: "https://www.tencentcloud.com/document/product/1300/78939", keysUrl: "https://console.tencentcloud.com/tokenhub/apikey", catalog: "tencent-tokenhub" };
// as /api/providers serves the preset
const tencent = (added) => ({
  id: "tencent-cloud", name: "Tencent Cloud", icon: "tencentcloud-color", kind: "vendor", hosts: true, added,
  note: "Token Plan · pay as you go", chat: plan.chat, anthropic: plan.anthropic, website: plan.website, keysUrl: plan.keysUrl,
  regions: [{ id: "plan", name: "Plan · China", ...plan }, { id: "cn", name: "Pay as you go · China", ...cn }, { id: "intl", name: "Pay as you go · Global", ...intl }],
});
const deepseek = { id: "deepseek", name: "DeepSeek", icon: "deepseek", kind: "vendor", chat: "https://api.deepseek.com/v1", added: false };
// saved by an older magpie as TokenHub China, its preset id since rewritten
const old = {
  id: "tencent-tokenhub-cn", name: "Tencent Cloud TokenHub", icon: "tencentcloud-color", preset: "tencent-cloud",
  chat: cn.chat, responses: cn.responses, anthropic: cn.anthropic, catalog: cn.catalog, website: cn.website, keysUrl: cn.keysUrl,
  models: [{ id: "hy3", name: "hy3", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…ab12" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};

const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [], agents: [], fallback: [], headers: {}, key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};

function serve(lang, list, posts) {
  const providers = { providers: list, presets: [tencent(list.includes(old)), deepseek], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (route.request().method() === "POST" && (url.pathname === "/api/open" || url.pathname.startsWith("/api/provider/"))) {
      posts.push({ action: url.pathname.slice("/api/".length), body: route.request().postDataJSON() });
      return url.pathname === "/api/open" ? route.fulfill({ status: 204, body: "" }) : json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { regions: ["Plan · China", "Pay as you go · China", "Pay as you go · Global"], key: "Get a key ↗", add: "Add", save: "Save" },
  zh: { regions: ["套餐 · 中国", "按量 · 中国", "按量 · 国际"], key: "获取密钥 ↗", add: "添加", save: "保存" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    const start = async (t, list) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 800 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posts = [];
      await page.route("**/*", serve(lang, list, posts));
      await page.goto("http://magpie.test/?view=providers");
      return { page, errors, posts };
    };
    const ed = (page) => page.locator("#modal .editor");
    const segs = (page) => ed(page).locator(".segs.regions .opt");
    const site = (page) => ed(page).locator(".ehead .link.site");
    const posted = async (page, posts, n, action) => {
      for (let i = 0; i < 50 && !posts.slice(n).some((p) => p.action === action); i++) await page.waitForTimeout(50);
      return posts.slice(n).find((p) => p.action === action);
    };
    // the link's page, as a click opens it
    const opens = async (page, posts, loc) => {
      const n = posts.length;
      await loc.click();
      return (await posted(page, posts, n, "open"))?.body.url;
    };

    test(`${engine} ${lang}: one Tencent Cloud tile, its regions picked with the segmented control`, async (t) => {
      const { page, errors, posts } = await start(t, [relay]);
      await page.locator("#addProvider").click();
      const sheet = page.locator("#addSheet");
      await sheet.locator(".tile").first().waitFor();
      const names = await sheet.locator(".tile .n").allTextContents();
      assert.equal(names.filter((n) => /Tencent/.test(n)).length, 1, names.join(" | "));
      assert.equal(names.filter((n) => /TokenHub|Token Plan/.test(n)).length, 0, names.join(" | "));

      await sheet.locator('.tile[data-pick="Tencent Cloud"]').click();
      await ed(page).locator(".segs.regions").waitFor();
      assert.equal(await ed(page).locator("select").count(), 0, "no native select");
      assert.deepEqual(await segs(page).allTextContents(), w.regions);
      // a new one is at the plan, as the old Token Plan preset was
      assert.equal(await segs(page).locator("xpath=self::*[contains(@class,'on')]").textContent(), w.regions[0]);
      assert.equal(await site(page).textContent(), "cloud.tencent.com ↗");
      assert.equal(await opens(page, posts, site(page)), plan.website);
      const getKey = ed(page).getByRole("button", { name: w.key, exact: true });
      assert.equal(await opens(page, posts, getKey), plan.keysUrl);

      // Global: TokenHub's international endpoints, keys page and docs
      await segs(page).nth(2).click();
      assert.equal(await ed(page).locator(".segs.regions .opt.on").textContent(), w.regions[2]);
      assert.equal(await site(page).textContent(), "www.tencentcloud.com ↗");
      assert.equal(await opens(page, posts, site(page)), intl.website);
      assert.equal(await opens(page, posts, getKey), intl.keysUrl);
      // China's pay as you go
      await segs(page).nth(1).click();
      assert.equal(await opens(page, posts, site(page)), cn.website);
      assert.equal(await opens(page, posts, getKey), cn.keysUrl);

      await ed(page).locator("input[type=password]").first().fill("sk-tc-1");
      const n = posts.length;
      await ed(page).locator(".bar").getByRole("button", { name: w.add, exact: true }).click();
      const saved = await posted(page, posts, n, "provider/save");
      assert(saved, "saved");
      assert.equal(saved.body.preset, "tencent-cloud");
      assert.deepEqual([saved.body.chat, saved.body.responses, saved.body.anthropic], [cn.chat, cn.responses, cn.anthropic]);

      const missing = await page.evaluate(() => ["Plan · China", "Pay as you go · China", "Pay as you go · Global", "Token Plan · pay as you go"].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a provider saved under an old TokenHub id opens at its region and keeps its id`, async (t) => {
      const { page, errors, posts } = await start(t, [old]);
      await page.locator('#providers .row[data-id="tencent-tokenhub-cn"]').click();
      await ed(page).locator(".segs.regions").waitFor();
      assert.equal(await ed(page).locator("select").count(), 0, "no native select");
      assert.equal(await ed(page).locator(".segs.regions .opt.on").textContent(), w.regions[1]);
      assert.equal(await opens(page, posts, site(page)), cn.website);

      await segs(page).nth(2).click();
      assert.equal(await opens(page, posts, site(page)), intl.website);
      const n = posts.length;
      await ed(page).locator(".bar").getByRole("button", { name: w.save, exact: true }).click();
      const saved = await posted(page, posts, n, "provider/save");
      assert(saved, "saved");
      assert.equal(saved.body.id, "tencent-tokenhub-cn");
      assert.equal(saved.body.preset, "tencent-cloud");
      assert.deepEqual([saved.body.chat, saved.body.responses, saved.body.anthropic], [intl.chat, intl.responses, intl.anthropic]);
      assert.deepEqual(errors, []);
    });
  }
}
