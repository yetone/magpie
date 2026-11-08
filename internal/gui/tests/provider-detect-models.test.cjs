// Run with Node's test runner and Playwright on the module path; see README.md.
// Which API serves which model, found at a click (01huadalang on Discord:
// 检测协议的结果看不出对应的模型支持协议情况…有的仅支持 response 有的双协议):
// Detect APIs' "Each picked model" posts provider/detect with every model
// picked (detectModels) and shows model by API, ✓ or ✗ with why. Its
// "Use these" sets the URLs that answered and gives each model that
// answered on one API only that API, staged in Model settings (unsaved)
// and sent with the Save as modelPrefs — nothing before. A chip's
// right-click Test says the API it was asked on. No click moves the page,
// and nothing has a left-border accent. In English and Chinese, Chromium
// and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const ROOT = "https://relay.example.com";
const IDS = ["m-both", "m-resp", "m-chat"];
// what each model answers on: chat, responses, anthropic
const SERVES = { "m-both": ["chat", "responses"], "m-resp": ["responses"], "m-chat": ["chat"] };

function serve(lang, posts) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const models = IDS.map((id) => ({ id, name: id, on: true }));
  const provider = { id: "relay", name: "Relay", icon: "generic", chat: ROOT + "/v1", responses: "", anthropic: "", models, agents: [], fallback: [], headers: {}, key: { set: true, masked: "sk-…1234" }, keyList: [], ready: true };
  const providers = { providers: [provider], presets: [], excluded: [], gateway: { running: true, window: true } };
  const one = (m, proto) => {
    const base = proto === "anthropic" ? ROOT : ROOT + "/v1";
    if ((SERVES[m] || []).includes(proto)) return { protocol: proto, ok: true, ms: 120, model: m, base };
    if (proto === "anthropic") return { protocol: proto, ok: false, status: 404, error: "Invalid URL (POST /v1/messages)", model: m, base };
    return { protocol: proto, ok: false, status: 400, error: `model ${m} is not supported on this endpoint`, model: m, base };
  };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/provider/detect") {
      const body = route.request().postDataJSON();
      posts.push({ path: url.pathname, body });
      const each = body.detectModels.map((m) => ({ model: m, results: ["chat", "responses", "anthropic"].map((p) => one(m, p)) }));
      const results = [0, 1, 2].map((i) => each.find((x) => x.results[i].ok)?.results[i] || each[0].results[i]);
      return json({ results, models: each });
    }
    if (url.pathname === "/api/provider/test") {
      const body = route.request().postDataJSON();
      posts.push({ path: url.pathname, body });
      return json({ results: body.test.map((m) => ({ protocol: "responses", ok: true, ms: 123, model: m })) });
    }
    if (url.pathname.startsWith("/api/provider/")) {
      posts.push({ path: url.pathname, body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
  };
}

// a right-click on a chip out of sight: Playwright scrolls it in first, and
// Chromium sends that scroll's event a frame later, which closes the menu
// the click opened (a menu closes on a scroll). Scroll it in, let the
// frame pass, then click.
async function rclick(loc) {
  await loc.click({ button: "right", trial: true }); // scrolls it in, clicks nothing
  await loc.page().evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
  await loc.click({ button: "right" });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: Detect APIs model by model, and Test says the API`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-provider-detect-models.png`), fullPage: true });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      const zh = lang === "zh";
      const L = {
        each: zh ? "逐个检测已选模型" : "Each picked model", use: zh ? "使用检测结果" : "Use these", taken: zh ? "已填入 · 保存后生效" : "Taken · save to keep",
        only: zh ? "m-resp：仅 Responses · m-chat：仅 OpenAI" : "m-resp: Responses only · m-chat: OpenAI only",
        said: zh ? "保存后 2 个模型将只使用各自唯一可用的协议" : "2 models are asked on the one API that answered them once saved",
        names: zh ? "模型设置" : "Model settings", unsaved: zh ? "未保存" : "unsaved", save: zh ? "保存" : "Save",
        item: zh ? "测试此模型" : "Test this model",
        via: zh ? "m-both 通过 Responses 协议在 123 毫秒内响应" : "m-both answered via Responses in 123 ms",
        viaTitle: zh ? "通过 Responses 协议在 123 毫秒内响应" : "Answered via Responses in 123 ms",
      };
      await page.locator(".row.provider").click();
      const ed = page.locator(".editor");
      await ed.locator(".detect").waitFor();
      const row = (id) => page.locator(".mname", { has: page.locator("code", { hasText: id }) });
      const scrolls = () => page.evaluate(() => [scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join());
      // a click that leaves the page, and the button, where they were
      const still = async (loc, what) => {
        await loc.scrollIntoViewIfNeeded();
        await page.waitForTimeout(150);
        const before = await loc.evaluate((e) => e.getBoundingClientRect().top);
        const y = await scrolls();
        await loc.click();
        await page.waitForTimeout(250);
        if (await loc.count()) assert.equal(await loc.evaluate((e) => e.getBoundingClientRect().top), before, what + " moved");
        assert.equal(await scrolls(), y, what + " scrolled the page");
      };

      // every picked model, asked on each API
      await still(ed.getByRole("button", { name: L.each, exact: true }), "Each picked model");
      await ed.locator(".detect-grid .dg-cell").nth(8).waitFor();
      assert.equal(posts.length, 1);
      assert.equal(posts[0].path, "/api/provider/detect");
      assert.deepEqual(posts[0].body.detectModels, IDS, "the models picked");
      assert.equal(posts[0].body.base, ROOT + "/v1");
      const cells = await ed.locator(".detect-grid .dg-cell").evaluateAll((es) => es.map((e) => `${e.dataset.model}/${e.dataset.api}=${e.classList.contains("ok") ? "ok" : "bad"}`));
      assert.deepEqual(cells, [
        "m-both/chat=ok", "m-both/responses=ok", "m-both/anthropic=bad",
        "m-resp/chat=bad", "m-resp/responses=ok", "m-resp/anthropic=bad",
        "m-chat/chat=ok", "m-chat/responses=bad", "m-chat/anthropic=bad",
      ]);
      const heads = await ed.locator(".detect-grid .dg-head").allTextContents();
      assert.deepEqual(heads.slice(1), ["OpenAI", "Responses", "Anthropic"]);
      const why = ed.locator('.detect-grid .dg-cell[data-model="m-resp"][data-api="chat"]');
      assert((await why.textContent()).startsWith("400 · model m-resp is not supported"), "a ✗ says why");
      assert((await why.getAttribute("title")).includes("m-resp · OpenAI"));
      assert.equal(await ed.locator('.detect-grid .dg-cell[data-model="m-both"][data-api="chat"]').textContent(), zh ? "120 毫秒" : "120 ms");
      assert.equal(await ed.locator(".detect-acts .hint").textContent(), L.only);

      // Use these: Responses' URL set, the two models staged, nothing sent
      await page.getByRole("button", { name: L.names, exact: true }).click();
      await row("m-both").waitFor();
      await still(ed.getByRole("button", { name: L.use, exact: true }), "Use these");
      assert.equal(await ed.getByRole("button", { name: L.taken, exact: true }).count(), 1);
      assert.equal(await page.locator("#status").textContent(), L.said);
      assert.equal(posts.length, 1, "nothing is sent before the Save");
      await row("m-resp").locator(".mapi").waitFor();
      assert.equal(await row("m-resp").locator(".mapi .opt.on").getAttribute("data-api"), "responses");
      assert.equal(await row("m-chat").locator(".mapi .opt.on").getAttribute("data-api"), "chat");
      assert.equal(await row("m-both").locator(".mapi .opt.on").getAttribute("data-api"), "", "a model served on both stays Auto");
      assert(await row("m-resp").getByText(L.unsaved, { exact: true }).isVisible(), "it says unsaved");
      assert.equal(await row("m-both").getByText(L.unsaved, { exact: true }).isVisible(), false);

      // a chip's right-click Test says the API it was asked on
      const chip = page.locator(".editor .mchips .mchip", { hasText: "m-both" });
      await rclick(chip);
      await page.locator(".pop.row-menu").getByRole("menuitem", { name: L.item }).click();
      await chip.locator(".tdot.ok").waitFor();
      assert.equal(await page.locator("#status").textContent(), L.via);
      assert((await chip.getAttribute("title")).startsWith(L.viaTitle));

      const border = await page.evaluate(() => [...document.querySelectorAll(".detect, .detect *, .mapi, .mapi *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no left-border accent");

      // the Save carries the URLs and the two APIs
      await page.getByRole("button", { name: L.save, exact: true }).click();
      for (let i = 0; i < 60 && !posts.some((p) => p.path === "/api/provider/save"); i++) await page.waitForTimeout(50);
      const save = posts.find((p) => p.path === "/api/provider/save");
      assert(save, "saved");
      assert.equal(save.body.chat, ROOT + "/v1");
      assert.equal(save.body.responses, ROOT + "/v1");
      assert.equal(save.body.anthropic || "", "", "Anthropic (404) isn't set");
      assert.deepEqual(save.body.modelPrefs, { "m-resp": { api: "responses" }, "m-chat": { api: "chat" } });

      const missing = await page.evaluate(() => [
        "Each picked model", "Ask every model picked below on each API, to see which API serves which model (up to {n}, a few at a time)",
        "Pick models first, or type one and press Detect APIs", "Only the first {n} of {all} models were asked", "Model",
        "Set the URLs of the APIs that answered, and give each model that answered on one API only that API (staged in Model settings, made with the Save)",
        "{n} models are asked on the one API that answered them once saved", "{model}: {api} only",
        "an image model: asked on the images API, not here", "not asked: out of time",
        "Answered via {api} in {took}", "{model} answered via {api} in {took}", "{model} didn't answer via {api}: {error}",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
