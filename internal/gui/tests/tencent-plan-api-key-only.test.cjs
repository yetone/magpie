// Run with Node's test runner and Playwright on the module path; see README.md.
// big_yooooo on X: Tencent Cloud added at "Plan · China" (Token Plan 个人版)
// asked for a SecretId and SecretKey, which read as required though the
// plan's API key is all its requests use. They only let the Usage page read
// Token Plan 企业版's usage (tencent_usage.go: 个人版 has no API for it),
// so they sit under Advanced, folded, and the hint says they are optional.
// Here: being added and saved at the plan, the editor shows no SecretId or
// SecretKey outside Advanced and posts with the API key alone; opened,
// Advanced has both with the optional hint; a SecretId with no SecretKey
// opens Advanced again and is refused. The preset is served as
// /api/providers serves the real one (regions plan, cn, intl,
// enterprise-intl; the access key at plan and enterprise-intl). A 420px
// window, both engines, every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const plan = { chat: "https://tokenhub.tencentmaas.com/plan/v3", responses: "https://tokenhub.tencentmaas.com/plan/v3", anthropic: "https://tokenhub.tencentmaas.com/plan/anthropic" };
const cn = { chat: "https://tokenhub.tencentmaas.com/v1", responses: "https://tokenhub.tencentmaas.com/v1", anthropic: "https://tokenhub.tencentmaas.com" };
const intl = { chat: "https://tokenhub-intl.tencentmaas.com/v1", responses: "https://tokenhub-intl.tencentmaas.com/v1", anthropic: "https://tokenhub-intl.tencentmaas.com" };
const entIntl = { chat: "https://tokenhub-intl.tencentmaas.com/plan/v3", responses: "https://tokenhub-intl.tencentmaas.com/plan/v3", anthropic: "https://tokenhub-intl.tencentmaas.com/plan/anthropic" };
const tencent = (added) => ({
  id: "tencent-cloud", name: "Tencent Cloud", icon: "tencentcloud-color", kind: "vendor", hosts: true, added, ...plan,
  accessKey: true, accessKeyVendor: "tencent", accessKeyRegions: ["plan", "enterprise-intl"],
  regions: [{ id: "plan", name: "Plan · China", ...plan }, { id: "cn", name: "Pay as you go · China", ...cn }, { id: "intl", name: "Pay as you go · Global", ...intl },
    { id: "enterprise-intl", name: "Enterprise plan · Global", ...entIntl }],
});
const personal = {
  id: "tencent-cloud", name: "Tencent Cloud", icon: "tencentcloud-color", preset: "tencent-cloud", ...plan, catalog: "",
  models: [{ id: "tc-code-latest", name: "tc-code-latest", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-tp…ab12" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
  accessKey: { vendor: "tencent", id: "", secretSet: false },
};
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [], agents: [], fallback: [], headers: {}, key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};

function serve(lang, list, posts) {
  const providers = { providers: list, presets: [tencent(list.some((x) => x.preset === "tencent-cloud"))], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/provider/") && route.request().method() === "POST") {
      posts.push({ action: url.pathname.slice("/api/provider/".length), body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const HINT = "Optional, for Token Plan Enterprise only: with the Tencent Cloud account's API key (console → Access management → API keys; a sub-user's that may read TokenHub is enough), the Usage page shows the plan's usage. Requests use the plan's API key alone; the personal plan needs nothing more.";
const WORDS = ["SecretId", "SecretKey", HINT, "Advanced", "Add", "Save", "Give the access key's Secret too"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    const start = async (t, name, list) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${name}-tencent-plan-api-key-only.png`), fullPage: true });
        }
        await browser.close();
      });
      const page = await (await browser.newContext({ viewport: { width: 420, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posts = [];
      await page.route("**/*", serve(lang, list, posts));
      await page.goto("http://magpie.test/?view=providers");
      // every word in this language, read from the page's own I18N
      const missing = await page.evaluate(([l, keys]) => l === "en" ? [] : keys.filter((k) => !I18N[l][k]), [lang, WORDS]);
      assert.deepEqual(missing, [], `said in ${lang}`);
      const w = Object.fromEntries(await page.evaluate(([l, keys]) => keys.map((k) => [k, l === "en" ? k : I18N[l][k]]), [lang, WORDS]));
      return { page, errors, posts, w };
    };
    const ak = (page) => page.locator(".editor .volc-ak");
    const sk = (page) => page.locator(".editor .volc-sk");
    const adv = (page) => page.locator(".editor details.advanced");
    // the SecretId and SecretKey, and their labels, only inside Advanced
    const onlyUnderAdvanced = (page) => page.evaluate(() => {
      const ed = document.querySelector(".editor");
      const outside = (x) => !x.closest("details.advanced");
      const inputs = [...ed.querySelectorAll(".volc-ak, .volc-sk")];
      const labels = [...ed.querySelectorAll("label")].filter((l) => /^Secret(Id|Key)$/.test(l.textContent.trim()));
      return { inputs: inputs.length, outside: [...inputs, ...labels].filter(outside).length };
    });
    const visibleSecretLabels = (page) => page.locator(".editor label").filter({ hasText: /^Secret(Id|Key)$/, visible: true }).count();
    const press = async (page, posts, label) => {
      const n = posts.length;
      const b = page.locator(".editor .bar").getByRole("button", { name: label, exact: true });
      await b.scrollIntoViewIfNeeded();
      await b.click();
      for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
      return posts.length > n ? posts.at(-1) : null;
    };
    const fits = async (page) => {
      const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      assert(over <= 0, `the page scrolls sideways by ${over}px`);
    };
    const openAdvanced = async (page) => {
      const s = adv(page).locator(":scope > summary");
      await s.scrollIntoViewIfNeeded();
      await s.click();
      await ak(page).waitFor();
    };

    test(`${engine} ${lang}: Tencent Cloud added at its Token Plan takes the API key alone`, async (t) => {
      const { page, errors, posts, w } = await start(t, "add", [relay]);
      await page.locator("#addProvider").click();
      await page.locator('#addSheet .tile[data-pick="Tencent Cloud"]').click();
      await page.locator(".editor.new").waitFor();
      assert.deepEqual(await onlyUnderAdvanced(page), { inputs: 2, outside: 0 }, "the access key is under Advanced");
      assert.equal(await adv(page).evaluate((d) => d.open), false, "Advanced folded");
      assert.equal(await ak(page).isVisible(), false);
      assert.equal(await sk(page).isVisible(), false);
      assert.equal(await visibleSecretLabels(page), 0, "no SecretId or SecretKey in sight");
      await fits(page);

      await page.locator(".editor.new input[type=password]").first().fill("sk-tp-key");
      const body = (await press(page, posts, w.Add)).body;
      assert.equal(await page.locator(".editor-error").count(), 0, "nothing refused");
      assert.equal(body.new, true);
      assert.equal(body.preset, "tencent-cloud");
      // the preset's own endpoints (the plan) when none was picked
      assert([undefined, "", plan.chat].includes(body.chat), body.chat);
      assert.equal(body.key, "sk-tp-key");
      assert(!body.accessKeyID && !("secretAccessKey" in body), JSON.stringify(body));
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a saved Token Plan provider keeps them folded, optional, and refuses a SecretId alone`, async (t) => {
      const { page, errors, posts, w } = await start(t, "saved", [personal]);
      await page.locator(".row.provider", { hasText: "Tencent Cloud" }).click();
      await page.locator(".editor .proxy-mode").waitFor({ state: "attached" });
      assert.deepEqual(await onlyUnderAdvanced(page), { inputs: 2, outside: 0 });
      assert.equal(await adv(page).evaluate((d) => d.open), false, "Advanced folded with no access key saved");
      assert.equal(await visibleSecretLabels(page), 0);
      let body = (await press(page, posts, w.Save)).body;
      assert.equal(body.id, "tencent-cloud");
      assert(!body.accessKeyID && !("secretAccessKey" in body), JSON.stringify(body));

      // Advanced opened: both, and the hint that says they are optional
      await page.locator(".row.provider", { hasText: "Tencent Cloud" }).click();
      await page.locator(".editor .proxy-mode").waitFor({ state: "attached" });
      await openAdvanced(page);
      assert(await sk(page).isVisible());
      const tips = (await adv(page).locator(".hint").allTextContents()).map((s) => s.trim());
      assert(tips.includes(w[HINT]), tips.join(" | "));

      // a SecretId with Advanced folded again: refused, and Advanced opens on the SecretKey
      await ak(page).fill("AKIDnew");
      await adv(page).locator(":scope > summary").click();
      await page.waitForFunction(() => !document.querySelector(".editor details.advanced").open);
      assert.equal(await press(page, posts, w.Save), null, "a SecretId with no SecretKey posts nothing");
      assert.equal(await page.locator(".editor .editor-error").textContent(), w["Give the access key's Secret too"]);
      assert.equal(await adv(page).evaluate((d) => d.open), true, "Advanced opened to the SecretKey");
      assert(await sk(page).isVisible());
      await sk(page).fill("sk-new");
      body = (await press(page, posts, w.Save)).body;
      assert.equal(body.accessKeyID, "AKIDnew");
      assert.equal(body.secretAccessKey, "sk-new");
      await fits(page);
      assert.deepEqual(errors, []);
    });
  }
}
