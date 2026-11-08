// Run with Node's test runner and Playwright on the module path; see README.md.
// plugins#349: under a subscription's accounts, once any has a cap, two
// ticks say how the pool ends at its caps: stop there (429, no fallback)
// and check usage again near a cap. Each posts its own action and the
// redraw keeps it ticked; a note names the accounts in use with no cap.
// With no cap and neither on, there is no box. The page doesn't move. In
// English, Chinese, Japanese and German, Chromium and WebKit, at 1100 and
// 420 wide.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const codex = ({ capped = true, capStop = false, capCareful = false } = {}) => ({
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "", routing: "ordered", capStop, capCareful,
  accountCaps: capped ? { "work@example.com": 90 } : {},
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: {
    agent: "codex", agentName: "Codex", user: "work@example.com", plan: "PLUS",
    logins: [
      { user: "work@example.com", plan: "PLUS", active: true, on: true },
      { user: "spare@example.com", plan: "PRO", on: true },
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
    if (url.pathname === "/api/provider/capstop" || url.pathname === "/api/provider/capcareful") {
      const body = route.request().postDataJSON();
      posts.push(body);
      list = [{ ...list[0], ...body }];
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

const label = {
  en: ["Stop when every account is at its cap", "Check usage again near a cap", "No cap on spare@example.com: used to 100% of each window"],
  zh: ["所有账号都到上限时停止", "接近上限时重新读取用量", "spare@example.com 没有上限：每个窗口都会用到 100%"],
  ja: ["すべてのアカウントが上限に達したら止める", "上限に近づいたら使用量を読み直す", "spare@example.com には上限がありません：各ウィンドウを 100% まで使います"],
  de: ["Anhalten, wenn jedes Konto sein Limit erreicht hat", "Nutzung nahe am Limit neu lesen", "Kein Limit für spare@example.com: jedes Fenster wird bis 100 % genutzt"],
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const lang of Object.keys(label)) {
      const open = async (t, opts) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
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

      test(`${engine} ${width} ${lang}: a capped pool's ticks stop at the caps and read usage again`, async (t) => {
        const { page, errors, posts } = await open(t, {});
        const ticks = page.locator(".editor .accts .pool-ends label");
        assert.equal(await ticks.count(), 2);
        assert.deepEqual((await ticks.allTextContents()).map((s) => s.trim()), label[lang].slice(0, 2));
        for (const i of [0, 1]) {
          assert.ok(await ticks.nth(i).getAttribute("title"), "it says what it does");
          assert.equal(await ticks.nth(i).locator("input").isChecked(), false, "off by default");
        }
        assert.equal((await page.locator(".editor .accts .pool-ends .note").textContent()).trim(), label[lang][2], "the uncapped account is named");
        assert.equal(await page.locator(".editor .accts .keep-login.pool-ends, .editor .accts .keep-login .pool-ends").count(), 0, "apart from keep-login's box");
        if (lang === "en") {
          const strings = await page.evaluate(() => [...document.querySelectorAll(".editor .accts .pool-ends label")].map((l) => l.title));
          const keys = ["Stop when every account is at its cap", "Check usage again near a cap", "No cap on {users}: used to 100% of each window",
            "On: once every account in use is at its cap, requests get 429 instead of going to the fallbacks.",
            "On: an account near its cap has its usage read again before a request. One whose usage can't be read is held."];
          assert.deepEqual(strings, keys.slice(3));
          const missing = await page.evaluate((ks) => ["zh", "zh-TW", "ja", "de"].flatMap((l) => ks.filter((k) => !I18N[l][k]).map((k) => `${l}: ${k}`)), keys);
          assert.deepEqual(missing, [], "every string has its translations");
        }
        await ticks.nth(1).scrollIntoViewIfNeeded();
        for (const i of [0, 1]) {
          const box = await ticks.nth(i).boundingBox();
          assert.ok(box.x >= 0 && box.x + box.width <= width, `tick ${i} fits across ${width}`);
        }
        const scrollers = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop).concat(document.scrollingElement.scrollTop));
        const before = await scrollers();
        const nth = (i) => `document.querySelectorAll(".editor .accts .pool-ends input")[${i}]?.checked`;
        await ticks.nth(0).locator("input").click();
        await page.waitForFunction(`${nth(0)} === true`);
        assert.deepEqual(posts, [{ id: "codex", capStop: true }]);
        await page.locator(".editor .accts .pool-ends input").nth(1).click();
        await page.waitForFunction(`${nth(1)} === true && ${nth(0)} === true`);
        assert.deepEqual(posts, [{ id: "codex", capStop: true }, { id: "codex", capCareful: true }]);
        assert.deepEqual(await scrollers(), before, "the page doesn't move");
        await page.locator(".editor .accts .pool-ends input").nth(0).click();
        await page.waitForFunction(`${nth(0)} === false && ${nth(1)} === true`);
        assert.deepEqual(posts.at(-1), { id: "codex", capStop: false });
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${width} ${lang}: the pool's ticks are drawn as set, and not at all with no cap`, async (t) => {
        const on = await open(t, { capStop: true, capCareful: true });
        assert.deepEqual(await on.page.locator(".editor .accts .pool-ends input").evaluateAll((is) => is.map((i) => i.checked)), [true, true]);
        const none = await open(t, { capped: false });
        assert.equal(await none.page.locator(".editor .accts .pool-ends").count(), 0);
        // no cap left but a tick still on: shown, so it can be turned off
        const left = await open(t, { capped: false, capStop: true });
        assert.equal(await left.page.locator(".editor .accts .pool-ends input").count(), 2);
        assert.deepEqual([...on.errors, ...none.errors, ...left.errors], []);
      });
    }
  }
}
