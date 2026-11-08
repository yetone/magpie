// Run with Node's test runner and Playwright on the module path; see README.md.
// A key's balance typed by hand may have a part that expires
// (tenacious_eagle_96548 on Discord: money that stays and credit good
// until a date). The key's pill says how much of that credit is left and
// in how many days it goes, amber from a week; the Usage page's card and
// the panel's Balances say it on a line under the balance, amber the same
// way. From the pill's menu (the app's, no native select), Credit that
// expires… takes its amount, in the balance's currency, then its last
// day, and posts provider/keyexpiring; a sign of the other currency or a
// day gone by is refused and nothing posted. No credit that expires takes
// it off, and is offered for credit that has expired too, which the
// pill's title says no longer counts. In every language, at a phone's
// width the row stays inside the window.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
// the end of credit whose last day is n days from today: midnight after
// it, here
const ends = (n) => { const d = new Date(); return new Date(d.getFullYear(), d.getMonth(), d.getDate() + n + 1).toISOString(); };
const dayIn = (n) => { const d = new Date(); d.setDate(d.getDate() + n); return [d.getFullYear(), d.getMonth() + 1, d.getDate()].map((x) => String(x).padStart(2, "0")).join("-"); };
const ago = new Date(Date.now() - 864e5).toISOString();
const fresh = () => ({
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" },
  takesOwnBalance: true,
  keyList: [
    { id: "k1", name: "Main", masked: "sk-…one", on: true, balance: { set: 70, at: ago, spent: 10, left: 60, expiring: { set: 30, at: ago, left: 20, ends: ends(30) } } },
    { id: "k2", name: "Team", masked: "sk-…two", on: true, balance: { set: 40, at: ago, spent: 0, left: 40, currency: "cny", rate: 7.2, expiring: { set: 5, at: ago, left: 5, ends: ends(3) } } },
    { id: "k3", name: "Spare", masked: "sk-…three", on: true, balance: { set: 10, at: ago, spent: 0, left: 10, expired: ends(-1) } },
  ],
  balanceToken: { takes: false, set: false }, proxy: "",
});

const quotas = [
  { provider: "relay", name: "Relay", icon: "generic", user: "Main", windows: [], balance: "$60.00", balanceSplit: [{ text: "$20.00", expires: ends(30) }] },
  { provider: "relay", name: "Relay", icon: "generic", user: "Team", windows: [], balance: "¥40.00", balanceSplit: [{ text: "¥5.00", expires: ends(3) }] },
  { provider: "relay", name: "Relay", icon: "generic", user: "Last", windows: [], balance: "$9.00", balanceSplit: [{ text: "$1.00", expires: ends(0) }] },
];

function serve(lang, posts, relay) {
  const providers = { providers: [relay], presets: [], excluded: [], gateway: { running: true, window: true } };
  const settings = { lang, theme: "light", tray: "panel", quotaLeft: true, currency: "usd", usageOrder: [], panelUsageHidden: [] };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/usage") return json({ days: [] });
    if (url.pathname === "/api/lanes") return json({});
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/provider/") && route.request().method() === "POST") {
      const action = url.pathname.slice("/api/provider/".length), body = route.request().postDataJSON();
      posts.push({ action, body });
      if (action === "keyexpiring") {
        const b = relay.keyList.find((x) => x.id === body.account).balance;
        delete b.expired;
        if (body.expiring) {
          const [y, m, d] = body.expiring.until.split("-").map(Number);
          b.expiring = { set: body.expiring.amount, at: new Date().toISOString(), left: body.expiring.amount, ends: new Date(y, m - 1, d + 1).toISOString() };
        } else delete b.expiring;
      }
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { k1: "≈$60.00 left · $20.00 expires in 30 days", k2: "≈¥40.00 left · ¥5.00 expires in 3 days", k1off: "≈$60.00 left", k3: "≈$10.00 left", k3new: "≈$10.00 left · $15.00 expires in 10 days",
    in30: "Expires in 30 days", in3: "Expires in 3 days", today: "Expires today", exp: "Credit that expires…", noexp: "No credit that expires",
    badAmount: "Credit that expires is an amount above 0, in the balance's currency", badDay: "The last day is a date from today on, as 2026-12-31", gone: "no longer counts" },
  zh: { k1: "≈$60.00 剩余 · $20.00 30 天后到期", k2: "≈¥40.00 剩余 · ¥5.00 3 天后到期", k1off: "≈$60.00 剩余", k3: "≈$10.00 剩余", k3new: "≈$10.00 剩余 · $15.00 10 天后到期",
    in30: "30 天后到期", in3: "3 天后到期", today: "今天到期", exp: "限期额度…", noexp: "去掉限期额度",
    badAmount: "限期额度是大于 0 的金额，用余额的货币", badDay: "最后一天是今天或以后的日期，如 2026-12-31", gone: "已不再计入" },
  "zh-TW": { k1: "≈$60.00 剩餘 · $20.00 30 天後到期", k2: "≈¥40.00 剩餘 · ¥5.00 3 天後到期", k1off: "≈$60.00 剩餘", k3: "≈$10.00 剩餘", k3new: "≈$10.00 剩餘 · $15.00 10 天後到期",
    in30: "30 天後到期", in3: "3 天後到期", today: "今天到期", exp: "限期額度…", noexp: "去掉限期額度",
    badAmount: "限期額度是大於 0 的金額，用餘額的貨幣", badDay: "最後一天是今天或以後的日期，如 2026-12-31", gone: "已不再計入" },
  ja: { k1: "残り ≈$60.00 · $20.00 は 30 日後に失効", k2: "残り ≈¥40.00 · ¥5.00 は 3 日後に失効", k1off: "残り ≈$60.00", k3: "残り ≈$10.00", k3new: "残り ≈$10.00 · $15.00 は 10 日後に失効",
    in30: "30 日後に失効", in3: "3 日後に失効", today: "今日で失効", exp: "期限付きクレジット…", noexp: "期限付きクレジットなし",
    badAmount: "期限付きクレジットは 0 より大きい金額です（残高の通貨で）", badDay: "最終日は今日以降の日付です（例: 2026-12-31）", gone: "もう数えません" },
  de: { k1: "≈$60.00 übrig · $20.00 läuft in 30 Tagen ab", k2: "≈¥40.00 übrig · ¥5.00 läuft in 3 Tagen ab", k1off: "≈$60.00 übrig", k3: "≈$10.00 übrig", k3new: "≈$10.00 übrig · $15.00 läuft in 10 Tagen ab",
    in30: "Läuft in 30 Tagen ab", in3: "Läuft in 3 Tagen ab", today: "Läuft heute ab", exp: "Ablaufendes Guthaben…", noexp: "Kein ablaufendes Guthaben",
    badAmount: "Ablaufendes Guthaben ist ein Betrag über 0, in der Währung des Guthabens", badDay: "Der letzte Tag ist ein Datum ab heute, etwa 2026-12-31", gone: "zählt nicht mehr" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [900, 400]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: a key's balance has credit that expires, said in days and amber from a week`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        let page;
        t.after(async () => {
          if (process.env.ARTIFACT_DIR && page) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-key-balance-expiring.png`) });
          }
          await browser.close();
        });
        const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
        await context.addInitScript(() => { try { localStorage.setItem("magpie.panelTab", "usage"); } catch {} });
        page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [], relay = fresh();
        await page.route("**/*", serve(lang, posts, relay));

        // the Usage page's cards: a line under the balance, amber within a week
        await page.goto("http://magpie.test/?view=usage&tab=usage");
        await page.waitForFunction(() => document.querySelectorAll("#subscriptionUsage .bal-part").length >= 6);
        const lines = () => page.evaluate(() => [...document.querySelectorAll("#subscriptionUsage .quota-balance")].map((b) =>
          [...b.querySelectorAll(".bal-part:not(.lead)")].map((r) => [r.querySelector("span").textContent, r.querySelector("b").textContent, r.classList.contains("soon")])));
        assert.deepEqual(await lines(), [[[w.in30, "$20.00", false]], [[w.in3, "¥5.00", true]], [[w.today, "$1.00", true]]]);
        const soonColor = () => page.evaluate(() => {
          const e = document.createElement("i");
          e.style.color = "var(--amber)";
          document.body.append(e);
          const a = getComputedStyle(e).color;
          e.remove();
          return [...document.querySelectorAll(".bal-part.soon b, .abal .abal-exp.soon")].map((b) => getComputedStyle(b).color === a);
        });
        assert.deepEqual(await soonColor(), [true, true], "credit going within a week isn't amber");
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth + 1), true, "the Usage page scrolls sideways");

        // the panel's Balances: the same lines
        const panel = await context.newPage();
        panel.on("pageerror", (e) => errors.push(e.message));
        await panel.route("**/*", serve(lang, [], fresh()));
        await panel.goto("http://magpie.test/?mode=panel");
        await panel.waitForFunction(() => document.querySelectorAll("#panelQuota .pq-bp").length >= 3);
        assert.deepEqual(await panel.evaluate(() => [...document.querySelectorAll("#panelQuota .pq-bp")].map((r) => [r.querySelector("span").textContent, r.querySelector("b").textContent, r.classList.contains("soon")])),
          [[w.in30, "$20.00", false], [w.in3, "¥5.00", true], [w.today, "$1.00", true]]);
        await panel.close();

        // the key's pills in the provider's editor
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Relay" }).first().click();
        await page.locator(".editor .accts .acc .abal").first().waitFor({ state: "attached" });
        await page.waitForTimeout(300);
        const row = (id) => page.locator(`.editor .accts .acc[data-account-id="${id}"]`);
        const pill = (id) => row(id).locator(".abal");
        const box = (id) => row(id).locator("input.abal-in");
        const menu = page.locator(".proto-menu");
        const item = (name) => menu.locator(".pm-item", { has: page.locator(".pm-name", { hasText: new RegExp(`^${name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`) }) });
        const open = async (id) => {
          if (await menu.isVisible()) await page.keyboard.press("Escape");
          await page.waitForTimeout(150);
          await row(id).hover();
          await pill(id).click();
          await menu.waitFor();
        };
        const posted = async (n) => {
          for (let i = 0; i < 60 && posts.length === n; i++) await new Promise((r) => setTimeout(r, 50));
          assert(posts.length > n, "nothing posted");
          return posts.at(-1);
        };
        const over = () => page.evaluate(() => [...document.querySelectorAll(".editor .accts .acc")].filter((e) => e.getBoundingClientRect().right > innerWidth + 1).length);

        assert.equal(await pill("k1").textContent(), w.k1);
        assert.equal(await pill("k2").textContent(), w.k2);
        assert.equal(await pill("k3").textContent(), w.k3);
        assert.deepEqual(await page.evaluate(() => [...document.querySelectorAll(".abal .abal-exp")].map((e) => e.classList.contains("soon"))), [false, true]);
        assert.deepEqual(await soonColor(), [true], "the pill's credit going within a week isn't amber");
        assert.match(await pill("k3").getAttribute("title"), new RegExp(w.gone));
        assert.equal(await page.locator("select").count(), 0, "no native select");
        assert.equal(await over(), 0, "a key's row past the window's edge");

        // k3: a sign of the other currency is refused, then a day gone by
        await open("k3");
        await item(w.exp).click();
        await box("k3").waitFor();
        let n = posts.length;
        await box("k3").fill("¥15");
        await box("k3").press("Enter");
        await page.waitForTimeout(300);
        assert.equal(posts.length, n, "an amount in the other currency was posted");
        assert.equal(await page.locator("#status").textContent(), w.badAmount);
        await page.waitForTimeout(300);
        await open("k3");
        await item(w.exp).click();
        await box("k3").waitFor();
        await box("k3").fill("15");
        await box("k3").press("Enter");
        const day = row("k3").locator("input.abal-day");
        await day.waitFor();
        assert.equal(await day.evaluate((e) => e === document.activeElement), true, "the day's field hasn't the focus");
        assert.equal(await over(), 0, "the day's row past the window's edge");
        await day.fill(dayIn(-1));
        await day.press("Enter");
        await page.waitForTimeout(300);
        assert.equal(posts.length, n, "a day gone by was posted");
        assert.equal(await page.locator("#status").textContent(), w.badDay);
        await page.waitForTimeout(300);
        await open("k3");
        await item(w.exp).click();
        await box("k3").waitFor();
        await box("k3").fill("15");
        await box("k3").press("Enter");
        await day.waitFor();
        await day.fill(dayIn(10).replace(/-/g, "/"));
        await day.press("Enter");
        let p = await posted(n);
        assert.equal(p.action, "keyexpiring");
        assert.deepEqual(p.body, { id: "relay", account: "k3", expiring: { amount: 15, until: dayIn(10) } });
        await page.waitForTimeout(250);
        assert.equal(await pill("k3").textContent(), w.k3new);
        assert.equal(await over(), 0, "a key's row past the window's edge");

        // k1's taken off
        await open("k1");
        n = posts.length;
        await item(w.noexp).click();
        p = await posted(n);
        assert.deepEqual(p.body, { id: "relay", account: "k1", expiring: null });
        await page.waitForTimeout(250);
        assert.equal(await pill("k1").textContent(), w.k1off);

        const stripes = await page.evaluate(() => [...document.querySelectorAll(".abal, .abal-in, .bal-part, .pq-bp")].map((e) => getComputedStyle(e)).filter((s) => parseFloat(s.borderLeftWidth) > parseFloat(s.borderRightWidth) || (parseFloat(s.borderLeftWidth) > 0 && s.borderLeftColor !== s.borderRightColor)).length);
        assert.equal(stripes, 0, "no border stripes");
        assert.deepEqual(errors, []);
      });
    }
  }
}
