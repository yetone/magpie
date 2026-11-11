// Run with Node's test runner and Playwright on the module path; see README.md.
// #524: under Codex's accounts, with more than one on, turning off the
// automatic switch (11-11, it was a "Keep Codex signed in to" tick) keeps
// Codex signed in to the first account; it posts provider/keeplogin, and
// the Routing note then says Codex stays on the first. #530: In order, the
// note says magpie moves Codex on once the account is used up, not at 98%.
// With one account there is nothing to switch, so no switch. With it off, a
// pick keeps Codex signed in to an account of the user's choosing
// (keepLoginAs) while the first stays first: that row says Signed in, the
// first says First, and Make first on it arranges the order rather than
// signing Codex in. The page doesn't move. #1907: while magpie only
// switches Codex's sign-in, it always moves it: the switch is held on, and
// the notes say magpie moves it. In English and Chinese, Chromium and
// WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const codex = ({ keepLogin = false, keepLoginAs = "", switchOnly = false, routing = "", spare = true } = {}) => ({
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "", routing, keepLogin, keepLoginAs, switchOnly,
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: {
    agent: "codex", agentName: "Codex", user: "work@example.com", plan: "PLUS",
    logins: [
      { user: "work@example.com", plan: "PLUS", active: true, on: true },
      { user: "spare@example.com", plan: "PRO", on: spare },
    ],
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
      const logins = list[0].account.logins.map((l) => ({ ...l, active: as ? l.user === as : l.user === "work@example.com" }));
      list = [{ ...list[0], keepLogin: body.keepLogin, keepLoginAs: as, account: { ...list[0].account, logins } }];
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
  en: { switchOnly: "Only switch Codex's sign-in", magpieMoves: /Tick more and magpie moves on to the next/, keep: "Keep Codex signed in to", first: "the first account", pickHead: "Account to stay signed in to", signed: "Signed in", firstLabel: "First", makeFirst: "Make first", keptAs: /Codex on its own stays signed in to spare@example.com/, kept: /Codex on its own stays signed in to the first account/, moves: /once it is 98% used/, usedUp: /once it is used up/ },
  zh: { switchOnly: "只切换 Codex 的登录", magpieMoves: /前一个额度用完 magpie 自动换下一个/, keep: "Codex 始终登录", first: "首选账号", pickHead: "始终登录的账号", signed: "已登录", firstLabel: "首选", makeFirst: "设为首选", keptAs: /Codex 直连时始终登录 spare@example.com/, kept: /Codex 直连时始终登录首选账号/, moves: /用到 98% 时/, usedUp: /额度用完时/ },
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
      await page.route("**/*", serve(lang, codex(opts), posts));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Codex" }).first().click();
      await page.locator(".editor .acc").first().waitFor();
      return { page, errors, posts };
    };

    test(`${engine} ${lang}: the switch off keeps Codex signed in to the first account`, async (t) => {
      const { page, errors, posts } = await open(t, {});
      const sw = page.locator(".editor .accts .auto-switch [role=switch]");
      const on = async () => (await sw.getAttribute("aria-checked")) === "true";
      assert.equal(await on(), true);
      assert.equal(await page.locator(".editor .accts .auto-switch .keep-as").count(), 0);
      assert.match(await page.locator(".editor").textContent(), w.moves);
      const missing = await page.evaluate(() => [
        "Keep {agent} signed in to",
        "the first account",
        "Account to stay signed in to",
        "The account {agent} stays signed in to",
        "{agent} stays signed in to {user}",
        "{agent} is kept signed in to this account; requests through magpie go to the accounts in their order",
        "The gateway uses this account first; {agent} stays signed in to {user}",
        "Routing picks the account for each request through magpie, in the accounts' order; {agent} on its own stays signed in to {user}, whatever it has left.",
        "{agent} stays on this account even when it runs low. Requests through magpie still follow Routing.",
        "{agent} stays signed in to the first account",
        "magpie moves {agent} to an account with room again",
        "Routing picks the account for each request through magpie; {agent} on its own stays signed in to the first account, whatever it has left.",
        "Routing picks the account for each request through magpie; {agent} on its own uses the one it is signed in to, which magpie moves to the next ticked account with room once it is used up, and back to the first once that has room again.",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      const top = await page.evaluate(() => document.scrollingElement.scrollTop);
      await sw.click();
      // the answer drawn: the note says so, the switch is off, the account it stays on shown
      const says = (re) => page.waitForFunction((src) => new RegExp(src).test(document.querySelector(".editor")?.textContent || ""), re.source);
      await says(w.kept);
      assert.equal(await on(), false);
      assert.equal((await page.locator(".editor .accts .auto-switch .as-keep span").first().textContent()).trim(), w.keep);
      assert.deepEqual(posts, [{ id: "codex", keepLogin: true }]);
      assert.match(await page.locator(".editor").textContent(), w.kept);
      assert.doesNotMatch(await page.locator(".editor").textContent(), w.moves);
      assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), top, "the page doesn't move");
      await sw.click();
      await says(w.moves);
      assert.equal(await on(), true);
      assert.deepEqual(posts[1], { id: "codex", keepLogin: false });
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: Codex kept signed in to an account of the user's choosing, the first still first`, async (t) => {
      const { page, errors, posts } = await open(t, { keepLogin: true });
      // the app's own menu, not a native select (the owner: 这里为啥是原生的 selector 而不是我们抽象的 ComboBox？)
      assert.equal(await page.locator(".editor .accts .auto-switch select").count(), 0, "no native select");
      const pick = page.locator(".editor .accts .auto-switch button.keep-as");
      const choose = async (name) => {
        await pick.click();
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        assert.equal((await menu.locator(".pm-head").textContent()).trim(), w.pickHead);
        await menu.locator(".pm-item", { hasText: name }).click();
        assert.equal(await page.locator(".proto-menu").count(), 0, "the menu closes on a pick");
      };
      assert.equal(await pick.getAttribute("data-value"), "");
      assert.equal((await pick.textContent()).trim(), w.first);
      assert.ok(await pick.getAttribute("title"), "it says what it does");
      const top = await page.evaluate(() => document.scrollingElement.scrollTop);
      await pick.click();
      assert.deepEqual((await page.locator(".proto-menu .pm-item .pm-name").allTextContents()).map((s) => s.trim()), [w.first, "work@example.com", "spare@example.com"]);
      await pick.click();
      assert.equal(await page.locator(".proto-menu").count(), 0, "a second click closes it");
      await choose("spare@example.com");
      await page.waitForFunction((src) => new RegExp(src).test(document.querySelector(".editor")?.textContent || ""), w.keptAs.source);
      assert.deepEqual(posts, [{ id: "codex", keepLogin: true, keepLoginAs: "spare@example.com" }]);
      assert.equal(await page.locator(".editor .accts .auto-switch [role=switch]").getAttribute("aria-checked"), "false");
      assert.equal(await page.locator(".editor .accts .auto-switch button.keep-as").getAttribute("data-value"), "spare@example.com");
      assert.equal((await page.locator(".editor .accts .auto-switch button.keep-as").textContent()).trim(), "spare@example.com");
      // the order holds: work first, spare signed in at its place
      const rows = page.locator(".editor .accts .acc[data-account-id]");
      assert.deepEqual(await rows.evaluateAll((rs) => rs.map((r) => r.dataset.accountId)), ["work@example.com", "spare@example.com"]);
      const work = rows.nth(0), spare = rows.nth(1);
      assert.equal((await work.locator(".using").textContent()).trim(), w.firstLabel);
      assert.equal((await spare.locator(".using").textContent()).trim(), w.signed);
      assert.equal(await work.locator("button.text", { hasText: w.makeFirst }).count(), 0);
      // Make first arranges the order, no sign-in
      await spare.locator("button.text", { hasText: w.makeFirst }).click();
      await page.waitForFunction(() => !document.querySelector(".editor .busy"));
      assert.deepEqual(posts[1], { arrange: { id: "codex", accountOrder: ["spare@example.com", "work@example.com"] } });
      assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), top, "the page doesn't move");
      // back to the first account
      await page.locator(".editor .accts .auto-switch button.keep-as").click();
      await page.locator(".proto-menu .pm-item", { hasText: w.first }).click();
      await page.waitForFunction((src) => new RegExp(src).test(document.querySelector(".editor")?.textContent || ""), w.kept.source);
      assert.deepEqual(posts[2], { id: "codex", keepLogin: true });
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: in order, Codex is moved on once the account is used up`, async (t) => {
      const { page, errors } = await open(t, { routing: "order" });
      const text = await page.locator(".editor").textContent();
      assert.match(text, w.usedUp);
      assert.doesNotMatch(text, w.moves);
      assert.deepEqual(errors, []);
    });

    for (const keepLoginAs of ["", "spare@example.com"]) {
      test(`${engine} ${lang}: switching only Codex's sign-in, kept on ${keepLoginAs || "the first"}, it is moved on all the same`, async (t) => {
        const { page, errors } = await open(t, { switchOnly: true, keepLogin: true, keepLoginAs });
        const sw = page.locator(".editor .accts .auto-switch [role=switch]");
        assert.equal(await sw.getAttribute("aria-checked"), "true", "switch-only always moves it");
        assert.equal(await sw.isDisabled(), true);
        assert.equal(await page.locator(".editor .accts .auto-switch .keep-as").count(), 0);
        const only = page.locator(".editor .accts .switch-only label");
        assert.equal((await only.textContent()).trim(), w.switchOnly);
        assert.equal(await only.locator("input").isChecked(), true);
        const text = await page.locator(".editor").textContent();
        assert.match(text, w.moves);
        assert.doesNotMatch(text, w.kept);
        assert.doesNotMatch(text, w.keptAs);
        assert.match(text, w.magpieMoves);
        assert.equal(await page.evaluate(() => I18N.zh["{agent} signs in to the first. Tick more and magpie moves on to the next when the one before it is out of quota. Sessions already running keep theirs until restarted."] ? "" : "missing"), "");
        assert.deepEqual(errors, []);
      });
    }

    test(`${engine} ${lang}: with one account on, there is nothing to keep`, async (t) => {
      const { page, errors } = await open(t, { spare: false });
      assert.equal(await page.locator(".editor .accts .auto-switch").count(), 0);
      assert.deepEqual(errors, []);
    });
  }
}
