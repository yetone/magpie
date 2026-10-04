// Run with Node's test runner and Playwright on the module path; see README.md.
// A model's credit rate, as its vendor lists it, shows by the model so a
// cheap one is picked without opening the vendor's app (01huadalang on
// Discord: qcoder 或者 workbuddy 能不能…展示列表模型的积分消耗倍率): Qoder's
// 0.5×, WorkBuddy's x0.03 as 0.03×, a discount's price before it struck
// through (Qwen3.8-Flash free, 0.1× struck through; 0.2× with 0.5× struck
// through). A small grey badge on the provider editor's model chips and in
// an agent's model picker, its title saying it; a model with no rate has
// none (the picker in a connected agent's opened row). Clicking a chip
// picks it and scrolls nothing; no stripe. In English and Chinese,
// Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const fetched = new Date(Date.now() - 3600e3).toISOString();
const qoder = {
  id: "qoder", name: "Qoder", icon: "generic", host: "qoder.example.com", chat: "https://qoder.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [
    { id: "qmodel_38max", name: "Qwen3.8-Max", on: false, rate: 0.5 },
    { id: "qmodel_offpeak", name: "Qwen3.8-Max Off-Peak", on: false, rate: 0.2, rateWas: 0.5 },
    { id: "qfmodel", name: "Qwen3.8-Flash", on: false, free: true, rateWas: 0.1 },
    { id: "wb-sg", name: "Deepseek-V4.1-Flash (SG)", on: false, rate: 0.03 },
    { id: "plain", name: "Plain", on: false },
  ],
  chosen: [], fetched, agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};
const options = [
  { value: "magpie/qoder/qmodel_38max", ref: "qoder/qmodel_38max", label: "Qwen3.8-Max", note: "Qoder · via magpie", group: "Qoder", rate: 0.5 },
  { value: "magpie/qoder/qfmodel", ref: "qoder/qfmodel", label: "Qwen3.8-Flash", note: "Qoder · via magpie", group: "Qoder", free: true, rateWas: 0.1 },
  { value: "magpie/qoder/plain", ref: "qoder/plain", label: "Plain", note: "Qoder · via magpie", group: "Qoder" },
];
const agents = [{ id: "claude", name: "Claude Code", path: "/test/claude", wired: true, fields: [{ key: "model", label: "model", value: "magpie/qoder/plain", options }] }];

function serve(lang) {
  const providers = { providers: [qoder], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents, profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { costs: "costs 0.5× credits a request", was: "costs 0.2× credits a request, 0.5× before the discount", free: "free now, 0.1× before the discount" },
  zh: { costs: "每次请求消耗 0.5× 积分", was: "每次请求消耗 0.2× 积分，折扣前 0.5×", free: "限时免费，原价 0.1×" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a model's credit rate shows by it`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 600 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-model-rate.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));

      // the provider editor's chips
      await page.goto("http://magpie.test/?view=providers");
      const editor = page.locator("#modal:not([hidden]) .editor");
      await page.locator(".row.provider", { hasText: "Qoder" }).click();
      await editor.locator(".mchips .mchip").first().waitFor();
      const chips = () => editor.locator(".mchips .mchip").evaluateAll((cs) => cs.map((c) => {
        const r = c.querySelector(".badge.rate");
        return [c.querySelector("span").textContent, r ? [r.querySelector("s")?.textContent ?? "", r.querySelector("span")?.textContent ?? "", r.title] : null];
      }));
      assert.deepEqual(await chips(), [
        ["Qwen3.8-Max", ["", "0.5×", w.costs]],
        ["Qwen3.8-Max Off-Peak", ["0.5×", "0.2×", w.was]],
        ["Qwen3.8-Flash", ["0.1×", "", w.free]],
        ["Deepseek-V4.1-Flash (SG)", ["", "0.03×", lang === "en" ? "costs 0.03× credits a request" : "每次请求消耗 0.03× 积分"]],
        ["Plain", null],
      ]);
      // muted and small: grey, as the context badge, not the green FREE;
      // the old price struck through
      const look = await editor.locator(".mchip .badge.rate").first().evaluate((b) => {
        const s = getComputedStyle(b), ctx = document.createElement("span");
        ctx.className = "badge ctx";
        b.parentNode.append(ctx);
        const c = getComputedStyle(ctx), out = { color: s.color, bg: s.backgroundColor, ctxColor: c.color, ctxBg: c.backgroundColor, size: s.fontSize, upper: s.textTransform };
        ctx.remove();
        return out;
      });
      assert.equal(look.color, look.ctxColor, "grey as the context badge");
      assert.equal(look.bg, look.ctxBg);
      assert.equal(look.size, "9px");
      assert.equal(look.upper, "none");
      assert.match(await editor.locator(".mchip .badge.rate s").first().evaluate((e) => getComputedStyle(e).textDecorationLine), /line-through/);
      const border = await page.evaluate(() => [...document.querySelectorAll(".mchip, .mchip *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no border stripes");

      // a click on a chip's rate is a click on the chip and moves nothing (the
      // chip brought into view first, as a reader's would be)
      const scrolled = () => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop > 0).map((e) => `${e.className}:${e.scrollTop}`)].join(" "));
      const badge = editor.locator(".mchip .badge.rate").first();
      await badge.scrollIntoViewIfNeeded();
      const before = await scrolled();
      await badge.click();
      // none picked, it was served: the click picks it alone (#681)
      assert.equal(await editor.locator(".mchips .mchip.on").count(), 1, "the chip picked alone");
      assert.equal(await editor.locator(".mchips .mchip.auto").count(), 0);
      assert.equal(await scrolled(), before, "the page moved");
      await page.keyboard.press("Escape");
      await page.locator("dialog.action-confirm[open] button").last().click();
      await page.locator("#modal").waitFor({ state: "hidden" });

      // an agent's model picker
      await page.goto("http://magpie.test/");
      await page.locator('.row.agent[data-id="claude"] > .field.ag-start[data-key="model"]').click();
      const list = page.locator("#pop:not([hidden]) #list");
      await list.locator("li").filter({ hasText: "Qwen3.8-Max" }).first().waitFor();
      const rows = await list.locator("li[data-i]").evaluateAll((ls) => ls.map((l) => {
        const r = l.querySelector(".badge.rate");
        return [l.querySelector(".v").textContent, !!l.querySelector(".badge.free"), r ? r.textContent : null];
      }));
      // the models' rows, by name (the picker's Default aside, the current
      // one shown first)
      const by = Object.fromEntries(rows.map((r) => [r[0], r.slice(1)]));
      assert.deepEqual([by["Qwen3.8-Max"], by["Qwen3.8-Flash"], by["Plain"]], [[false, "0.5×"], [true, "0.1×"], [false, null]]);
      assert.equal(await list.locator("li .badge.rate").first().getAttribute("title"), w.costs);

      const missing = await page.evaluate(() => ["costs {rate} credits a request", "costs {rate} credits a request, {was} before the discount", "free now, {was} before the discount"].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
