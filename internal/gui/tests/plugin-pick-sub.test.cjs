// Run with Node's test runner and Playwright on the module path; see README.md.
// A plugin that signs in to several subscriptions, none signed in yet
// (Qoder's: qoder.com, Qoder CN, QwenWork and its enterprise flagship), as
// the latest release lists it once installed: the retired built-ins Qoder
// and Qoder CN are the plugin's. 小武 on Discord: "Qoder 只支持国际版 请添加
// Qoder中国版" — the Plugins page's Sign in took the first, qoder.com, and
// Qoder CN went unseen. Now the Discover card's and the Installed row's
// Sign in each ask which by name, and picking Qoder CN signs in to it; the
// Add sheet calls it Qoder 中国版 in Chinese and finds it by 中国版, and
// before the plugin is installed a search for 中国版 points to Plugins.
// Every language, Chromium and WebKit, wide and narrow; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const QODER = "@magpie-community/opencode-qoder-auth";
const listings = [{ package: QODER, name: "Qoder", icon: "qoder", providers: ["qoder", "qoder-cn", "qwenwork", "qwenwork-enterprise"], community: true, replaces: "qoder", summary: { en: "Qoder.", zh: "Qoder。" }, npm: { version: "0.2.52", weekly: 10 } }];
// the plugin's own providers and sign-in labels (packages/qoder/index.mjs)
const subs = [
  { id: "qoder", pid: "qoder", name: "Qoder", icon: "qoder", spec: QODER, signedIn: false, models: 2, methods: [{ type: "oauth", label: "Sign in with Qoder" }, { type: "oauth", label: "Sign in with Qoder PAT" }] },
  { id: "qoder-cn", pid: "qoder-cn", name: "Qoder CN", icon: "qoder", spec: QODER, signedIn: false, models: 2, methods: [{ type: "oauth", label: "Sign in with Qoder CN" }, { type: "oauth", label: "Sign in with Qoder CN PAT" }, { type: "oauth", label: "Sign in with Qoder CN Enterprise (VPC)" }] },
  { id: "qwenwork", pid: "qwenwork", name: "QwenWork", icon: "qoder", spec: QODER, signedIn: false, models: 2, methods: [{ type: "oauth", label: "Sign in with QwenWork" }] },
  { id: "qwenwork-enterprise", pid: "qwenwork-enterprise", name: "QwenWork 企业版", icon: "qoder", spec: QODER, signedIn: false, models: 2, methods: [{ type: "oauth", label: "Sign in with QwenWork 企业版（输入企业域名）" }] },
];

function server(lang, asked, installed) {
  const state = { bun: true, bunVersion: "1.3.0", plugins: installed ? [{ spec: QODER + "@0.2.52", providers: subs.map((x) => x.name), version: "0.2.52" }] : [] };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") {
      return json({ providers: [{ id: "openai", name: "OpenAI", icon: "openai", preset: "openai", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }],
        presets: [], excluded: [], gateway: { running: true, window: true },
        movable: ["qoder", "qoder-cn"], movesTo: { qoder: QODER, "qoder-cn": QODER },
        onPlugins: installed ? ["qoder", "qoder-cn"] : undefined, plugins: installed ? subs : [] });
    }
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugins/market" || url.pathname === "/api/plugins" || url.pathname === "/api/plugins/listings") return json(url.pathname === "/api/plugins" ? state : url.pathname === "/api/plugins/listings" ? { listings } : { listings, state });
    if (url.pathname === "/api/plugin-signin/prompt") { asked.push(route.request().postDataJSON()); return json({ prompt: null, inputs: {} }); }
    if (url.pathname === "/api/plugin-signin") { asked.push(route.request().postDataJSON()); return json({ id: "s1", agent: "qoder-cn", state: "waiting", url: "https://fake.test/device" }); }
    if (url.pathname === "/api/signin/s1") return json({ id: "s1", agent: "qoder-cn", state: "waiting", url: "https://fake.test/device" });
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { signIn: "Sign in", installed: "Installed", head: "Sign in to which subscription", intl: "Qoder (international)", cn: "Qoder CN", tile: "Qoder CN", find: "qoder cn", anyway: "Sign in anyway" },
  zh: { signIn: "登录", installed: "已安装", head: "登录哪个订阅", intl: "Qoder 国际版 (qoder.com)", cn: "Qoder 中国版 (qoder.cn)", tile: "Qoder 中国版", find: "中国版", anyway: "仍然登录" },
  "zh-TW": { signIn: "登入", installed: "已安裝", head: "登入哪個訂閱", intl: "Qoder 國際版 (qoder.com)", cn: "Qoder 中國版 (qoder.cn)", tile: "Qoder 中國版", find: "中國版" },
  ja: { signIn: "サインイン", installed: "インストール済み", head: "サインインするサブスクリプション", intl: "Qoder（海外版）", cn: "Qoder 中国版", tile: "Qoder 中国版", find: "中国版" },
  de: { signIn: "Anmelden", installed: "Installiert", head: "Bei welchem Abo anmelden", intl: "Qoder (international)", cn: "Qoder CN", tile: "Qoder CN", find: "qoder cn" },
};

async function open(browser, lang, width, installed, view) {
  const page = await (await browser.newContext({ viewport: { width, height: 820 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], asked = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", server(lang, asked, installed));
  await page.goto("http://magpie.test/?view=" + view);
  return { page, errors, asked };
}

// the menu a Sign in opened: its heading and its items' names
async function menuOf(page) {
  const m = page.locator(".proto-menu");
  await m.waitFor();
  return { head: await m.locator(".pm-head").textContent(), names: await m.locator(".pm-item .pm-name").allTextContents(), m };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a plugin of several subscriptions asks which to sign in to", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) {
      for (const width of [980, 420]) {
        await t.test(`${lang} ${width}px`, async () => {
          const w = L[lang];
          const want = [w.intl, w.cn, "QwenWork", "QwenWork 企业版"];

          // Discover: the card's Sign in asks which, and Qoder CN signs in to qoder-cn
          let { page, errors, asked } = await open(browser, lang, width, true, "plugins");
          const card = page.locator(`#view-plugins .pm-card[data-pkg="${QODER}"] .pm-act`);
          await card.waitFor();
          assert.equal((await card.innerText()).trim(), w.signIn);
          await card.click();
          let menu = await menuOf(page);
          assert.equal(menu.head, w.head);
          assert.deepEqual(menu.names, want);
          const box = await menu.m.boundingBox();
          assert.ok(box.x >= 0 && box.x + box.width <= width, `the menu fits the window: ${JSON.stringify(box)}`);
          assert.deepEqual(asked, [], "nothing asked before one is picked");
          await menu.m.locator(".pm-item", { hasText: w.cn }).click();
          const signing = page.locator("#addSheet .signing").first();
          await signing.waitFor();
          assert.ok((await signing.innerText()).includes(w.cn), "the sign-in is Qoder CN's");
          if (w.anyway) {
            await signing.locator("button", { hasText: w.anyway }).click();
            // the plugin is asked for Qoder CN, not qoder.com
            for (let i = 0; i < 100 && !asked.length; i++) await page.waitForTimeout(50);
            assert.ok(asked.length && asked.every((x) => x.provider === "qoder-cn"), JSON.stringify(asked));
          }
          if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `plugin-pick-sub-${engine}-${lang}-${width}.png`) });
          assert.deepEqual(errors, []);
          await page.context().close();

          // Installed: the row's Sign in asks the same
          ({ page, errors, asked } = await open(browser, lang, width, true, "plugins"));
          await page.locator(":is(.lib-tabs .opt, .nav-s):visible", { hasText: w.installed }).first().click();
          const row = page.locator("#view-plugins .pm-row").filter({ hasText: w.cn });
          await row.waitFor();
          const b = row.locator(".val button", { hasText: w.signIn }).first();
          await b.click();
          menu = await menuOf(page);
          assert.deepEqual(menu.names, want);
          await menu.m.locator(".pm-item", { hasText: "QwenWork 企业版" }).click();
          await page.locator("#addSheet .signing").first().waitFor();
          for (let i = 0; i < 100 && !asked.length; i++) await page.waitForTimeout(50);
          assert.ok(asked.length && asked.every((x) => x.provider === "qwenwork-enterprise"), JSON.stringify(asked));
          assert.deepEqual(errors, []);
          await page.context().close();

          // the Add sheet names it and finds it by its name
          ({ page, errors } = await open(browser, lang, width, true, "providers"));
          await page.locator("#addProvider").click();
          await page.locator(`#addSheet .tile[data-pick="${w.tile}"]`).waitFor();
          await page.locator("#addSheet input").first().fill(w.find);
          await page.locator(`#addSheet .tile[data-pick="${w.tile}"]`).waitFor();
          assert.deepEqual(errors, []);
          await page.context().close();

          // not installed: the search points to Plugins
          ({ page, errors } = await open(browser, lang, width, false, "providers"));
          await page.locator("#addProvider").click();
          await page.locator("#addSheet .tile").first().waitFor();
          assert.equal(await page.locator(`#addSheet .tile[data-pick="${w.tile}"]`).count(), 0, "the plugin's to install");
          await page.locator("#addSheet input").first().fill(w.find);
          await page.locator('#addSheet .tile[data-pick="plugins"]').waitFor();
          assert.deepEqual(errors, []);
          await page.context().close();
        });
      }
    }
  });
}
