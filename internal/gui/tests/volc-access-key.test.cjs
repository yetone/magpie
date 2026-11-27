// Run with Node's test runner and Playwright on the module path; see README.md.
// A Volcengine Ark plan's windows are told only to the account's access key
// (#1427): the editor of an Ark provider, saved or being added, has
// AccessKey ID and Secret Access Key. The saved Secret is never shown, only
// that one is saved; Save posts the ID as typed and a Secret only when a new
// one was typed, Remove posts clearAccessKey, and an ID with no Secret is
// refused before anything is posted, Advanced opened to the Secret. They
// are under Advanced, open when a key is saved, folded when none is. Other
// providers have neither and post none. In English and Chinese, and every
// string in every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const ark = {
  id: "volcengine", name: "Volcengine Ark", icon: "volcengine-color", preset: "volcengine", chat: "https://ark.cn-beijing.volces.com/api/coding/v3", responses: "https://ark.cn-beijing.volces.com/api/coding/v3", anthropic: "https://ark.cn-beijing.volces.com/api/coding", catalog: "",
  models: [{ id: "ark-code-latest", name: "ark-code-latest", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "…ab12" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
  accessKey: { id: "AKLTsaved", secretSet: true },
};
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};
const presets = [
  { id: "volcengine", name: "Volcengine Ark", icon: "volcengine-color", kind: "vendor", chat: "https://ark.cn-beijing.volces.com/api/coding/v3", added: true, accessKey: true,
    regionLabel: "Plan", regions: [{ id: "coding", name: "Coding Plan", chat: "https://ark.cn-beijing.volces.com/api/coding/v3" }, { id: "agent", name: "Agent Plan", chat: "https://ark.cn-beijing.volces.com/api/plan/v3" }] },
  { id: "deepseek", name: "DeepSeek", icon: "deepseek", kind: "vendor", chat: "https://api.deepseek.com/v1", added: false },
];

function serve(lang, posts, fresh) {
  // fresh: Ark not added yet, so its tile adds a new one
  const providers = fresh
    ? { providers: [relay], presets: presets.map((x) => ({ ...x, added: false })), excluded: [], gateway: { running: true, window: true } }
    : { providers: [ark, relay], presets, excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
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

const strings = [
  "AccessKey ID", "Secret Access Key", "saved · paste a new one to replace it", "Remove",
  "Optional: with the account's access key (Volcengine console → API Access Keys), the Usage page shows the plan's 5-hour, weekly and monthly windows. Requests use the plan's API key alone.",
  "Give the access key's Secret too",
];
const words = {
  en: { id: "AccessKey ID", secret: "Secret Access Key", saved: "saved · paste a new one to replace it", remove: "Remove", save: "Save", add: "Add", cancel: "Cancel", one: "Give the access key's Secret too" },
  zh: { id: "AccessKey ID", secret: "Secret Access Key", saved: "已保存 · 粘贴新的即可替换", remove: "移除", save: "保存", add: "添加", cancel: "取消", one: "请同时填写访问密钥的 Secret" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    const start = async (t, name, fresh = false) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${name}-volc-access-key.png`) });
        }
        await browser.close();
      });
      const page = await (await browser.newContext({ viewport: { width: 900, height: 560 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posts = [];
      await page.route("**/*", serve(lang, posts, fresh));
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
    const press = async (page, posts, label) => {
      const n = posts.length;
      const b = page.locator(".editor .bar").getByRole("button", { name: label, exact: true });
      await b.scrollIntoViewIfNeeded();
      await b.click();
      for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
      return posts.length > n ? posts.at(-1) : null;
    };

    test(`${engine} ${lang}: an Ark provider's access key is shown, kept, replaced and removed`, async (t) => {
      const { page, errors, posts } = await start(t, "saved");
      await edit(page, "Volcengine Ark");
      const labels = await page.locator(".editor label").allTextContents();
      assert(labels.includes(w.id) && labels.includes(w.secret), labels.join(" | "));
      assert.equal(await ak(page).inputValue(), "AKLTsaved", "the ID is shown");
      assert.equal(await sk(page).inputValue(), "", "the saved Secret is not");
      assert.equal(await sk(page).getAttribute("type"), "password");
      assert.equal(await sk(page).getAttribute("placeholder"), w.saved);

      // left as it is: the ID posted, no Secret, nothing cleared
      let saved = await press(page, posts, w.save);
      assert.equal(saved.action, "save");
      assert.equal(saved.body.accessKeyID, "AKLTsaved");
      assert(!("secretAccessKey" in saved.body) && !("clearAccessKey" in saved.body), JSON.stringify(saved.body));

      // a new Secret, trimmed
      await edit(page, "Volcengine Ark");
      await sk(page).fill(" new-secret ");
      saved = await press(page, posts, w.save);
      assert.equal(saved.body.secretAccessKey, "new-secret");
      assert(!("clearAccessKey" in saved.body));

      // Remove clears both
      await edit(page, "Volcengine Ark");
      await page.locator(".editor").getByRole("button", { name: w.remove, exact: true }).first().click();
      assert.equal(await ak(page).inputValue(), "");
      assert.notEqual(await sk(page).getAttribute("placeholder"), w.saved);
      saved = await press(page, posts, w.save);
      assert.equal(saved.body.accessKeyID, "");
      assert.equal(saved.body.clearAccessKey, true);
      assert(!("secretAccessKey" in saved.body));

      const missing = await page.evaluate((keys) => Object.fromEntries(["zh", "zh-TW", "ja", "de"].map((l) => [l, keys.filter((k) => !I18N[l][k])])), strings);
      assert.deepEqual(missing, { zh: [], "zh-TW": [], ja: [], de: [] }, "every string in every language");
      const border = await page.evaluate(() => [...document.querySelectorAll(".editor, .editor *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no border stripes");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: another provider has no access key fields and posts none`, async (t) => {
      const { page, errors, posts } = await start(t, "relay");
      await edit(page, "Relay");
      assert.equal(await ak(page).count(), 0);
      assert.equal(await sk(page).count(), 0);
      const saved = await press(page, posts, w.save);
      assert.equal(saved.body.id, "relay");
      assert(!("accessKeyID" in saved.body) && !("secretAccessKey" in saved.body) && !("clearAccessKey" in saved.body));
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: Ark being added takes an access key, DeepSeek doesn't, an ID alone is refused`, async (t) => {
      const { page, errors, posts } = await start(t, "add", true);
      await page.locator("#addProvider").click();
      const sheet = page.locator("#addSheet");
      await sheet.locator('.tile[data-pick="DeepSeek"]').click();
      await page.locator(".editor.new").waitFor();
      assert.equal(await ak(page).count(), 0, "DeepSeek has none");
      await page.locator(".editor .bar").getByRole("button", { name: w.cancel, exact: true }).click();
      await page.locator(".editor").waitFor({ state: "detached" });

      await sheet.locator('.tile[data-pick="Volcengine Ark"]').click();
      await page.locator(".editor.new").waitFor();
      // optional: under Advanced, folded
      const adv = page.locator(".editor details.advanced");
      assert.equal(await adv.evaluate((d) => d.open), false, "Advanced folded");
      assert.equal(await ak(page).evaluate((x) => !!x.closest("details.advanced")), true, "under Advanced");
      assert.equal(await ak(page).isVisible(), false);
      await adv.locator(":scope > summary").scrollIntoViewIfNeeded();
      await adv.locator(":scope > summary").click();
      await ak(page).waitFor();
      assert.equal(await sk(page).inputValue(), "");
      assert.notEqual(await sk(page).getAttribute("placeholder"), w.saved, "nothing is saved yet");
      await page.locator(".editor.new input[type=password]").first().fill("ark-key");
      await ak(page).fill(" AKLTnew ");
      assert.equal(await press(page, posts, w.add), null, "an ID with no Secret posts nothing");
      assert.equal(await page.locator(".editor .editor-error").textContent(), w.one);
      assert.equal(await adv.evaluate((d) => d.open), true);
      await sk(page).fill("sk-new");
      const saved = await press(page, posts, w.add);
      assert.equal(saved.body.new, true);
      assert.equal(saved.body.preset, "volcengine");
      assert.equal(saved.body.accessKeyID, "AKLTnew");
      assert.equal(saved.body.secretAccessKey, "sk-new");
      assert.deepEqual(errors, []);
    });
  }
}
