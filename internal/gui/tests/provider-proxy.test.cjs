// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider's own proxy (#237): its editor has a Proxy row, Global proxy |
// Direct | Custom, the address field there only for Custom; Save posts
// "" for the global one, "direct", or the address typed, and Custom with
// none typed is refused before anything is posted. A signed-in account
// (Codex here) has the row too, opening on what it has. A click on the
// row leaves the page where it is. In English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};
const codex = {
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS", logins: [{ user: "me@example.com", plan: "PLUS", active: true, on: true }] },
  proxy: "http://10.0.0.2:7890",
};

function serve(lang, posts) {
  const providers = { providers: [relay, codex], presets: [], excluded: [], gateway: { running: true, window: true } };
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
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { global: "Global proxy", direct: "Direct", custom: "Custom", save: "Save", follows: "Follows the proxy in Settings", none: "Proxy: type its address, like http://127.0.0.1:7890" },
  zh: { global: "全局代理", direct: "直连", custom: "自定义", save: "保存", follows: "跟随设置里的代理", none: "代理：请填写地址，例如 http://127.0.0.1:7890" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    const open = async (t, name) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${name}-proxy.png`) });
        }
        await browser.close();
      });
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posts = [];
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: name }).click();
      await page.locator(".editor .proxy-mode").waitFor();
      return { page, errors, posts };
    };
    const mode = (page) => page.locator(".editor .proxy-mode .opt.on").textContent();
    const addr = (page) => page.locator(".editor .proxy-url");
    // a click on one of the row's options, the page left where it was
    const pick = async (page, name) => {
      const b = page.locator(".editor .proxy-mode .opt", { hasText: name });
      await b.scrollIntoViewIfNeeded();
      await page.waitForTimeout(200);
      const before = await b.evaluate((e) => e.getBoundingClientRect().top);
      await b.click();
      await page.waitForTimeout(250);
      const after = await b.evaluate((e) => e.getBoundingClientRect().top);
      assert(Math.abs(after - before) <= 1, `${name} moved from ${before} to ${after}`);
    };
    const save = async (page, posts) => {
      const n = posts.length;
      await page.locator(".editor .bar").getByRole("button", { name: w.save, exact: true }).click();
      for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
      return posts.at(-1);
    };

    test(`${engine} ${lang}: a provider's proxy is picked and saved`, async (t) => {
      const { page, errors, posts } = await open(t, "Relay");
      assert.equal(await mode(page), w.global, "a provider with none follows the global one");
      assert(await addr(page).isHidden(), "no address field for the global one");
      assert.equal(await page.locator(".editor .proxy-pick .hint").textContent(), w.follows);

      // Custom with nothing typed is refused, nothing posted
      await pick(page, w.custom);
      assert(await addr(page).isVisible());
      await page.locator(".editor .bar").getByRole("button", { name: w.save, exact: true }).click();
      await page.locator(".editor .editor-error").waitFor();
      assert.equal(await page.locator(".editor .editor-error").textContent(), w.none);
      assert.equal(posts.length, 0);

      await addr(page).fill(" socks5://127.0.0.1:1080 ");
      let saved = await save(page, posts);
      assert.equal(saved.action, "save");
      assert.equal(saved.body.id, "relay");
      assert.equal(saved.body.proxy, "socks5://127.0.0.1:1080");

      // Direct, then the global one again
      await page.locator(".row.provider", { hasText: "Relay" }).click();
      await pick(page, w.direct);
      assert(await addr(page).isHidden());
      saved = await save(page, posts);
      assert.equal(saved.body.proxy, "direct");
      await page.locator(".row.provider", { hasText: "Relay" }).click();
      await pick(page, w.direct);
      await pick(page, w.global);
      saved = await save(page, posts);
      assert.equal(saved.body.proxy, "");

      const missing = await page.evaluate(() => [
        "Proxy", "Global proxy", "Direct", "Custom", "Follows the proxy in Settings",
        "Requests to it go direct, whatever the proxy in Settings",
        "Requests to it go through this proxy: http://, https:// or socks5://",
        "Proxy: type its address, like http://127.0.0.1:7890",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a signed-in account's proxy opens as it is and is saved`, async (t) => {
      const { page, errors, posts } = await open(t, "Codex");
      assert.equal(await mode(page), w.custom, "Codex's own proxy is shown");
      assert.equal(await addr(page).inputValue(), "http://10.0.0.2:7890");
      assert.equal(await page.locator(".editor .proxy-pick .opt.on").count(), 1);
      await pick(page, w.direct);
      const saved = await save(page, posts);
      assert.equal(saved.action, "save");
      assert.equal(saved.body.id, "codex");
      assert.equal(saved.body.proxy, "direct");
      assert.deepEqual(saved.body.models, ["gpt-6"], "its picks go with it");
      const border = await page.evaluate(() => [...document.querySelectorAll(".proxy-pick, .proxy-pick *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no border stripes");
      assert.deepEqual(errors, []);
    });
  }
}
