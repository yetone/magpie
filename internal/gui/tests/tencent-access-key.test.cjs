// Run with Node's test runner and Playwright on the module path; see README.md.
// Tencent Cloud's Token Plan 企业版 tells its usage only to the account's
// API key, a SecretId and SecretKey (#1574): a Tencent Cloud provider's
// editor asks for them, named so, at the Enterprise plan regions only. At
// the personal plan or pay as you go they are hidden, and come back when
// an Enterprise region is picked. The saved SecretKey is never shown, only
// that one is saved; Save posts the SecretId and a SecretKey only when a new
// one was typed, Remove posts clearAccessKey, and a SecretId with no
// SecretKey is refused at an Enterprise region, never at one that hides
// them. They are under Advanced, open when a key is saved and folded when
// none is (tencent-plan-api-key-only.test.cjs). A 420px window; English and
// Chinese, every string in every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const plan = { chat: "https://api.lkeap.cloud.tencent.com/plan/v3", anthropic: "https://api.lkeap.cloud.tencent.com/plan/anthropic" };
const cn = { chat: "https://tokenhub.tencentmaas.com/v1", responses: "https://tokenhub.tencentmaas.com/v1", anthropic: "https://tokenhub.tencentmaas.com" };
const intl = { chat: "https://tokenhub-intl.tencentmaas.com/v1", responses: "https://tokenhub-intl.tencentmaas.com/v1", anthropic: "https://tokenhub-intl.tencentmaas.com" };
const ent = { chat: "https://tokenhub.tencentmaas.com/plan/v3", responses: "https://tokenhub.tencentmaas.com/plan/v3", anthropic: "https://tokenhub.tencentmaas.com/plan/anthropic" };
const entIntl = { chat: "https://tokenhub-intl.tencentmaas.com/plan/v3", responses: "https://tokenhub-intl.tencentmaas.com/plan/v3", anthropic: "https://tokenhub-intl.tencentmaas.com/plan/anthropic" };
// as /api/providers serves the preset: an access key at the two
// Enterprise regions alone (presetAccessKey)
const tencent = (added) => ({
  id: "tencent-cloud", name: "Tencent Cloud", icon: "tencentcloud-color", kind: "vendor", hosts: true, added, chat: plan.chat, anthropic: plan.anthropic,
  accessKey: true, accessKeyVendor: "tencent", accessKeyRegions: ["enterprise", "enterprise-intl"],
  regions: [{ id: "plan", name: "Plan · China", ...plan }, { id: "cn", name: "Pay as you go · China", ...cn }, { id: "intl", name: "Pay as you go · Global", ...intl },
    { id: "enterprise", name: "Enterprise plan · China", ...ent }, { id: "enterprise-intl", name: "Enterprise plan · Global", ...entIntl }],
});
const saved = (id, name, at, accessKey) => ({
  id, name, icon: "tencentcloud-color", preset: "tencent-cloud", ...at, catalog: "",
  models: [{ id: "hy3", name: "hy3", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-tp…ab12" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", accessKey,
});
const team = saved("tencent-cloud", "Tencent Cloud", ent, { vendor: "tencent", id: "AKIDsaved", secretSet: true });
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [], agents: [], fallback: [], headers: {}, key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};
const personal = saved("tc-personal", "Tencent personal", plan, { vendor: "tencent", id: "", secretSet: false });

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

const hint = "Optional, for Token Plan Enterprise only: with the Tencent Cloud account's API key (console → Access management → API keys; a sub-user's that may read TokenHub is enough), the Usage page shows the plan's usage. Requests use the plan's API key alone; the personal plan needs nothing more.";
const strings = ["SecretId", "SecretKey", hint, "Enterprise plan · China", "Enterprise plan · Global"];
const words = {
  en: { saved: "saved · paste a new one to replace it", remove: "Remove", save: "Save", add: "Add", one: "Give the access key's Secret too" },
  zh: { saved: "已保存 · 粘贴新的即可替换", remove: "移除", save: "保存", add: "添加", one: "请同时填写访问密钥的 Secret" },
};
// the region buttons in the preset's order
const PLAN = 0, CN = 1, ENT = 3, ENT_INTL = 4;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    const start = async (t, name, list) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${name}-tencent-access-key.png`), fullPage: true });
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
      return { page, errors, posts };
    };
    const edit = async (page, name) => {
      await page.locator(".row.provider", { hasText: name }).click();
      // drawn: the proxy is under Advanced, folded
      await page.locator(".editor .proxy-mode").waitFor({ state: "attached" });
    };
    const ak = (page) => page.locator(".editor .volc-ak");
    const sk = (page) => page.locator(".editor .volc-sk");
    const region = async (page, i) => {
      const b = page.locator(".editor .segs.regions .opt").nth(i);
      await b.scrollIntoViewIfNeeded();
      await b.click();
    };
    // Advanced unfolded, where the access key is
    const unfold = async (page) => {
      const d = page.locator(".editor details.advanced");
      if (await d.evaluate((x) => x.open)) return;
      await d.locator(":scope > summary").scrollIntoViewIfNeeded();
      await d.locator(":scope > summary").click();
      await ak(page).waitFor();
    };
    const shown = async (page) => {
      const [a, s] = [await ak(page).isVisible(), await sk(page).isVisible()];
      assert.equal(a, s, "both or neither");
      return a;
    };
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

    test(`${engine} ${lang}: an Enterprise plan provider asks for its SecretId and SecretKey, kept, replaced and removed`, async (t) => {
      const { page, errors, posts } = await start(t, "team", [team, personal]);
      await edit(page, "Tencent Cloud");
      const labels = await page.locator(".editor label").allTextContents();
      assert(labels.includes("SecretId") && labels.includes("SecretKey"), labels.join(" | "));
      assert(!labels.includes("AccessKey ID"), "named as Tencent names them");
      assert(await shown(page));
      assert.equal(await ak(page).inputValue(), "AKIDsaved");
      assert.equal(await sk(page).inputValue(), "", "the saved SecretKey is not shown");
      assert.equal(await sk(page).getAttribute("type"), "password");
      assert.equal(await sk(page).getAttribute("placeholder"), w.saved);
      const tip = await page.locator(".editor .hint").allTextContents();
      assert(tip.some((x) => x.includes("TokenHub")), tip.join(" | "));
      assert(!tip.some((x) => x.includes("Volcengine") || x.includes("火山")), "not Volcengine's hint");
      await fits(page);

      // the personal plan has no usage API: hidden; Enterprise · Global
      // brings them back
      await region(page, PLAN);
      assert.equal(await shown(page), false, "hidden at the personal plan");
      await region(page, CN);
      assert.equal(await shown(page), false, "hidden at pay as you go");
      await region(page, ENT_INTL);
      assert(await shown(page), "shown at Enterprise · Global");
      assert.equal(await ak(page).inputValue(), "AKIDsaved", "what was there is kept");

      // left as it is: the SecretId posted, no SecretKey, nothing cleared
      let body = (await press(page, posts, w.save)).body;
      assert.equal(body.accessKeyID, "AKIDsaved");
      assert.equal(body.chat, entIntl.chat);
      assert(!("secretAccessKey" in body) && !("clearAccessKey" in body), JSON.stringify(body));

      // a new SecretKey, trimmed
      await edit(page, "Tencent Cloud");
      await sk(page).fill(" new-secret ");
      body = (await press(page, posts, w.save)).body;
      assert.equal(body.secretAccessKey, "new-secret");

      // Remove clears both; the placeholder names the SecretKey
      await edit(page, "Tencent Cloud");
      await page.locator(".editor").getByRole("button", { name: w.remove, exact: true }).first().click();
      assert.equal(await ak(page).inputValue(), "");
      assert.equal(await sk(page).getAttribute("placeholder"), "SecretKey");
      body = (await press(page, posts, w.save)).body;
      assert.equal(body.accessKeyID, "");
      assert.equal(body.clearAccessKey, true);

      const missing = await page.evaluate((keys) => Object.fromEntries(["zh", "zh-TW", "ja", "de"].map((l) => [l, keys.filter((k) => !I18N[l][k])])), strings);
      assert.deepEqual(missing, { zh: [], "zh-TW": [], ja: [], de: [] }, "every string in every language");
      const border = await page.evaluate(() => [...document.querySelectorAll(".editor, .editor *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no border stripes");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a personal plan provider has them hidden, and Enterprise · China asks for both`, async (t) => {
      const { page, errors, posts } = await start(t, "personal", [team, personal]);
      await edit(page, "Tencent personal");
      assert.equal(await ak(page).count(), 1);
      assert.equal(await shown(page), false, "hidden at the personal plan");
      let body = (await press(page, posts, w.save)).body;
      assert.equal(body.id, "tc-personal");
      assert(!body.accessKeyID && !("secretAccessKey" in body), JSON.stringify(body));

      await edit(page, "Tencent personal");
      await region(page, ENT);
      await unfold(page);
      assert(await shown(page));
      assert.notEqual(await sk(page).getAttribute("placeholder"), w.saved, "nothing is saved yet");
      await ak(page).fill(" AKIDnew ");
      assert.equal(await press(page, posts, w.save), null, "a SecretId with no SecretKey posts nothing");
      assert.equal(await page.locator(".editor .editor-error").textContent(), w.one);
      await sk(page).fill("sk-new");
      body = (await press(page, posts, w.save)).body;
      assert.equal(body.chat, ent.chat);
      assert.equal(body.accessKeyID, "AKIDnew");
      assert.equal(body.secretAccessKey, "sk-new");
      await fits(page);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: Tencent Cloud being added asks for them at an Enterprise region only`, async (t) => {
      const { page, errors, posts } = await start(t, "add", [relay]);
      await page.locator("#addProvider").click();
      await page.locator('#addSheet .tile[data-pick="Tencent Cloud"]').click();
      await page.locator(".editor.new").waitFor();
      assert.equal(await shown(page), false, "hidden at the plan it opens at");
      await region(page, ENT);
      await unfold(page);
      assert(await shown(page));
      const labels = await page.locator(".editor label").allTextContents();
      assert(labels.includes("SecretId") && labels.includes("SecretKey"), labels.join(" | "));
      await page.locator(".editor.new input[type=password]").first().fill("sk-tp-key");
      await ak(page).fill("AKIDadd");
      // moved back to the personal plan, a SecretId alone is no reason to refuse
      await region(page, PLAN);
      const body = (await press(page, posts, w.add)).body;
      assert.equal(body.new, true);
      assert.equal(body.preset, "tencent-cloud");
      assert.equal(body.chat, plan.chat);
      assert(!("secretAccessKey" in body));
      await fits(page);
      assert.deepEqual(errors, []);
    });
  }
}
