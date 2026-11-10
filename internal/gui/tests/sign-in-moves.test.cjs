// Run with Node's test runner and Playwright on the module path; see README.md.
// The owner (11-11): Claude Code stopped switching to an account with room,
// and nothing on the page said why: "Keep Claude Code signed in to" was
// ticked. Under the accounts a line now says what magpie does with Claude
// Code's own sign-in, as the server tells it (signInMoves): moved on at 98%
// or once used up, back to the first or not; off while kept on one; left
// alone while its requests go through the gateway. Ticking the keep box
// changes the line. No line with one account. English and Chinese,
// Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const claude = ({ signInMoves = { mode: "moves", share: 98 }, keepLogin = false, keepLoginAs = "", spare = true, one = false } = {}) => ({
  id: "claude", name: "Claude Code", icon: "claude-color", chat: "", responses: "", anthropic: "", catalog: "", routing: "", keepLogin, keepLoginAs, signInMoves,
  models: [{ id: "claude-opus-5-5", name: "Claude Opus 5.5", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: {
    agent: "claude", agentName: "Claude Code", user: "a@example.com", plan: "MAX",
    logins: [{ user: "a@example.com", plan: "MAX", active: true, on: true }, ...(one ? [] : [{ user: "b@example.com", plan: "MAX", on: spare }])],
  },
});

function serve(lang, first, posts) {
  let list = [first];
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const providers = () => ({ providers: list, presets: [], excluded: [], gateway: { running: true, window: true } });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers());
    if (url.pathname === "/api/provider/keeplogin") {
      const body = route.request().postDataJSON();
      posts.push(body);
      const as = body.keepLogin && body.keepLoginAs || "";
      const logins = list[0].account.logins.map((l) => ({ ...l, active: as ? l.user === as : l.user === "a@example.com" }));
      // the server says what the switch now does (provider.SignInMovesOf)
      const signInMoves = body.keepLogin ? { mode: "kept" } : { mode: "moves", share: 98 };
      list = [{ ...list[0], keepLogin: body.keepLogin, keepLoginAs: as, signInMoves, account: { ...list[0].account, logins } }];
      return json(providers());
    }
    if (url.pathname === "/api/provider/arrange") {
      posts.push({ arrange: route.request().postDataJSON() });
      return json(providers());
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: {
    at98: "When the account Claude Code is signed in to reaches 98%, magpie signs it in to the next ticked account with room. It stays on that one till it runs low too.",
    usedUpBack: "When the account Claude Code is signed in to is used up, magpie signs it in to the next ticked account with room. It goes back to the first once that has room again.",
    kept: "Automatic switching is off: Claude Code stays signed in to the first account, even once it runs out.",
    keptAs: "Automatic switching is off: Claude Code stays signed in to b@example.com, even once it runs out.",
    gateway: "Claude Code's requests go through magpie, which sends each one to an account with room; its own sign-in stays where you put it.",
  },
  zh: {
    at98: "Claude Code 登录的账号用到 98% 时，magpie 把它登录到下一个已勾选且有额度的账号。之后一直用这个账号，直到它的额度也快用完。",
    usedUpBack: "Claude Code 登录的账号用完时，magpie 把它登录到下一个已勾选且有额度的账号。首选账号恢复额度后再切回去。",
    kept: "自动切换已关闭：Claude Code 一直登录首选账号，额度用完也不换。",
    keptAs: "自动切换已关闭：Claude Code 一直登录 b@example.com，额度用完也不换。",
    gateway: "Claude Code 的请求经过 magpie，每个请求都发给有额度的账号；Claude Code 自己的登录保持你选的账号不变。",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    const open = async (t, opts) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, claude(opts), posts));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Claude Code" }).first().click();
      await page.locator(".editor .acc").first().waitFor();
      return { page, errors, posts };
    };
    const line = (page) => page.locator(".editor .accts .sign-in-moves");

    test(`${engine} ${lang}: the line says how Claude Code's sign-in moves`, async (t) => {
      for (const [opts, want] of [
        [{}, w.at98],
        [{ signInMoves: { mode: "moves", share: 100, back: true } }, w.usedUpBack],
        [{ signInMoves: { mode: "gateway" } }, w.gateway],
        [{ signInMoves: { mode: "kept" }, keepLogin: true, keepLoginAs: "b@example.com" }, w.keptAs],
      ]) {
        const { page, errors } = await open(t, opts);
        assert.equal((await line(page).textContent()).trim(), want);
        assert.equal(await line(page).count(), 1);
        assert.deepEqual(errors, []);
        await page.close();
      }
    });

    test(`${engine} ${lang}: ticking keep changes the line, and none with one account or off`, async (t) => {
      const { page, errors, posts } = await open(t, {});
      const top = await page.evaluate(() => document.scrollingElement.scrollTop);
      await page.locator(".editor .accts .keep-login input").click();
      await page.waitForFunction((s) => document.querySelector(".editor .accts .sign-in-moves")?.textContent.trim() === s, w.kept);
      assert.deepEqual(posts, [{ id: "claude", keepLogin: true }]);
      await page.locator(".editor .accts .keep-login input").click();
      await page.waitForFunction((s) => document.querySelector(".editor .accts .sign-in-moves")?.textContent.trim() === s, w.at98);
      assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), top, "the page doesn't move");
      assert.deepEqual(errors, []);
      for (const opts of [{ one: true }, { signInMoves: { mode: "off" } }]) {
        const o = await open(t, opts);
        assert.equal(await line(o.page).count(), 0, JSON.stringify(opts));
        assert.deepEqual(o.errors, []);
      }
    });
  }
}
