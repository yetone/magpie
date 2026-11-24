// Run with Node's test runner and Playwright on the module path; see README.md.
// Amazon Bedrock takes an AWS access key in place of its API key (#7687).
// Its editor has Access key ID, Secret access key and Session token at the
// top of Advanced, folded until one is saved. The saved Secret and token are
// never shown, only that they are saved; Save posts the ID as typed and a
// Secret or token only when a new one was typed; the token's Remove posts
// clearSessionToken and the key's Remove clearAccessKey; an ID with no
// Secret is refused, with Advanced opened to it. A Bedrock being added
// takes the key with no API key. In English and Chinese, and every string
// in every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const urls = { chat: "https://bedrock-runtime.us-east-1.amazonaws.com/openai/v1", responses: "https://bedrock-runtime.us-east-1.amazonaws.com/openai/v1", anthropic: "https://bedrock-runtime.us-east-1.amazonaws.com/anthropic" };
const bedrock = (accessKey) => ({
  id: "bedrock", name: "Amazon Bedrock", icon: "bedrock-color", preset: "bedrock", ...urls, catalog: "",
  models: [{ id: "global.anthropic.claude-opus-5-5", name: "Claude Opus 5.5", on: true }], agents: [], fallback: [], headers: {},
  key: { set: false }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
  accessKey,
});
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};
const presets = [
  { id: "bedrock", name: "Amazon Bedrock", icon: "bedrock-color", kind: "vendor", ...urls, added: true, accessKey: true, accessKeyVendor: "aws" },
  { id: "deepseek", name: "DeepSeek", icon: "deepseek", kind: "vendor", chat: "https://api.deepseek.com/v1", added: false },
];

function serve(lang, posts, mode) {
  const providers = mode === "fresh"
    ? { providers: [relay], presets: presets.map((x) => ({ ...x, added: false })), excluded: [], gateway: { running: true, window: true } }
    : { providers: [bedrock(mode === "saved" ? { vendor: "aws", id: "ASIAsaved", secretSet: true, tokenSet: true } : { vendor: "aws", id: "", secretSet: false })], presets, excluded: [], gateway: { running: true, window: true } };
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
  "Access key ID", "Secret access key", "Session token", "optional", "saved · paste a new one to replace it", "Remove", "Give the access key's Secret too",
  "In place of the API key: an AWS access key that may call Bedrock, which signs each request. A temporary key needs its session token too.",
];
const words = {
  en: { id: "Access key ID", secret: "Secret access key", token: "Session token", optional: "optional", saved: "saved · paste a new one to replace it", remove: "Remove", save: "Save", add: "Add", one: "Give the access key's Secret too" },
  zh: { id: "访问密钥 ID", secret: "私有访问密钥", token: "会话令牌", optional: "可不填", saved: "已保存 · 粘贴新的即可替换", remove: "移除", save: "保存", add: "添加", one: "请同时填写访问密钥的 Secret" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    const start = async (t, mode) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${mode}-bedrock-access-key.png`) });
        }
        await browser.close();
      });
      const page = await (await browser.newContext({ viewport: { width: 900, height: 560 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posts = [];
      await page.route("**/*", serve(lang, posts, mode));
      await page.goto("http://magpie.test/?view=providers");
      return { page, errors, posts };
    };
    const edit = async (page) => {
      await page.locator(".row.provider", { hasText: "Amazon Bedrock" }).click();
      await page.locator(".editor .proxy-mode").waitFor({ state: "attached" });
      // the token's row comes with awstoken.js, imported as the editor draws
      await page.locator(".editor .aws-token").waitFor({ state: "attached" });
    };
    const adv = (page) => page.locator(".editor details.advanced");
    const ak = (page) => page.locator(".editor .volc-ak");
    const sk = (page) => page.locator(".editor .volc-sk");
    const st = (page) => page.locator(".editor .aws-token");
    const removeBy = (page, sel) => page.locator(".editor .pair", { has: page.locator(sel) }).getByRole("button", { name: w.remove, exact: true });
    const press = async (page, posts, label) => {
      const n = posts.length;
      const b = page.locator(".editor .bar").getByRole("button", { name: label, exact: true });
      await b.scrollIntoViewIfNeeded();
      await b.click();
      for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
      return posts.length > n ? posts.at(-1) : null;
    };
    const underAdvanced = (page) => page.evaluate(() => [".volc-ak", ".volc-sk", ".aws-token"].map((s) => !!document.querySelector(`.editor details.advanced ${s}`)));

    test(`${engine} ${lang}: a saved AWS key is under an open Advanced, kept, its token replaced and removed`, async (t) => {
      const { page, errors, posts } = await start(t, "saved");
      await edit(page);
      assert.equal(await adv(page).evaluate((d) => d.open), true, "Advanced opens when a key is saved");
      assert.deepEqual(await underAdvanced(page), [true, true, true]);
      const labels = (await adv(page).locator("label").allTextContents()).map((s) => s.trim());
      assert(labels.includes(w.id) && labels.includes(w.secret) && labels.includes(w.token), labels.join(" | "));
      assert.equal(labels.indexOf(w.id) + 1, labels.indexOf(w.secret), "the Secret right after the ID");
      assert.equal(await ak(page).inputValue(), "ASIAsaved");
      assert.equal(await sk(page).inputValue(), "");
      assert.equal(await st(page).inputValue(), "", "the saved token is not shown");
      assert.equal(await st(page).getAttribute("type"), "password");
      assert.equal(await st(page).getAttribute("placeholder"), w.saved);

      // left as it is: the ID, no Secret, no token, nothing cleared
      let saved = await press(page, posts, w.save);
      assert.equal(saved.action, "save");
      assert.equal(saved.body.accessKeyID, "ASIAsaved");
      for (const k of ["secretAccessKey", "sessionToken", "clearAccessKey", "clearSessionToken"]) assert(!(k in saved.body), `${k}: ${JSON.stringify(saved.body)}`);

      // a new token, trimmed
      await edit(page);
      await st(page).fill(" FQoG-new ");
      saved = await press(page, posts, w.save);
      assert.equal(saved.body.sessionToken, "FQoG-new");
      assert(!("clearSessionToken" in saved.body) && !("clearAccessKey" in saved.body));

      // the token alone removed: the key stays
      await edit(page);
      await removeBy(page, ".aws-token").click();
      assert.equal(await st(page).getAttribute("placeholder"), w.optional);
      assert.equal(await removeBy(page, ".aws-token").count(), 0);
      saved = await press(page, posts, w.save);
      assert.equal(saved.body.clearSessionToken, true);
      assert.equal(saved.body.accessKeyID, "ASIAsaved");
      assert(!("clearAccessKey" in saved.body) && !("sessionToken" in saved.body));

      // the key's Remove drops the token with it
      await edit(page);
      await removeBy(page, ".volc-sk").click();
      assert.equal(await ak(page).inputValue(), "");
      assert.equal(await st(page).getAttribute("placeholder"), w.optional);
      saved = await press(page, posts, w.save);
      assert.equal(saved.body.accessKeyID, "");
      assert.equal(saved.body.clearAccessKey, true);
      assert(!("sessionToken" in saved.body) && !("secretAccessKey" in saved.body));

      const missing = await page.evaluate((keys) => Object.fromEntries(["zh", "zh-TW", "ja", "de"].map((l) => [l, keys.filter((k) => !I18N[l][k])])), strings);
      assert.deepEqual(missing, { zh: [], "zh-TW": [], ja: [], de: [] }, "every string in every language");
      const border = await page.evaluate(() => [...document.querySelectorAll(".editor, .editor *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no border stripes");
      assert.equal(await page.locator(".editor select").count(), 0, "no native select");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: without a key Advanced stays folded; an ID alone is refused and opens it`, async (t) => {
      const { page, errors, posts } = await start(t, "none");
      await edit(page);
      assert.equal(await adv(page).evaluate((d) => d.open), false, "folded");
      assert.deepEqual(await underAdvanced(page), [true, true, true]);
      assert.equal(await st(page).getAttribute("placeholder"), w.optional);
      await adv(page).locator("summary").first().click();
      await ak(page).fill(" AKIAnew ");
      await adv(page).evaluate((d) => { d.open = false; });
      assert.equal(await press(page, posts, w.save), null, "an ID with no Secret posts nothing");
      assert.equal(await page.locator(".editor .editor-error").textContent(), w.one);
      assert.equal(await adv(page).evaluate((d) => d.open), true, "opened to the Secret");
      await sk(page).fill(" sk-new ");
      const saved = await press(page, posts, w.save);
      assert.equal(saved.body.accessKeyID, "AKIAnew");
      assert.equal(saved.body.secretAccessKey, "sk-new");
      assert(!("sessionToken" in saved.body) && !("clearSessionToken" in saved.body));
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: Bedrock being added takes the AWS key and its token with no API key`, async (t) => {
      const { page, errors, posts } = await start(t, "fresh");
      await page.locator("#addProvider").click();
      await page.locator('#addSheet .tile[data-pick="Amazon Bedrock"]').click();
      await page.locator(".editor.new").waitFor();
      await ak(page).waitFor({ state: "attached" });
      if (!(await adv(page).evaluate((d) => d.open))) await adv(page).locator("summary").first().click();
      await ak(page).fill("ASIAnew");
      await sk(page).fill("sk-new");
      await st(page).fill("FQoG-tok");
      const saved = await press(page, posts, w.add);
      assert.equal(saved.body.new, true);
      assert.equal(saved.body.preset, "bedrock");
      assert.equal(saved.body.key || "", "");
      assert.equal(saved.body.accessKeyID, "ASIAnew");
      assert.equal(saved.body.secretAccessKey, "sk-new");
      assert.equal(saved.body.sessionToken, "FQoG-tok");
      assert.deepEqual(errors, []);
    });
  }
}
