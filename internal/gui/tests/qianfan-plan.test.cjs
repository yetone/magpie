// Run with Node's test runner and Playwright on the module path; see README.md.
// A Baidu Qianfan Token Plan Personal provider's editor takes a sign-in on
// Baidu Cloud's console the way StepFun's does (yuushabu_buyinn on X, #5860):
// the paste posts to /api/qianfan/{id}/session for that provider's id, and a
// signed-in one's Sign out posts /api/qianfan/{id}/signout. A Qianfan
// provider without the plan has no such field. In English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const qianfan = (id, name, plan) => ({
  id, name, icon: "baiducloud-color", preset: "baidu-qianfan", chat: "https://qianfan.baidubce.com/v2/tokenplan/personal", responses: "", anthropic: "", catalog: "",
  models: [{ id: "ernie-5.0", name: "ERNIE 5.0", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "…ab12" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
  ...(plan ? { qianfanPlan: { site: "", signedIn: plan === "in", url: "https://console.bce.baidu.com/qianfan/resource/token-plan", bookmarklet: "javascript:void 0" } } : {}),
});

function serve(lang, posts) {
  const providers = { providers: [qianfan("qianfan", "Qianfan Plan", "in"), qianfan("qianfan-2", "Qianfan Work", "out"), qianfan("qianfan-api", "Qianfan API", "")], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/qianfan/")) {
      posts.push({ path: url.pathname, body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = { en: { out: "Sign out", cancel: "Cancel" }, zh: { out: "退出登录", cancel: "取消" } };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a Qianfan Token Plan provider signs in and out for its own id`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 560 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posts = [];
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      const wait = async (n) => { for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50); };

      // a Qianfan provider without the plan has no sign-in field
      await page.locator(".row.provider", { hasText: "Qianfan API" }).click();
      await page.locator(".editor").waitFor();
      assert.equal(await page.locator(".editor .stepplan").count(), 0);
      await page.locator(".editor .bar").getByRole("button", { name: words[lang].cancel, exact: true }).click();
      await page.locator(".editor").waitFor({ state: "detached" });

      // the one not signed in takes a paste, for its own id
      await page.locator(".row.provider", { hasText: "Qianfan Work" }).click();
      const paste = page.locator(".editor .stepplan input[type=password]");
      await paste.fill("magpie-qianfan:{}");
      await paste.press("Enter");
      await wait(0);
      assert.equal(posts.length, 1);
      assert.equal(posts[0].path, "/api/qianfan/qianfan-2/session");
      assert.equal(posts[0].body.text, "magpie-qianfan:{}");

      // the one signed in signs out, for its own id
      await page.locator(".editor .bar").getByRole("button", { name: words[lang].cancel, exact: true }).click();
      await page.locator(".editor").waitFor({ state: "detached" });
      await page.locator(".row.provider", { hasText: "Qianfan Plan" }).click();
      await page.locator(".editor .stepplan").getByRole("button", { name: words[lang].out, exact: true }).click();
      await wait(1);
      assert.equal(posts.length, 2);
      assert.equal(posts[1].path, "/api/qianfan/qianfan/signout");
      assert.deepEqual(errors, []);
    });
  }
}
