// Run with Node's test runner and Playwright on the module path; see README.md.
// An agent's own sign-in (kiro-cli's here) can be removed in magpie, even
// while it is first: Remove says magpie only hides it, its files left as
// they are, and posts login/forget; one of magpie's in use first still has
// no Remove. In English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const kiro = (logins) => ({
  id: "kiro", name: "Kiro", icon: "kiro-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "auto", name: "Auto", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "kiro", agentName: "Kiro", user: logins[0].user, plan: "KIRO PRO", logins },
});

function serve(lang, logins, posts) {
  const providers = { providers: [kiro(logins)], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/login/") && route.request().method() === "POST") {
      posts.push({ path: url.pathname, body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const title = {
  en: "magpie stops showing and using Kiro's own sign-in; its files are left as they are, and it shows again when Kiro signs in anew",
  zh: "不再显示和使用 Kiro 自己的登录；文件保持原样，Kiro 重新登录后再次出现",
};
const remove = { en: "Remove", zh: "移除" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const open = async (t, logins, posts) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, logins, posts));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Kiro" }).click();
      await page.locator(".accts .acc").first().waitFor();
      return { page, errors };
    };

    test(`${engine} ${lang}: kiro-cli's own sign-in behind another can be removed, hidden`, async (t) => {
      const posts = [];
      const { page, errors } = await open(t, [
        { user: "me@example.com", plan: "KIRO PRO", active: true, on: true },
        { user: "Kiro account", plan: "KIRO FREE", on: true, own: true },
      ], posts);
      const mine = page.locator(".accts .acc", { hasText: "me@example.com" });
      assert.equal(await mine.getByRole("button", { name: remove[lang], exact: true }).count(), 0, "the first of magpie's has no Remove");
      const own = page.locator(".accts .acc", { hasText: "Kiro account" }).getByRole("button", { name: remove[lang], exact: true });
      assert.equal(await own.getAttribute("title"), title[lang]);
      await own.click();
      assert.deepEqual(posts, [], "Remove waits for confirmation");
      await page.locator("dialog.action-confirm[open] button").last().click();
      for (let i = 0; i < 50 && !posts.length; i++) await page.waitForTimeout(50);
      assert.deepEqual(posts, [{ path: "/api/login/forget", body: { agent: "kiro", user: "Kiro account" } }]);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: kiro-cli's own sign-in in use first can be removed too`, async (t) => {
      const posts = [];
      const { page, errors } = await open(t, [{ user: "Kiro account", plan: "KIRO FREE", active: true, on: true, own: true }], posts);
      const own = page.locator(".accts .acc", { hasText: "Kiro account" }).getByRole("button", { name: remove[lang], exact: true });
      assert.equal(await own.getAttribute("title"), title[lang]);
      await own.click();
      assert.deepEqual(posts, [], "Remove waits for confirmation");
      await page.locator("dialog.action-confirm[open] button").last().click();
      for (let i = 0; i < 50 && !posts.length; i++) await page.waitForTimeout(50);
      assert.deepEqual(posts, [{ path: "/api/login/forget", body: { agent: "kiro", user: "Kiro account" } }]);
      assert.deepEqual(errors, []);
    });
  }
}
