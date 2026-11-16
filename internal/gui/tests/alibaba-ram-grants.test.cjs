// Run with Node's test runner and Playwright on the module path; see README.md.
// MetalHqa on X: what an Alibaba Cloud Token Plan's AccessKey needs to read
// the plan's usage wasn't said where the key is given. The editor said "a
// RAM user that may use Model Studio is enough", which is wrong (#6506),
// and the grants Alibaba's docs list (aliRAMHint) were said only in
// English, on the card, after a refusal. Now the editor says them under
// the AccessKey ID, in the reader's language, and a card refused with
// aliRAMHint says them in the reader's language too, the raw error in its
// tooltip. The refusal is read from alibaba_usage.go itself, so a change of
// its words that quotaError no longer matches is red here. A 420px window,
// both engines, every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const GRANTS = "A RAM user's AccessKey needs AliyunTokenPlanReadOnlyAccess and AliyunBSSReadOnlyAccess, and Administrator or Subscription plan in Model Studio → Account Management; the main account's needs none";
const HINT = "The plan's usage is told only to the Alibaba Cloud account's AccessKey (console → AccessKey), not to the plan's API key. Give its AccessKey ID and Secret here for the Usage page to show it. It is used for nothing else.";
const plan = { chat: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", anthropic: "https://token-plan.cn-beijing.maas.aliyuncs.com/apps/anthropic" };
const ali = {
  id: "bailian-token-plan", name: "Bailian Token Plan", icon: "alibabacloud-color", preset: "bailian-token-plan", ...plan, responses: "", catalog: "",
  models: [{ id: "qwen3.6-plus", name: "qwen3.6-plus", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-sp-…ab12" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
  accessKey: { vendor: "alibaba", id: "LTAIsaved", secretSet: true },
};
const volc = {
  id: "volcengine", name: "Volcengine Ark", icon: "volcengine", preset: "volcengine", chat: "https://ark.cn-beijing.volces.com/api/coding/v3", responses: "", anthropic: "", catalog: "",
  models: [], agents: [], fallback: [], headers: {}, key: { set: true, masked: "ark-…key" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
  accessKey: { vendor: "volcengine", id: "", secretSet: false },
};

// the card's error as aliAskUsage words it on BailianGateway.Team.NotAuthorised
async function refusal() {
  const src = await fs.readFile(path.resolve(__dirname, "../../provider/alibaba_usage.go"), "utf8");
  const hint = /\baliRAMHint\s*=\s*"((?:[^"\\]|\\.)*)"/.exec(src);
  assert.ok(hint, "aliRAMHint is in alibaba_usage.go");
  return `Alibaba Cloud's console: BailianGateway.Team.NotAuthorised: ${JSON.parse(`"${hint[1]}"`)}`;
}

function serve(lang, quotas) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  const providers = { providers: [ali, volc], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (body) => route.fulfill({ json: body });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    const start = async (t, view, quotas) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 420, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, quotas));
      await page.goto(`http://magpie.test/?view=${view}`);
      const said = lang === "en" ? [GRANTS, HINT] : await page.evaluate(([l, keys]) => keys.map((k) => I18N[l][k]), [lang, [GRANTS, HINT]]);
      return { page, errors, said };
    };
    const fits = async (page) => {
      const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      assert(over <= 0, `the page scrolls sideways by ${over}px`);
    };

    test(`${engine} ${lang}: the editor says what an Alibaba Token Plan's AccessKey needs, under its AccessKey ID`, async (t) => {
      const { page, errors, said: [grants, hint] } = await start(t, "providers", []);
      if (lang !== "en") {
        const missing = await page.evaluate(([l, keys]) => keys.filter((k) => !I18N[l][k]), [lang, [GRANTS, HINT]]);
        assert.deepEqual(missing, [], "said in this language");
      }
      assert.ok(grants && hint);
      await page.locator(".row.provider", { hasText: "Bailian Token Plan" }).click();
      const ak = page.locator(".editor .volc-ak");
      await ak.waitFor();
      // the note sits right under the AccessKey ID, and its input reads it
      const note = ak.locator("xpath=following-sibling::div[contains(@class,'hint')]");
      assert.equal((await note.textContent()).trim(), grants);
      assert.equal(await ak.getAttribute("aria-describedby"), await note.getAttribute("id"));
      for (const name of ["AliyunTokenPlanReadOnlyAccess", "AliyunBSSReadOnlyAccess"]) assert.ok(grants.includes(name), `${grants} names ${name}`);
      const tips = await page.locator(".editor .hint").allTextContents();
      assert.ok(tips.includes(hint), tips.join(" | "));
      assert.ok(!tips.some((x) => /Model Studio is enough|百炼使用权限|百鍊使用權限|利用できる RAM|nutzen darf/.test(x)), "the wrong 'any Model Studio RAM user' is gone");
      await fits(page);

      // another vendor's access key has no such note
      await page.keyboard.press("Escape");
      await page.locator(".row.provider", { hasText: "Volcengine Ark" }).click();
      const vak = page.locator(".editor .volc-ak");
      await vak.waitFor();
      assert.equal(await vak.locator("xpath=following-sibling::div[contains(@class,'hint')]").count(), 0);
      assert.ok(!(await page.locator(".editor").textContent()).includes("AliyunTokenPlan"));
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a card refused for a RAM user's grants says them in the reader's language`, async (t) => {
      const raw = await refusal();
      const quotas = [
        { provider: "bailian-token-plan", name: "Bailian Token Plan", icon: "alibabacloud-color", plan: "Token Plan", error: raw, windows: [] },
      ];
      const { page, errors, said: [grants] } = await start(t, "usage", quotas);
      const err = page.locator(".subscription-card", { hasText: "Bailian Token Plan" }).locator(".subscription-error");
      await err.waitFor();
      assert.equal(await err.getAttribute("title"), raw, "the raw refusal in the tooltip");
      assert.ok((await err.textContent()).includes(grants), await err.textContent());
      if (lang !== "en") assert.ok(!(await err.textContent()).includes("from the main account"), "not the English refusal");
      await fits(page);
      assert.deepEqual(errors, []);
    });
  }
}
