// Run with Node's test runner and Playwright on the module path; see README.md.
// A key's balance typed by hand (tenacious_eagle on Discord: a provider
// with no balance URL). Where the vendor tells no balance, each key's row
// has a pill: what is left of the balance typed, marked as an estimate
// (≈), else Set balance (always shown, as the row's other unset pills).
// Set balance opens the amount in place; a balance set opens the app's
// menu (no native select): Enter balance…, a refill each month (a day)
// or each week (a weekday), Never refills, or No balance, posted to
// provider/keyownbalance and provider/keybalancerefill. An amount in yuan
// ("¥100", or 100 with ¥ CNY picked beside the field) is posted as CNY
// and shown in yuan; a balance's currency is picked there, the number kept. An amount that isn't one is
// refused and nothing is posted. A click moves nothing; at a phone's width the row stays
// inside the window. In every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const fresh = () => ({
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" },
  takesOwnBalance: true,
  keyList: [
    { id: "k1", name: "Main", masked: "sk-…one", on: true, balance: { set: 20, at: new Date(Date.now() - 864e5).toISOString(), spent: 7.5, left: 12.5 } },
    { id: "k2", name: "Team", masked: "sk-…two", on: true },
  ],
  balanceToken: { takes: false, set: false }, proxy: "",
});

function serve(lang, posts) {
  const relay = fresh();
  const providers = { providers: [relay], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/lanes") return json({});
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/provider/") && route.request().method() === "POST") {
      const action = url.pathname.slice("/api/provider/".length), body = route.request().postDataJSON();
      posts.push({ action, body });
      if (action === "keyownbalance") {
        const k = relay.keyList.find((x) => x.id === body.account);
        if (body.balance != null) k.balance = { set: body.balance, at: new Date().toISOString(), spent: 0, left: body.balance, ...body.currency && { currency: body.currency }, ...k.balance?.refill && { refill: k.balance.refill, next: k.balance.next } };
        else delete k.balance;
      }
      if (action === "keybalancerefill") {
        const k = relay.keyList.find((x) => x.id === body.account);
        if (body.refill) Object.assign(k.balance, { refill: body.refill, next: new Date(Date.now() + 5 * 864e5).toISOString() });
        else delete k.balance.refill, delete k.balance.next;
      }
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { currency: "Currency", yuan40: "≈¥40.00 left", usd100: "≈$100.00 left", k1: "≈$12.50 left", none: "Set balance", menu: "Balance", enter: "Enter balance…", off: "No balance", k2: "≈$5.00 left", k1again: "≈$40.00 left", yuan: "≈¥100.00 left", bad: "A balance is an amount from -1,000,000 to 1,000,000, in USD or in yuan (¥)", month: "Refills each month…", week: "Refills each week…", day: "Refills each day", never: "Never refills", on: "Refills on", refills: "Refills to $40.00 on" },
  zh: { currency: "货币", yuan40: "≈¥40.00 剩余", usd100: "≈$100.00 剩余", k1: "≈$12.50 剩余", none: "设置余额", menu: "余额", enter: "填写余额…", off: "无余额", k2: "≈$5.00 剩余", k1again: "≈$40.00 剩余", yuan: "≈¥100.00 剩余", bad: "余额是 -1,000,000 到 1,000,000 之间的金额，美元或人民币（¥）", month: "每月恢复…", week: "每周恢复…", day: "每日恢复", never: "不恢复", on: "恢复于", refills: "恢复为 $40.00" },
  "zh-TW": { currency: "貨幣", yuan40: "≈¥40.00 剩餘", usd100: "≈$100.00 剩餘", k1: "≈$12.50 剩餘", none: "設定餘額", menu: "餘額", enter: "填寫餘額…", off: "無餘額", k2: "≈$5.00 剩餘", k1again: "≈$40.00 剩餘", yuan: "≈¥100.00 剩餘", bad: "餘額是 -1,000,000 到 1,000,000 之間的金額，美元或人民幣（¥）", month: "每月恢復…", week: "每週恢復…", day: "每日恢復", never: "不恢復", on: "恢復於", refills: "恢復為 $40.00" },
  ja: { currency: "通貨", yuan40: "残り ≈¥40.00", usd100: "残り ≈$100.00", k1: "残り ≈$12.50", none: "残高を設定", menu: "残高", enter: "残高を入力…", off: "残高なし", k2: "残り ≈$5.00", k1again: "残り ≈$40.00", yuan: "残り ≈¥100.00", bad: "残高は -1,000,000 から 1,000,000 までの金額です（USD または人民元 ¥）", month: "毎月戻す…", week: "毎週戻す…", day: "毎日戻す", never: "戻さない", on: "戻す曜日", refills: "$40.00 へ戻る" },
  de: { currency: "Währung", yuan40: "≈¥40.00 übrig", usd100: "≈$100.00 übrig", k1: "≈$12.50 übrig", none: "Guthaben festlegen", menu: "Guthaben", enter: "Guthaben eingeben…", off: "Kein Guthaben", k2: "≈$5.00 übrig", k1again: "≈$40.00 übrig", yuan: "≈¥100.00 übrig", bad: "Ein Guthaben ist ein Betrag von -1.000.000 bis 1.000.000, in USD oder in Yuan (¥)", month: "Monatlich auffüllen…", week: "Wöchentlich auffüllen…", day: "Täglich auffüllen", never: "Nie auffüllen", on: "Auffüllen am", refills: "wieder auf $40.00 aufgefüllt" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [900, 400]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: a key's balance typed by hand shows what is left as an estimate, and is typed again or taken off`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        let page;
        t.after(async () => {
          if (process.env.ARTIFACT_DIR && page) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-key-own-balance.png`) });
          }
          await browser.close();
        });
        page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [];
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Relay" }).first().click();
        await page.locator(".editor .accts .acc .abal").first().waitFor({ state: "attached" });
        await page.waitForTimeout(300);
        const row = (id) => page.locator(`.editor .accts .acc[data-account-id="${id}"]`);
        const pill = (id) => row(id).locator(".abal");
        const box = (id) => row(id).locator("input.abal-in");
        const cur = (id) => row(id).locator(".abal-cur");
        const menu = page.locator(".proto-menu");
        const item = (name) => menu.locator(".pm-item", { has: page.locator(".pm-name", { hasText: new RegExp(`^${name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`) }) });
        const scrolled = () => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop > 0).map((e) => `${e.className}:${e.scrollTop}`)].join(" "));
        const still = async (id) => {
          await row(id).hover();
          await page.waitForTimeout(150);
          const before = await row(id).evaluate((e) => Math.round(e.getBoundingClientRect().top)), sc = await scrolled();
          await pill(id).click();
          await page.waitForTimeout(200);
          assert.equal(await row(id).evaluate((e) => Math.round(e.getBoundingClientRect().top)), before, `${id}'s row moved`);
          assert.equal(await scrolled(), sc, "a click scrolled");
        };
        const posted = async (n) => {
          for (let i = 0; i < 60 && posts.length === n; i++) await new Promise((r) => setTimeout(r, 50));
          assert(posts.length > n, "nothing posted");
          return posts.at(-1);
        };

        assert.equal(await pill("k1").textContent(), w.k1);
        assert.equal(await pill("k2").textContent(), w.none);
        assert.equal(await page.locator("select").count(), 0, "no native select");
        // a balance typed is shown off the pointer, and so is none (#841)
        await page.mouse.move(0, 0);
        await page.waitForTimeout(250);
        assert.equal(await pill("k1").evaluate((e) => getComputedStyle(e).opacity), "1");
        const over = await page.evaluate(() => [...document.querySelectorAll(".editor .accts .acc")].filter((e) => e.getBoundingClientRect().right > innerWidth + 1).length);
        assert.equal(over, 0, "a key's row past the window's edge");
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth + 1), true, "the page scrolls sideways");

        // k2: none typed, so the amount opens in place; a bad one is refused
        await still("k2");
        await box("k2").waitFor();
        let n = posts.length;
        await box("k2").fill("lots");
        await box("k2").press("Enter");
        await page.waitForTimeout(300);
        assert.equal(posts.length, n, "a bad amount was posted");
        assert.equal(await page.locator("#status").textContent(), w.bad);
        // the Enter that was refused can press the pill drawn in the
        // field's place, which opens the field again: type in it, else
        // press the pill
        await page.waitForTimeout(300);
        if (!(await box("k2").isVisible())) await still("k2");
        await box("k2").waitFor();
        await box("k2").fill("$5");
        await box("k2").press("Enter");
        let p = await posted(n);
        assert.equal(p.action, "keyownbalance");
        assert.deepEqual(p.body, { id: "relay", account: "k2", balance: 5 });
        await page.waitForTimeout(250);
        assert.equal(await pill("k2").textContent(), w.k2);

        // k1: typed again from the menu, then taken off
        await still("k1");
        await menu.waitFor();
        assert.equal(await menu.locator(".pm-head").textContent(), w.menu);
        await item(w.enter).click();
        await box("k1").waitFor();
        assert.equal(await box("k1").inputValue(), "20");
        n = posts.length;
        await box("k1").fill("40");
        await box("k1").press("Enter");
        p = await posted(n);
        assert.deepEqual(p.body, { id: "relay", account: "k1", balance: 40 });
        await page.waitForTimeout(250);
        assert.equal(await pill("k1").textContent(), w.k1again);

        // a key the vendor let run past its balance: a negative amount is
        // posted as it is typed (AlanWang1027713 on X), with the sign
        // before the currency's too, then 40 again
        for (const [typed, want] of [["-1.84", -1.84], ["-$2", -2]]) {
          await still("k1");
          await menu.waitFor();
          await item(w.enter).click();
          await box("k1").waitFor();
          n = posts.length;
          await box("k1").fill(typed);
          await box("k1").press("Enter");
          p = await posted(n);
          assert.deepEqual(p.body, { id: "relay", account: "k1", balance: want }, typed);
          await page.waitForTimeout(250);
        }
        await still("k1");
        await menu.waitFor();
        await item(w.enter).click();
        await box("k1").waitFor();
        n = posts.length;
        await box("k1").fill("40");
        await box("k1").press("Enter");
        p = await posted(n);
        assert.deepEqual(p.body, { id: "relay", account: "k1", balance: 40 });
        await page.waitForTimeout(250);

        // k1 refills on a day of each month, then each Friday, then each day, then never
        await still("k1");
        await menu.waitFor();
        await item(w.month).click();
        await box("k1").waitFor();
        n = posts.length;
        await box("k1").fill("40");
        await box("k1").press("Enter");
        await page.waitForTimeout(300);
        assert.equal(posts.length, n, "day 40 was posted");
        // the refused Enter can press the pill, which opens its menu
        await page.waitForTimeout(300);
        if (!(await box("k1").isVisible())) {
          if (!(await menu.isVisible())) await still("k1");
          await item(w.month).click();
        }
        await box("k1").waitFor();
        await box("k1").fill("1");
        await box("k1").press("Enter");
        p = await posted(n);
        assert.equal(p.action, "keybalancerefill");
        assert.deepEqual(p.body, { id: "relay", account: "k1", refill: { every: "month", day: 1 } });
        await page.waitForTimeout(250);
        assert.match(await pill("k1").getAttribute("title"), new RegExp(w.refills.replace(/[$.]/g, "\\$&")));
        await still("k1");
        await menu.waitFor();
        await item(w.week).click();
        await page.waitForTimeout(200);
        await menu.waitFor();
        assert.equal(await menu.locator(".pm-head").textContent(), w.on);
        assert.equal(await menu.locator(".pm-item").count(), 7);
        const friday = await page.evaluate(() => new Date(2026, 10, 6).toLocaleDateString(intlLang(), { weekday: "long" }));
        n = posts.length;
        await item(friday).click();
        p = await posted(n);
        assert.deepEqual(p.body, { id: "relay", account: "k1", refill: { every: "week", day: 5 } });
        await page.waitForTimeout(250);
        // each day (Kelen_Yc on X): picked at once, no day to choose, and
        // the menu shows it picked
        await still("k1");
        await menu.waitFor();
        n = posts.length;
        await item(w.day).click();
        p = await posted(n);
        assert.deepEqual(p.body, { id: "relay", account: "k1", refill: { every: "day", day: 0 } });
        await page.waitForTimeout(250);
        assert.equal(await page.locator(".proto-menu").count(), 0, "a daily refill asked for more");
        await still("k1");
        await menu.waitFor();
        assert.equal(await item(w.day).getAttribute("aria-checked"), "true");
        await page.keyboard.press("Escape");
        await page.waitForTimeout(200);
        await still("k1");
        await menu.waitFor();
        n = posts.length;
        await item(w.never).click();
        p = await posted(n);
        assert.deepEqual(p.body, { id: "relay", account: "k1", refill: null });
        await page.waitForTimeout(250);
        assert.doesNotMatch(await pill("k1").getAttribute("title"), new RegExp(w.refills.replace(/[$.]/g, "\\$&")));

        // k2 typed again in yuan
        await still("k2");
        await menu.waitFor();
        await item(w.enter).click();
        await box("k2").waitFor();
        n = posts.length;
        await box("k2").fill("¥100");
        // the sign typed says yuan, and the currency beside it follows
        assert.equal((await cur("k2").textContent()).trim(), "CNY");
        await box("k2").press("Enter");
        p = await posted(n);
        assert.deepEqual(p.body, { id: "relay", account: "k2", balance: 100, currency: "cny" });
        await page.waitForTimeout(250);
        assert.equal(await pill("k2").textContent(), w.yuan);

        // k1's $40 was meant in yuan (tenacious_eagle): the currency is
        // picked beside the field, the number kept as typed, and nothing
        // is posted before Enter
        await still("k1");
        await menu.waitFor();
        await item(w.enter).click();
        await box("k1").waitFor();
        assert.equal(await box("k1").inputValue(), "40");
        assert.equal((await cur("k1").textContent()).trim(), "USD");
        n = posts.length;
        await cur("k1").click();
        await menu.waitFor();
        assert.equal(await menu.locator(".pm-head").textContent(), w.currency);
        assert.equal(await menu.locator(".pm-item.on .pm-name").textContent(), "$ USD");
        await item("¥ CNY").click();
        await page.waitForTimeout(200);
        assert.equal(posts.length, n, "picking the currency posted");
        assert.equal(await box("k1").inputValue(), "40");
        assert.equal((await cur("k1").textContent()).trim(), "CNY");
        assert.equal(await box("k1").evaluate((e) => e === document.activeElement), true, "the field lost its focus");
        assert.equal(await box("k1").getAttribute("aria-label"), await page.evaluate(() => t("Balance, in CNY")));
        assert.equal(await page.evaluate(() => [...document.querySelectorAll(".editor .accts .acc")].filter((e) => e.getBoundingClientRect().right > innerWidth + 1).length), 0, "the field's row past the window's edge");
        await box("k1").press("Enter");
        p = await posted(n);
        assert.deepEqual(p.body, { id: "relay", account: "k1", balance: 40, currency: "cny" });
        await page.waitForTimeout(250);
        assert.equal(await pill("k1").textContent(), w.yuan40);

        // k2's ¥100 back to dollars by the keyboard, saved as it is left;
        // Escape in the menu picks nothing and posts nothing
        await still("k2");
        await menu.waitFor();
        await item(w.enter).click();
        await box("k2").waitFor();
        assert.equal(await box("k2").inputValue(), "100");
        assert.equal((await cur("k2").textContent()).trim(), "CNY");
        n = posts.length;
        await cur("k2").click();
        await menu.waitFor();
        await page.keyboard.press("Escape");
        await page.waitForTimeout(200);
        assert.equal(await menu.count(), 0);
        assert.equal(posts.length, n, "Escape in the currency's menu posted");
        assert.equal((await cur("k2").textContent()).trim(), "CNY");
        await cur("k2").click();
        await menu.waitFor();
        await item("$ USD").click();
        await page.waitForTimeout(200);
        assert.equal(posts.length, n, "picking the currency posted");
        await page.keyboard.press("Tab");
        await page.keyboard.press("Tab");
        p = await posted(n);
        assert.deepEqual(p.body, { id: "relay", account: "k2", balance: 100 });
        await page.waitForTimeout(250);
        assert.equal(await pill("k2").textContent(), w.usd100);

        await still("k1");
        await menu.waitFor();
        n = posts.length;
        await item(w.off).click();
        p = await posted(n);
        assert.deepEqual(p.body, { id: "relay", account: "k1", balance: null });
        await page.waitForTimeout(250);
        assert.equal(await pill("k1").textContent(), w.none);

        const stripes = await page.evaluate(() => [...document.querySelectorAll(".abal, .abal-in")].map((e) => getComputedStyle(e)).filter((s) => parseFloat(s.borderLeftWidth) > parseFloat(s.borderRightWidth) || (parseFloat(s.borderLeftWidth) > 0 && s.borderLeftColor !== s.borderRightColor)).length);
        assert.equal(stripes, 0, "no border stripes");
        assert.deepEqual(errors, []);
      });
    }
  }
}
