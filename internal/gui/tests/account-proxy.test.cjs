// Run with Node's test runner and Playwright on the module path; see README.md.
// Each account of a subscription with several has a proxy of its own
// (gakki: 是否可以为不同的codex账号设置不同的代理): Codex's editor has, under
// its Proxy row, a line per account — Provider's proxy | Direct | Custom —
// opening on what each has; Save posts accountProxies with each account
// that doesn't follow Codex's, and Custom with no address is refused
// before anything is posted. A pick moves nothing and the page stays where
// it is. A subscription with one account has no such lines. A provider's
// keys have the same lines, a key by its fingerprint (Beyfish_Wang on X:
// 不同 key 走不同的代理), and one with one key none. In English and
// Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const codex = {
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: {
    agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS",
    logins: [
      { user: "spare@example.com", plan: "PLUS", on: true },
      { user: "me@example.com", plan: "PLUS", active: true, on: true },
      { user: "Third@Example.com", plan: "PRO", on: true },
    ],
  },
  proxy: "http://10.0.0.2:7890",
  accountProxies: { "me@example.com": "socks5://10.0.0.5:1080", "spare@example.com": "direct" },
};
const claude = {
  id: "claude", name: "Claude", icon: "claude-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "claude-opus-5", name: "Claude Opus 5", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "claude", agentName: "Claude Code", user: "solo@example.com", plan: "MAX", logins: [{ user: "solo@example.com", plan: "MAX", active: true, on: true }] },
  proxy: "",
};
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "big", name: "", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, balanceToken: { takes: false, set: false }, proxy: "http://10.0.0.2:7890",
  keyList: [{ id: "aaaaaaaaaa", name: "Team", masked: "sk-…one", on: true, active: true }, { id: "bbbbbbbbbb", name: "", masked: "sk-…two", on: true }, { id: "cccccccccc", name: "Spare", masked: "sk-…three", on: true }],
  accountProxies: { aaaaaaaaaa: "socks5://10.0.0.5:1080", bbbbbbbbbb: "direct" },
};
const solo = {
  id: "lone", name: "Lone", icon: "generic", chat: "https://lone.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "big", name: "", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…solo" }, balanceToken: { takes: false, set: false }, proxy: "",
  keyList: [{ id: "5050505050", name: "", masked: "sk-…solo", on: true, active: true }],
};

function serve(lang, posts) {
  const providers = { providers: [codex, claude, relay, solo], presets: [], excluded: [], gateway: { running: true, window: true } };
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
  en: { prov: "Provider's proxy", direct: "Direct", custom: "Custom", save: "Save", missing: "Proxy of Third@Example.com: type its address, like http://127.0.0.1:7890" },
  zh: { prov: "供应商代理", direct: "直连", custom: "自定义", save: "保存", missing: "Third@Example.com 的代理：请填写地址，例如 http://127.0.0.1:7890" },
};
const keyWords = {
  en: { missing: "Proxy of Spare: type its address, like http://127.0.0.1:7890", hint: "Each key can go through a proxy of its own; Provider's proxy is the one above" },
  zh: { missing: "Spare 的代理：请填写地址，例如 http://127.0.0.1:7890", hint: "每个 Key 可以走自己的代理；“供应商代理”即上面这一项" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    const open = async (t, name, wait = ".editor .proxy-mode") => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${name}-account-proxy.png`) });
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
      await page.locator(".row.provider", { hasText: name }).first().click();
      await page.locator(wait).first().waitFor();
      return { page, errors, posts };
    };
    const line = (page, user) => page.locator(`.editor .acct-proxy[data-${/^[a-f0-9]{10}$/.test(user) ? "key" : "user"}="${user}"]`);
    const mode = (page, user) => line(page, user).locator(".proxy-mode .opt.on").textContent();
    const addr = (page, user) => line(page, user).locator(".proxy-url");
    // what the page and the dialog's scrollers have scrolled
    const scrolled = (page) => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop > 0).map((e) => `${e.className}:${e.scrollTop}`)].join(" "));
    // a click on one of an account's options: it, the lines and the Save
    // button left where they were, nothing scrolled
    const pick = async (page, user, name) => {
      const b = line(page, user).locator(".proxy-mode .opt", { hasText: name });
      await b.scrollIntoViewIfNeeded();
      await page.waitForTimeout(200);
      const where = () => page.evaluate(() => [...document.querySelectorAll(".editor .acct-proxy, .editor .bar")].map((e) => Math.round(e.getBoundingClientRect().top)));
      const before = await b.evaluate((e) => e.getBoundingClientRect().top), lines = await where(), sc = await scrolled(page);
      await b.click();
      await page.waitForTimeout(250);
      const after = await b.evaluate((e) => e.getBoundingClientRect().top);
      assert(Math.abs(after - before) <= 1, `${user} ${name} moved from ${before} to ${after}`);
      assert.deepEqual(await where(), lines, `a pick of ${name} moved the lines`);
      assert.equal(await scrolled(page), sc, `a pick of ${name} scrolled`);
    };
    const save = async (page, posts) => {
      const n = posts.length;
      await page.locator(".editor .bar").getByRole("button", { name: w.save, exact: true }).click();
      for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
      return posts.at(-1);
    };

    test(`${engine} ${lang}: each Codex account's proxy opens as it is and is saved`, async (t) => {
      const { page, errors, posts } = await open(t, "Codex", ".editor .acct-proxy");
      // the account in use first, then the others as they are listed
      assert.deepEqual(await page.locator(".editor .acct-proxy").evaluateAll((ls) => ls.map((l) => l.dataset.user)), ["me@example.com", "spare@example.com", "Third@Example.com"]);
      assert.equal(await mode(page, "me@example.com"), w.custom);
      assert.equal(await addr(page, "me@example.com").inputValue(), "socks5://10.0.0.5:1080");
      assert(await addr(page, "me@example.com").isVisible());
      assert.equal(await mode(page, "spare@example.com"), w.direct);
      assert(await addr(page, "spare@example.com").isHidden());
      assert.equal(await mode(page, "Third@Example.com"), w.prov, "an account with none follows Codex's");
      assert(await addr(page, "Third@Example.com").isHidden());
      assert.equal(await page.locator(".editor .proxy-pick .proxy-url").inputValue(), "http://10.0.0.2:7890", "Codex's own is kept above");

      // saved untouched, it posts what it has
      let saved = await save(page, posts);
      assert.equal(saved.action, "save");
      assert.equal(saved.body.id, "codex");
      assert.equal(saved.body.proxy, "http://10.0.0.2:7890");
      assert.deepEqual(saved.body.accountProxies, { "me@example.com": "socks5://10.0.0.5:1080", "spare@example.com": "direct" });

      // Custom for the third with nothing typed is refused, nothing posted
      await page.locator(".row.provider", { hasText: "Codex" }).first().click();
      await line(page, "Third@Example.com").waitFor();
      await pick(page, "Third@Example.com", w.custom);
      assert(await addr(page, "Third@Example.com").isVisible());
      const n = posts.length;
      await page.locator(".editor .bar").getByRole("button", { name: w.save, exact: true }).click();
      await page.locator(".editor .editor-error").waitFor();
      assert.equal(await page.locator(".editor .editor-error").textContent(), w.missing);
      assert.equal(posts.length, n);
      assert(await addr(page, "Third@Example.com").evaluate((e) => e === document.activeElement), "its address is focused");

      // typed, the spare back to Codex's, the first direct
      await addr(page, "Third@Example.com").fill(" http://10.0.0.7:3128 ");
      await pick(page, "spare@example.com", w.prov);
      await pick(page, "me@example.com", w.direct);
      assert(await addr(page, "me@example.com").isHidden());
      saved = await save(page, posts);
      assert.deepEqual(saved.body.accountProxies, { "me@example.com": "direct", "third@example.com": "http://10.0.0.7:3128" });
      assert.deepEqual(saved.body.models, ["gpt-6"], "its picks go with it");

      const border = await page.evaluate(() => [...document.querySelectorAll(".acct-proxies, .acct-proxies *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no border stripes");
      const missing = await page.evaluate(() => [
        "Provider's proxy",
        "Each account can go through a proxy of its own; Provider's proxy is the one above",
        "Proxy of {user}: type its address, like http://127.0.0.1:7890",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a subscription with one account has no per-account lines`, async (t) => {
      const { page, errors, posts } = await open(t, "Claude");
      assert.equal(await page.locator(".editor .acct-proxies").count(), 0);
      const saved = await save(page, posts);
      assert.equal(saved.body.id, "claude");
      assert.deepEqual(saved.body.accountProxies, {});
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: each key's proxy opens as it is and is saved`, async (t) => {
      const { page, errors, posts } = await open(t, "Relay", ".editor .acct-proxy");
      const kw = keyWords[lang];
      // the keys as the Accounts list has them, named by their names, or
      // masked when they have none
      assert.deepEqual(await page.locator(".editor .acct-proxy").evaluateAll((ls) => ls.map((l) => [l.dataset.key, l.querySelector(".who").textContent])),
        [["aaaaaaaaaa", "Team"], ["bbbbbbbbbb", "sk-…two"], ["cccccccccc", "Spare"]]);
      assert.equal(await page.locator(".editor .acct-proxies .hint").textContent(), kw.hint);
      assert.equal(await mode(page, "aaaaaaaaaa"), w.custom);
      assert.equal(await addr(page, "aaaaaaaaaa").inputValue(), "socks5://10.0.0.5:1080");
      assert.equal(await mode(page, "bbbbbbbbbb"), w.direct);
      assert.equal(await mode(page, "cccccccccc"), w.prov, "a key with none follows the provider's");
      assert.equal(await page.locator(".editor .proxy-pick .proxy-url").inputValue(), "http://10.0.0.2:7890", "the provider's own is kept above");

      let saved = await save(page, posts);
      assert.equal(saved.body.id, "relay");
      assert.equal(saved.body.proxy, "http://10.0.0.2:7890");
      assert.deepEqual(saved.body.accountProxies, { aaaaaaaaaa: "socks5://10.0.0.5:1080", bbbbbbbbbb: "direct" });

      // Custom for Spare with nothing typed is refused by its name
      await page.locator(".row.provider", { hasText: "Relay" }).first().click();
      await line(page, "cccccccccc").waitFor();
      await pick(page, "cccccccccc", w.custom);
      const n = posts.length;
      await page.locator(".editor .bar").getByRole("button", { name: w.save, exact: true }).click();
      await page.locator(".editor .editor-error").waitFor();
      assert.equal(await page.locator(".editor .editor-error").textContent(), kw.missing);
      assert.equal(posts.length, n);
      assert(await addr(page, "cccccccccc").evaluate((e) => e === document.activeElement), "its address is focused");

      await addr(page, "cccccccccc").fill(" http://10.0.0.7:3128 ");
      await pick(page, "bbbbbbbbbb", w.prov);
      await pick(page, "aaaaaaaaaa", w.direct);
      saved = await save(page, posts);
      assert.deepEqual(saved.body.accountProxies, { aaaaaaaaaa: "direct", cccccccccc: "http://10.0.0.7:3128" });
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a provider with one key has no per-key lines`, async (t) => {
      const { page, errors, posts } = await open(t, "Lone");
      assert.equal(await page.locator(".editor .acct-proxies").count(), 0);
      const saved = await save(page, posts);
      assert.equal(saved.body.id, "lone");
      assert.equal(saved.body.accountProxies, undefined, "nothing said: the key's own are kept");
      assert.deepEqual(errors, []);
    });
  }
}
