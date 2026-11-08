// Run with Node's test runner and Playwright on the module path; see README.md.
// A key's spend cap (sparkling_lemur_62868 on Discord: a team's OpenAI key
// with credits other services spend from too, and magpie to stop at a
// share of them). Each key's row has a pill: what it spent of its cap
// today, this month or in all, else No spend cap (always shown, as the
// row's other unset pills). A key at its cap is marked, with a line under
// its row that says so and when it is back. A click opens the app's own
// menu (no native select): No spend cap, or a period and then an amount
// typed in place, posted to provider/keyspendcap; an amount that isn't one
// is refused and nothing is posted. A click moves nothing; at a phone's
// width the row stays inside the window. In every language.
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
  keyList: [
    { id: "k1", name: "Main", masked: "sk-…one", on: true, spend: { usd: 20, period: "day", spent: 7.5 } },
    { id: "k2", name: "Team", masked: "sk-…two", on: true, spend: { usd: 5, period: "month", spent: 6, held: true, reset: new Date(Date.now() + 3 * 864e5).toISOString() } },
    { id: "k3", name: "Spare", masked: "sk-…three", on: true },
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
      if (action === "keyspendcap") {
        const k = relay.keyList.find((x) => x.id === body.account);
        if (body.spend) k.spend = { ...body.spend, spent: k.spend?.spent || 0 };
        else delete k.spend;
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
  en: { k1: "$7.50 of $20.00 today", k2: "$6.00 of $5.00 this month", none: "No spend cap", held: "At its spend cap · back ", menu: "Spend cap", month: "Per month…", k3: "$0.00 of $12.50 this month", bad: "A spend cap is an amount in USD above 0, up to 1,000,000" },
  zh: { k1: "今日 $7.50 / $20.00", k2: "本月 $6.00 / $5.00", none: "无消费上限", held: "已达消费上限 · ", menu: "消费上限", month: "每月…", k3: "本月 $0.00 / $12.50", bad: "消费上限是大于 0、不超过 1,000,000 的美元金额" },
  "zh-TW": { k1: "今日 $7.50 / $20.00", k2: "本月 $6.00 / $5.00", none: "無消費上限", held: "已達消費上限 · ", menu: "消費上限", month: "每月…", k3: "本月 $0.00 / $12.50", bad: "消費上限是大於 0、不超過 1,000,000 的美元金額" },
  ja: { k1: "今日 $7.50 / $20.00", k2: "今月 $6.00 / $5.00", none: "支出上限なし", held: "支出上限に到達 · ", menu: "支出上限", month: "1か月あたり…", k3: "今月 $0.00 / $12.50", bad: "支出上限は 0 より大きく 1,000,000 以下の USD 金額です" },
  de: { k1: "Heute $7.50 von $20.00", k2: "Diesen Monat $6.00 von $5.00", none: "Kein Ausgabenlimit", held: "Am Ausgabenlimit · zurück ", menu: "Ausgabenlimit", month: "Pro Monat…", k3: "Diesen Monat $0.00 von $12.50", bad: "Ein Ausgabenlimit ist ein Betrag in USD über 0, bis 1.000.000" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [900, 400]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: a key's spend cap shows what it spent, marks one at its cap, and is set from the app's menu`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        let page;
        t.after(async () => {
          if (process.env.ARTIFACT_DIR && page) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-key-spend-cap.png`) });
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
        await page.locator(".editor .accts .acc .aspend").first().waitFor({ state: "attached" });
        await page.waitForTimeout(300);
        const row = (id) => page.locator(`.editor .accts .acc[data-account-id="${id}"]`);
        const pill = (id) => row(id).locator(".aspend");
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
        assert.equal(await pill("k2").textContent(), w.k2);
        assert.equal(await pill("k3").textContent(), w.none);
        assert.equal(await page.locator("select").count(), 0, "no native select");
        // one at its cap is marked, with a line saying when it is back
        assert.equal(await pill("k2").evaluate((e) => e.classList.contains("at-cap")), true);
        assert.equal(await pill("k1").evaluate((e) => e.classList.contains("at-cap")), false);
        const line = row("k2").locator(".key-spend-line");
        assert.equal(await line.count(), 1);
        assert((await line.textContent()).startsWith(w.held), await line.textContent());
        assert.equal(await row("k1").locator(".key-spend-line").count(), 0);
        // a cap set is shown off the pointer, and so is none (#841)
        await page.mouse.move(0, 0);
        await page.waitForTimeout(250);
        assert.equal(await pill("k1").evaluate((e) => getComputedStyle(e).opacity), "1");
        // inside the window at every width, the page not scrolled sideways
        const over = await page.evaluate(() => [...document.querySelectorAll(".editor .accts .acc")].filter((e) => e.getBoundingClientRect().right > innerWidth + 1).length);
        assert.equal(over, 0, "a key's row past the window's edge");
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth + 1), true, "the page scrolls sideways");

        // k3: a month's cap of 12.5, typed in place
        await still("k3");
        await menu.waitFor();
        assert.equal(await menu.locator(".pm-head").textContent(), w.menu);
        await item(w.month).click();
        const box = row("k3").locator("input.aspend-in");
        await box.waitFor();
        let n = posts.length;
        await box.fill("lots");
        await box.press("Enter");
        await page.waitForTimeout(300);
        assert.equal(posts.length, n, "a bad amount was posted");
        assert.equal(await page.locator("#status").textContent(), w.bad);
        await box.waitFor({ state: "detached" });
        await pill("k3").waitFor();
        await page.waitForTimeout(300);
        // the Enter that was refused can open the menu again on the pill
        // drawn in the field's place: pick from it, else open it
        if (!(await menu.isVisible())) await still("k3");
        await menu.waitFor();
        await item(w.month).click();
        await box.waitFor();
        await box.fill("12.5");
        await box.press("Enter");
        let p = await posted(n);
        assert.equal(p.action, "keyspendcap");
        assert.deepEqual(p.body, { id: "relay", account: "k3", spend: { usd: 12.5, period: "month" } });
        await page.waitForTimeout(250);
        assert.equal(await pill("k3").textContent(), w.k3);

        // k1's cap taken off
        await still("k1");
        n = posts.length;
        await item(w.none).click();
        p = await posted(n);
        assert.deepEqual(p.body, { id: "relay", account: "k1", spend: null });
        await page.waitForTimeout(250);
        assert.equal(await pill("k1").textContent(), w.none);

        // a left border thicker or another colour than the right one is a stripe
        const stripes = await page.evaluate(() => [...document.querySelectorAll(".aspend, .aspend-in, .key-spend-line, .key-spend-line *")].map((e) => getComputedStyle(e)).filter((s) => parseFloat(s.borderLeftWidth) > parseFloat(s.borderRightWidth) || (parseFloat(s.borderLeftWidth) > 0 && s.borderLeftColor !== s.borderRightColor)).length);
        assert.equal(stripes, 0, "no border stripes");
        assert.deepEqual(errors, []);
      });
    }
  }
}
