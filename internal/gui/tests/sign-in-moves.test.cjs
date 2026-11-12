// Run with Node's test runner and Playwright on the module path; see README.md.
// The owner (11-11): Claude Code stopped switching to an account with room,
// and nothing on the page said why: "Keep Claude Code signed in to" was
// ticked. Moving the sign-in is now a switch of its own under the accounts
// ("这个轮换做成一个开关"). On, a line under it says what magpie does, as
// the server tells it (signInMoves): moved on at 98% or once used up, back
// to the first or not, or left alone while its requests go through the
// gateway. Off, the account it stays on. Switch-only holds it on. Nothing
// with one account. Beside the switch, a mark whose hover card says how
// the switch works and why it is Claude Code's own use of the account
// ("提示这个原理是啥，以及它为什么不会让你的账号被封禁"), only where the
// sign-in moves. English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const claude = ({ signInMoves = { mode: "moves", share: 98 }, keepLogin = false, keepLoginAs = "", switchOnly = false, spare = true, one = false } = {}) => ({
  id: "claude", name: "Claude Code", icon: "claude-color", chat: "", responses: "", anthropic: "", catalog: "", routing: "", keepLogin, keepLoginAs, switchOnly, signInMoves,
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
    name: "Switch Claude Code's account automatically",
    keep: "Keep Claude Code signed in to",
    gateway: "Claude Code's requests go through magpie, which sends each one to an account with room; its own sign-in stays where you put it.",
    tip: [
      "When the account runs low, magpie signs Claude Code in to your next account, as /login does. Sessions already open keep their account until they are restarted.",
      "Requests still go from Claude Code straight to Anthropic, unchanged. Use only accounts that are yours: sharing one is against Anthropic's terms.",
    ],
  },
  zh: {
    at98: "Claude Code 登录的账号用到 98% 时，magpie 把它登录到下一个已勾选且有额度的账号。之后一直用这个账号，直到它的额度也快用完。",
    usedUpBack: "Claude Code 登录的账号用完时，magpie 把它登录到下一个已勾选且有额度的账号。首选账号恢复额度后再切回去。",
    name: "自动切换 Claude Code 的账号",
    keep: "Claude Code 始终登录",
    gateway: "Claude Code 的请求经过 magpie，每个请求都发给有额度的账号；Claude Code 自己的登录保持你选的账号不变。",
    tip: [
      "账号额度快用完时，magpie 把 Claude Code 登录到你的下一个账号，和 /login 一样。已经打开的会话继续用原来的账号，直到重新启动。",
      "请求仍由 Claude Code 直接发往 Anthropic，原样不改。请只用你自己的账号：共享账号违反 Anthropic 的条款。",
    ],
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
    const line = (page) => page.locator(".editor .accts .auto-switch .sign-in-moves");
    const sw = (page) => page.locator(".editor .accts .auto-switch [role=switch]");
    const lineIs = (page, want) => page.waitForFunction((s) => document.querySelector(".editor .accts .auto-switch .sign-in-moves")?.textContent.trim() === s, want);

    test(`${engine} ${lang}: on, the line under the switch says how Claude Code's sign-in moves`, async (t) => {
      for (const [opts, want] of [
        [{}, w.at98],
        [{ signInMoves: { mode: "moves", share: 100, back: true } }, w.usedUpBack],
        [{ signInMoves: { mode: "gateway" } }, w.gateway],
      ]) {
        const { page, errors } = await open(t, opts);
        assert.equal((await page.locator(".editor .accts .auto-switch .as-head").textContent()).trim(), w.name);
        assert.equal(await sw(page).getAttribute("aria-checked"), "true");
        // Where the words start, not the span's box: an inherited text-align: right
        // put them at the switch while the box still began at the left.
        const textIn = await page.locator(".editor .accts .auto-switch").evaluate((box) => {
          const r = document.createRange(); r.selectNodeContents(box.querySelector(".as-name"));
          return r.getClientRects()[0].left - box.getBoundingClientRect().left;
        });
        assert.ok(textIn < 20, `the name's words start at the left: ${textIn}px in`);
        assert.equal((await line(page).textContent()).trim(), want);
        assert.equal(await line(page).count(), 1);
        assert.equal(await page.locator(".editor .accts .auto-switch .keep-as").count(), 0, "no account to stay on while it switches");
        assert.deepEqual(errors, []);
        await page.close();
      }
    });

    test(`${engine} ${lang}: off, it says the account Claude Code stays on`, async (t) => {
      const { page, errors } = await open(t, { signInMoves: { mode: "kept" }, keepLogin: true, keepLoginAs: "b@example.com" });
      assert.equal(await sw(page).getAttribute("aria-checked"), "false");
      assert.equal(await line(page).count(), 0);
      assert.equal((await page.locator(".editor .accts .auto-switch .as-keep span").first().textContent()).trim(), w.keep);
      assert.equal(await page.locator(".editor .accts .auto-switch .keep-as").getAttribute("data-value"), "b@example.com");
      assert.equal((await page.locator(".editor .accts .auto-switch .as-keep").textContent()).trim(), w.keep + "b@example.com");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: the switch turns it off and on, from the switch or its name`, async (t) => {
      const { page, errors, posts } = await open(t, {});
      const top = await page.evaluate(() => document.scrollingElement.scrollTop);
      await sw(page).click();
      await page.waitForFunction(() => document.querySelector(".editor .accts .auto-switch [role=switch]")?.getAttribute("aria-checked") === "false");
      assert.deepEqual(posts, [{ id: "claude", keepLogin: true }]);
      assert.equal(await line(page).count(), 0);
      assert.equal((await page.locator(".editor .accts .auto-switch .keep-as").textContent()).trim().length > 0, true);
      await page.locator(".editor .accts .auto-switch .as-head .as-name").click();
      await lineIs(page, w.at98);
      assert.equal(await sw(page).getAttribute("aria-checked"), "true");
      assert.deepEqual(posts[1], { id: "claude", keepLogin: false });
      assert.equal(posts.length, 2, "one post a click");
      assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), top, "the page doesn't move");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: the mark beside the switch shows how it works on hover, and only where the sign-in moves`, async (t) => {
      for (const [opts, shown] of [
        [{}, true],
        [{ signInMoves: { mode: "kept" }, keepLogin: true }, true],
        [{ signInMoves: { mode: "kept", routed: true }, keepLogin: true }, false],
        [{ signInMoves: { mode: "gateway" } }, false],
      ]) {
        const { page, errors, posts } = await open(t, opts);
        const mark = page.locator(".editor .accts .auto-switch .as-head .as-tip");
        if (!shown) {
          // the module is in by the time a shown one's mark is drawn
          await page.waitForFunction(() => !!window.autoSwitchTip);
          assert.equal(await mark.count(), 0, JSON.stringify(opts));
          assert.equal(await page.locator(".editor .accts .auto-switch > .hint").count(), 0);
          assert.deepEqual(errors, []);
          await page.close();
          continue;
        }
        await mark.waitFor();
        // beside the switch, on its line
        const [m, s] = await Promise.all([mark.boundingBox(), sw(page).boundingBox()]);
        assert.ok(s.x - (m.x + m.width) >= 0 && s.x - (m.x + m.width) < 16, `the mark sits beside the switch: ${JSON.stringify([m, s])}`);
        assert.ok(Math.abs(m.y + m.height / 2 - (s.y + s.height / 2)) < 3, "on the switch's line");
        // its lines rest out of sight, read by a screen reader
        const lines = page.locator(".editor .accts .auto-switch > .hint");
        assert.deepEqual((await lines.allTextContents()).map((x) => x.trim()), w.tip);
        assert.ok((await lines.first().boundingBox()).width <= 1, "out of sight till asked");
        await mark.hover();
        const card = page.locator(".set-hint.on");
        await card.waitFor();
        assert.deepEqual((await card.locator("p").allTextContents()).map((x) => x.trim()), w.tip);
        const c = await card.boundingBox(), box = await page.locator(".editor .accts .auto-switch").boundingBox();
        assert.ok(c.y >= box.y + box.height - 1 || c.y + c.height <= box.y + 1, "the card is beside the row, not over it");
        // a press on the mark is no press on the switch
        await mark.click();
        assert.deepEqual(posts, []);
        assert.equal(await sw(page).getAttribute("aria-checked"), opts.keepLogin ? "false" : "true");
        assert.deepEqual(errors, []);
        await page.close();
      }
    });

    test(`${engine} ${lang}: switch-only holds it on; nothing with one account, no line when off in magpie`, async (t) => {
      const only = await open(t, { switchOnly: true, keepLogin: true });
      assert.equal(await sw(only.page).getAttribute("aria-checked"), "true");
      assert.equal(await sw(only.page).isDisabled(), true);
      assert.ok(await sw(only.page).getAttribute("title"), "it says why");
      await lineIs(only.page, w.at98);
      assert.deepEqual(only.errors, []);
      const one = await open(t, { one: true });
      assert.equal(await one.page.locator(".editor .accts .auto-switch").count(), 0);
      assert.deepEqual(one.errors, []);
      const off = await open(t, { signInMoves: { mode: "off" } });
      assert.equal(await line(off.page).count(), 0);
      assert.deepEqual(off.errors, []);
    });
  }
}
