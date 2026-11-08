// Run with Node's test runner and Playwright on the module path; see README.md.
// A key's balance typed by hand may have credit on top that expires, in
// grants (tenacious_eagle_96548 on Discord: money that stays, and
// promotional credit twice, one gone tomorrow and one the day after).
// The key's pill says how much of each is left and in how many days it
// goes, amber from a week; the Usage page's card and the panel's Balances
// say each on a line under the balance, amber the same way. From the
// pill's menu (the app's, no native select), Add credit that expires…
// opens one small form with the amount, in the balance's currency, and
// the last day together, the day a month ahead to begin with: Add or
// Enter posts provider/keyexpiring, a sign of the other currency or a day
// gone by is refused with the form kept, and Escape leaves it with
// nothing posted. Each grant is in the menu as Remove {amount}, with when
// it goes (amber within a week), and posts its removal by when it was
// typed; No credit that expires takes every one off, offered with two or
// more, or credit that has expired, which the pill's title says no longer
// counts. In every language, at a phone's width the rows and the form
// stay inside the window.
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
const grant = (set, left, n, at = ago) => ({ set, at, until: dayIn(n), left, ends: ends(n) });
const fresh = () => ({
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" },
  takesOwnBalance: true,
  keyList: [
    { id: "k1", name: "Main", masked: "sk-…one", on: true, balance: { set: 70, at: ago, spent: 10, left: 80, expiring: [grant(30, 20, 30)] } },
    // the reporter's: two grants, tomorrow's and the day after's
    { id: "k2", name: "Team", masked: "sk-…two", on: true, balance: { set: 40, at: ago, spent: 0, left: 53, currency: "cny", rate: 7.2,
      expiring: [grant(5, 5, 1, new Date(Date.now() - 2 * 864e5).toISOString()), grant(8, 8, 2)] } },
    { id: "k3", name: "Spare", masked: "sk-…three", on: true, balance: { set: 10, at: ago, spent: 0, left: 10, expired: ends(-1) } },
  ],
  balanceToken: { takes: false, set: false }, proxy: "",
});

const quotas = [
  { provider: "relay", name: "Relay", icon: "generic", user: "Main", windows: [], balance: "$80.00", balanceSplit: [{ text: "$20.00", expires: ends(30) }] },
  { provider: "relay", name: "Relay", icon: "generic", user: "Team", windows: [], balance: "¥53.00", balanceSplit: [{ text: "¥5.00", expires: ends(1) }, { text: "¥8.00", expires: ends(2) }] },
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
    if (url.pathname === "/api/state") posts.state = (posts.state || 0) + 1;
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
      posts.push({ action, body, state: posts.state || 0 });
      if (action === "keyexpiring") {
        // as the server: a grant added, one taken off by when it was
        // typed, or every one; the soonest to go listed first
        const b = relay.keyList.find((x) => x.id === body.account).balance, xs = b.expiring || [];
        delete b.expired;
        if (!body.expiring) { for (const x of xs) b.left -= x.left; b.expiring = []; }
        else if (body.remove) {
          const x = xs.find((x) => x.at === body.expiring.at);
          b.left -= x.left;
          b.expiring = xs.filter((y) => y !== x);
        } else {
          const [y, m, d] = body.expiring.until.split("-").map(Number);
          b.left += body.expiring.amount;
          b.expiring = [...xs, { set: body.expiring.amount, at: new Date().toISOString(), until: body.expiring.until, left: body.expiring.amount, ends: new Date(y, m - 1, d + 1).toISOString() }]
            .sort((a, c) => new Date(a.ends) - new Date(c.ends));
        }
      }
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// each language's words, from its i18n templates
const lang = (o) => ({ ...o, left: (v) => o.left.replace("{usd}", v), expIn: (a, n) => o.expIn.replace("{amount}", a).replace("{n}", n),
  expTomorrow: (a) => o.expTomorrow.replace("{amount}", a), inN: (n) => o.inN.replace("{n}", n), remove: (a) => o.remove.replace("{amount}", a) });
const words = {
  en: lang({ left: "≈{usd} left", expIn: "{amount} expires in {n} days", expTomorrow: "{amount} expires tomorrow", inN: "Expires in {n} days", tomorrow: "Expires tomorrow",
    today: "Expires today", exp: "Add credit that expires…", head: "Add credit that expires", add: "Add", remove: "Remove {amount}", noexp: "No credit that expires",
    badAmount: "Credit that expires is an amount above 0, in the balance's currency", badDay: "The last day is a date from today on, as 2026-12-31", gone: "no longer counts" }),
  zh: lang({ left: "≈{usd} 剩余", expIn: "{amount} {n} 天后到期", expTomorrow: "{amount} 明天到期", inN: "{n} 天后到期", tomorrow: "明天到期",
    today: "今天到期", exp: "添加限期额度…", head: "添加限期额度", add: "添加", remove: "去掉 {amount}", noexp: "去掉限期额度",
    badAmount: "限期额度是大于 0 的金额，用余额的货币", badDay: "最后一天是今天或以后的日期，如 2026-12-31", gone: "已不再计入" }),
  "zh-TW": lang({ left: "≈{usd} 剩餘", expIn: "{amount} {n} 天後到期", expTomorrow: "{amount} 明天到期", inN: "{n} 天後到期", tomorrow: "明天到期",
    today: "今天到期", exp: "新增限期額度…", head: "新增限期額度", add: "新增", remove: "移除 {amount}", noexp: "去掉限期額度",
    badAmount: "限期額度是大於 0 的金額，用餘額的貨幣", badDay: "最後一天是今天或以後的日期，如 2026-12-31", gone: "已不再計入" }),
  ja: lang({ left: "残り ≈{usd}", expIn: "{amount} は {n} 日後に失効", expTomorrow: "{amount} は明日で失効", inN: "{n} 日後に失効", tomorrow: "明日で失効",
    today: "今日で失効", exp: "期限付きクレジットを追加…", head: "期限付きクレジットを追加", add: "追加", remove: "{amount} を外す", noexp: "期限付きクレジットなし",
    badAmount: "期限付きクレジットは 0 より大きい金額です（残高の通貨で）", badDay: "最終日は今日以降の日付です（例: 2026-12-31）", gone: "もう数えません" }),
  de: lang({ left: "≈{usd} übrig", expIn: "{amount} läuft in {n} Tagen ab", expTomorrow: "{amount} läuft morgen ab", inN: "Läuft in {n} Tagen ab", tomorrow: "Läuft morgen ab",
    today: "Läuft heute ab", exp: "Ablaufendes Guthaben hinzufügen…", head: "Ablaufendes Guthaben hinzufügen", add: "Hinzufügen", remove: "{amount} entfernen", noexp: "Kein ablaufendes Guthaben",
    badAmount: "Ablaufendes Guthaben ist ein Betrag über 0, in der Währung des Guthabens", badDay: "Der letzte Tag ist ein Datum ab heute, etwa 2026-12-31", gone: "zählt nicht mehr" }),
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [900, 400]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: a key's grants of credit that expires, each said in days, added in one form and taken off one by one`, async (t) => {
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

        // the Usage page's cards: a line under the balance for each grant,
        // amber within a week
        await page.goto("http://magpie.test/?view=usage&tab=usage");
        await page.waitForFunction(() => document.querySelectorAll("#subscriptionUsage .bal-part").length >= 7);
        const lines = () => page.evaluate(() => [...document.querySelectorAll("#subscriptionUsage .quota-balance")].map((b) =>
          [...b.querySelectorAll(".bal-part:not(.lead)")].map((r) => [r.querySelector("span").textContent, r.querySelector("b").textContent, r.classList.contains("soon")])));
        assert.deepEqual(await lines(), [[[w.inN(30), "$20.00", false]], [[w.tomorrow, "¥5.00", true], [w.inN(2), "¥8.00", true]], [[w.today, "$1.00", true]]]);
        const amber = (sel) => page.evaluate((sel) => {
          const e = document.createElement("i");
          e.style.color = "var(--amber)";
          document.body.append(e);
          const a = getComputedStyle(e).color;
          e.remove();
          return [...document.querySelectorAll(sel)].map((b) => getComputedStyle(b).color === a);
        }, sel);
        assert.deepEqual(await amber(".bal-part.soon b"), [true, true, true], "credit going within a week isn't amber");
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth + 1), true, "the Usage page scrolls sideways");

        // the panel's Balances: the same lines
        const panel = await context.newPage();
        panel.on("pageerror", (e) => errors.push(e.message));
        await panel.route("**/*", serve(lang, [], fresh()));
        await panel.goto("http://magpie.test/?mode=panel");
        await panel.waitForFunction(() => document.querySelectorAll("#panelQuota .pq-bp").length >= 4);
        assert.deepEqual(await panel.evaluate(() => [...document.querySelectorAll("#panelQuota .pq-bp")].map((r) => [r.querySelector("span").textContent, r.querySelector("b").textContent, r.classList.contains("soon")])),
          [[w.inN(30), "$20.00", false], [w.tomorrow, "¥5.00", true], [w.inN(2), "¥8.00", true], [w.today, "$1.00", true]]);
        await panel.close();

        // the key's pills in the provider's editor
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Relay" }).first().click();
        await page.locator(".editor .accts .acc .abal").first().waitFor({ state: "attached" });
        await page.waitForLoadState("networkidle");
        const row = (id) => page.locator(`.editor .accts .acc[data-account-id="${id}"]`);
        const pill = (id) => row(id).locator(".abal");
        const menu = page.locator(".proto-menu");
        const form = page.locator(".abal-form");
        const amount = form.locator("input.abal-f-n"), day = form.locator("input.abal-f-d"), add = form.locator("button");
        const item = (name) => menu.locator(".pm-item", { has: page.locator(".pm-name", { hasText: new RegExp(`^${name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`) }) });
        const open = async (id) => {
          if (await menu.isVisible() || await form.isVisible()) await page.keyboard.press("Escape");
          await page.waitForTimeout(150);
          await row(id).hover();
          await pill(id).click();
          await menu.waitFor();
        };
        const posted = async (n) => {
          for (let i = 0; i < 60 && posts.length === n; i++) await new Promise((r) => setTimeout(r, 50));
          assert(posts.length > n, "nothing posted");
          // the save is done once the app has read its state again, which
          // it does after drawing the providers' rows
          for (let i = 0; i < 60 && (posts.state || 0) <= posts.at(-1).state; i++) await new Promise((r) => setTimeout(r, 50));
          await page.waitForTimeout(200);
          return posts.at(-1);
        };
        // a row, or anything in it, past the list's edge or the window's,
        // or the form past the window's
        const over = () => page.evaluate(() => {
          const list = document.querySelector(".editor .accts").getBoundingClientRect(), edge = Math.min(innerWidth, list.right);
          return [...document.querySelectorAll(".editor .accts .acc, .editor .accts .acc *")].filter((e) => e.getBoundingClientRect().right > edge + 1).map((e) => e.className)
            .concat([...document.querySelectorAll(".abal-form")].filter((e) => e.getBoundingClientRect().right > innerWidth + 1 || e.getBoundingClientRect().left < -1).map(() => "abal-form"));
        });

        assert.equal(await pill("k1").textContent(), w.left("$80.00") + " · " + w.expIn("$20.00", 30));
        assert.equal(await pill("k2").textContent(), w.left("¥53.00") + " · " + w.expTomorrow("¥5.00") + " · " + w.expIn("¥8.00", 2));
        assert.equal(await pill("k3").textContent(), w.left("$10.00"));
        assert.deepEqual(await page.evaluate(() => [...document.querySelectorAll(".abal .abal-exp")].map((e) => e.classList.contains("soon"))), [false, true, true]);
        assert.deepEqual(await amber(".abal .abal-exp.soon"), [true, true], "the pill's credit going within a week isn't amber");
        assert.match(await pill("k3").getAttribute("title"), new RegExp(w.gone));
        assert.equal(await page.locator("select").count(), 0, "no native select");
        assert.deepEqual(await over(), [], "a key's row past the window's edge");

        // k2's menu: each grant to take off, when it goes said beside it
        await open("k2");
        assert.deepEqual(await menu.locator(".pm-item").evaluateAll((es) => es.map((e) => [e.querySelector(".pm-name").textContent, e.querySelector(".pm-note").textContent, e.querySelector(".pm-note").classList.contains("soon")]).slice(1, 5)),
          [[w.exp, "", false], [w.remove("¥5.00"), w.tomorrow, true], [w.remove("¥8.00"), w.inN(2), true], [w.noexp, "", false]]);
        assert.deepEqual(await amber(".proto-menu .pm-note.soon"), [true, true], "a grant going within a week isn't amber in the menu");

        // k3: the form, amount and day together, the day a month ahead
        await open("k3");
        assert.equal(await item(w.noexp).count(), 1, "credit that expired can't be taken off");
        await item(w.exp).click();
        await form.waitFor();
        assert.equal(await form.getAttribute("aria-label"), w.head);
        assert.equal(await add.textContent(), w.add);
        assert.equal(await amount.evaluate((e) => e === document.activeElement), true, "the amount hasn't the focus");
        assert.equal(await day.inputValue(), dayIn(30));
        assert.deepEqual(await over(), [], "the form past the window's edge");
        let n = posts.length;
        // a sign of the other currency: refused, the form kept
        await amount.fill("¥15");
        await add.click();
        await page.waitForTimeout(200);
        assert.equal(posts.length, n, "an amount in the other currency was posted");
        assert.equal(await page.locator("#status").textContent(), w.badAmount);
        assert.equal(await form.isVisible(), true, "a refused amount closed the form");
        assert.equal(await form.locator(".abal-f-m.bad").count(), 1, "the refused amount isn't marked");
        // a day gone by: refused, the form kept
        await amount.fill("15");
        await day.fill(dayIn(-1));
        await amount.press("Enter");
        await page.waitForTimeout(200);
        assert.equal(posts.length, n, "a day gone by was posted");
        assert.equal(await page.locator("#status").textContent(), w.badDay);
        assert.equal(await form.isVisible(), true, "a refused day closed the form");
        // Escape leaves it, nothing posted
        await page.keyboard.press("Escape");
        await form.waitFor({ state: "detached" });
        assert.equal(posts.length, n, "Escape posted");
        // Enter adds, with the day it began with
        await open("k3");
        await item(w.exp).click();
        await form.waitFor();
        await amount.fill("15");
        await amount.press("Enter");
        let p = await posted(n);
        assert.equal(p.action, "keyexpiring");
        assert.deepEqual(p.body, { id: "relay", account: "k3", expiring: { amount: 15, until: dayIn(30) } });
        await form.waitFor({ state: "detached" });
        assert.equal(await pill("k3").textContent(), w.left("$25.00") + " · " + w.expIn("$15.00", 30));
        // Add adds a second, sooner: listed first
        await open("k3");
        await item(w.exp).click();
        await form.waitFor();
        n = posts.length;
        await amount.fill("4");
        await day.fill(dayIn(10));
        await add.click();
        p = await posted(n);
        assert.deepEqual(p.body, { id: "relay", account: "k3", expiring: { amount: 4, until: dayIn(10) } });
        assert.equal(await pill("k3").textContent(), w.left("$29.00") + " · " + w.expIn("$4.00", 10) + " · " + w.expIn("$15.00", 30));
        assert.deepEqual(await over(), [], "a key's row with two grants past the window's edge");

        // k2's day after's taken off, by when it was typed: the second,
        // not the first in the list
        const k2at = relay.keyList[1].balance.expiring[1].at;
        await open("k2");
        n = posts.length;
        await item(w.remove("¥8.00")).click();
        p = await posted(n);
        assert.deepEqual(p.body, { id: "relay", account: "k2", expiring: { at: k2at, until: dayIn(2) }, remove: true });
        assert.equal(await pill("k2").textContent(), w.left("¥45.00") + " · " + w.expTomorrow("¥5.00"));

        // k1's one grant is taken off as itself; with one, no "every one"
        await open("k1");
        assert.equal(await item(w.noexp).count(), 0, "every one offered for one grant");
        n = posts.length;
        await item(w.remove("$30.00")).click();
        p = await posted(n);
        assert.equal(p.body.remove, true);
        assert.equal(await pill("k1").textContent(), w.left("$60.00"));

        // k3's two taken off at once
        await open("k3");
        n = posts.length;
        await item(w.noexp).click();
        p = await posted(n);
        assert.deepEqual(p.body, { id: "relay", account: "k3", expiring: null });
        assert.equal(await pill("k3").textContent(), w.left("$10.00"));

        const stripes = await page.evaluate(() => [...document.querySelectorAll(".abal, .abal-form, .abal-form *, .bal-part, .pq-bp")].map((e) => getComputedStyle(e)).filter((s) => parseFloat(s.borderLeftWidth) > parseFloat(s.borderRightWidth) || (parseFloat(s.borderLeftWidth) > 0 && s.borderLeftColor !== s.borderRightColor)).length);
        assert.equal(stripes, 0, "no border stripes");
        assert.deepEqual(errors, []);
      });
    }
  }
}
