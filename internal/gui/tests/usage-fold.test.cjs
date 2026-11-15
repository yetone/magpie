// Run with Node's test runner and Playwright on the module path; see README.md.
// A subscription with several accounts shows one of them in full on the
// Usage page — the one that answered last, else the first — and the others
// in brief, their bars without when they reset (whqtian on Discord: 只显示
// 一个账号即可，其他的可以点击展开; ARNO: 以前每个账号所占的空间很小，基本
// 能一页看到所有账号… 关了magpie后又得一个个展开); the tray panel leaves the
// ones in brief out, behind "Show N more accounts". Each account opens or
// folds on its own, and stays so across a reload, by provider and account;
// the card's button opens the rest; a provider with one account has no
// button; the clicks don't move the page, and Hide accounts still hides
// every one. English and Chinese; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const ago = (m) => new Date(Date.now() - m * 60e3).toISOString();
const later = new Date(Date.now() + 5 * 3600e3).toISOString();
const wb = (user, used, served) => ({ provider: "workbuddy", name: "WorkBuddy", kind: "subscription", icon: "workbuddy-color", plan: "Pro", user, windows: [{ name: "Credits", used, display: `${used * 56} / 5600`, resetsAt: later }], ...(served ? { lastServedAt: served } : {}) });
const quotas = () => [
  wb("alpha@example.com", 10),
  wb("bravo@example.com", 20, ago(30)),
  wb("charlie@example.com", 30, ago(2)), // answered last: the one in sight
  wb("delta@example.com", 40),
  { provider: "zcode", name: "ZCode", kind: "subscription", icon: "zcode", plan: "Start", user: "solo@example.com", windows: [{ name: "5 hours", used: 10 }] },
  { provider: "codex", name: "Codex", kind: "subscription", icon: "openai", plan: "Plus", user: "one@example.com", windows: [{ name: "5 hours", used: 20 }] },
  { provider: "codex", name: "Codex", kind: "subscription", icon: "openai", plan: "Plus", user: "two@example.com", windows: [{ name: "5 hours", used: 50 }] },
];

function serve(lang, panel, list = quotas) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${!panel}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(list());
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { more3: "Show 3 more accounts", more1: "Show 1 more account", fewer: "Show fewer accounts", full3: "Show 3 more accounts in full", full1: "Show 1 more account in full", brief: "Show the other accounts in brief", hide: "Hide accounts" },
  zh: { more3: "展开其余 3 个账号", more1: "展开其余 1 个账号", fewer: "收起其余账号", full3: "展开其余 3 个账号", full1: "展开其余 1 个账号", brief: "收起其余账号", hide: "账号打码" },
};

// what each card shows: its name, the accounts in full and in brief (in
// sight both), the meters and the reset lines in sight, its button
const cards = (page) => page.evaluate(() => [...document.querySelectorAll("#subscriptionUsage > .subscription-card")].map((c) => {
  const shown = [...c.querySelectorAll(".subscription-account")].filter((a) => a.offsetParent);
  return {
    name: c.querySelector(".subscription-head b").textContent,
    full: shown.filter((a) => !a.classList.contains("brief")).map((a) => a.querySelector(".user").title),
    brief: shown.filter((a) => a.classList.contains("brief")).map((a) => a.querySelector(".user").title),
    meters: [...c.querySelectorAll(".quota-windows")].filter((a) => a.offsetParent).length,
    resets: [...c.querySelectorAll(".quota-reset")].filter((a) => a.offsetParent).length,
    more: c.querySelector(".quota-more")?.textContent ?? null,
  };
}));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: several accounts: one in full, the others in brief, each remembered`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const context = await browser.newContext({ viewport: { width: 900, height: 300 }, reducedMotion: "reduce" });
      const errors = [];
      const open = async (url) => {
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, url.includes("mode=panel")));
        await page.goto(url);
        return page;
      };
      // a new context's storage is empty: nothing to clear and reload. A
      // reload at once lands while the page's first reads are on their way,
      // and WebKit reports each one it cancels as a page error, "Fetch API
      // cannot load … due to access control checks" (#1350)
      const page = await open("http://magpie.test/?view=usage");
      await page.waitForSelector(".subscription-account .user", { state: "attached" });

      let got = await cards(page);
      const by = (name) => got.find((c) => c.name === name);
      // every account in sight: the one that answered last in full, the
      // others' bars alone
      assert.deepEqual(by("WorkBuddy"), { name: "WorkBuddy", full: ["charlie@example.com"], brief: ["alpha@example.com", "bravo@example.com", "delta@example.com"], meters: 4, resets: 1, more: w.full3 });
      assert.deepEqual(by("Codex"), { name: "Codex", full: ["one@example.com"], brief: ["two@example.com"], meters: 2, resets: 0, more: w.full1 });
      assert.equal(by("ZCode").more, null, "one account: no button");
      assert.deepEqual([by("ZCode").full, by("ZCode").brief], [["solo@example.com"], []]);
      assert.equal(await page.locator("#subscriptionUsage > [data-key=zcode] .subscription-account .quota-acct-fold").count(), 0, "one account: no fold on its row");
      assert.equal(await page.locator("#subscriptionUsage > [data-key=zcode] .subscription-head > .quota-acct-fold").count(), 1, "one account: the card's fold in its head");

      // opening it doesn't move the page
      const view = page.locator("#view-usage");
      await page.mouse.move(450, 200);
      await page.mouse.wheel(0, 60);
      await page.waitForFunction(() => document.querySelector("#view-usage").scrollTop > 0);
      await page.waitForTimeout(300);
      const head = page.locator("#subscriptionUsage > [data-key=workbuddy] .subscription-head");
      const before = await view.evaluate((v) => v.scrollTop);
      assert.ok(before > 0, "the view was scrolled");
      const headAt = await head.evaluate((e) => e.getBoundingClientRect().top);
      await page.locator("#subscriptionUsage > [data-key=workbuddy] .quota-more").click();
      await page.waitForTimeout(700);
      assert.equal(await view.evaluate((v) => v.scrollTop), before, "the view didn't scroll");
      assert.equal(await head.evaluate((e) => e.getBoundingClientRect().top), headAt, "the card stays where it was");
      got = await cards(page);
      assert.deepEqual(by("WorkBuddy"), { name: "WorkBuddy", full: ["alpha@example.com", "bravo@example.com", "charlie@example.com", "delta@example.com"], brief: [], meters: 4, resets: 4, more: w.brief });
      assert.equal(by("Codex").more, w.full1, "another provider stays as it was");

      // an account opens and folds on its own, where it is
      const acct = (key, user) => page.locator(`#subscriptionUsage > [data-key=${key}] .subscription-account`, { hasText: user });
      const two = acct("codex", "two@example.com");
      // wheeled to, as a reader would (the page holds still for a script's
      // scroll): packed into columns (#860), Codex's card is under ZCode's,
      // past this short window's foot. It stops above Scroll to top's
      // floater, which shows once the page scrolls (d5dc8eecc), as a reader
      // clicks what isn't under it (it caught WebKit's click, #6003)
      const wheelTo = async (l) => {
        for (let i = 0; i < 20; i++) {
          const y = (await l.boundingBox()).y;
          const foot = await page.evaluate(() => document.querySelector(".view-ends.on")?.getBoundingClientRect().top ?? innerHeight);
          if (y > 60 && y + 40 < foot) break;
          await page.mouse.wheel(0, y > 60 ? 60 : -60);
          await page.waitForTimeout(150);
        }
        await page.waitForTimeout(300);
      };
      await wheelTo(two.locator(".quota-acct-fold"));
      const twoAt = await two.evaluate((e) => e.getBoundingClientRect().top);
      const scrolled = await view.evaluate((v) => v.scrollTop);
      assert.equal(await two.locator(".quota-acct-fold").getAttribute("aria-expanded"), "false");
      await two.locator(".quota-acct-fold").click();
      await page.waitForTimeout(400);
      assert.equal(await view.evaluate((v) => v.scrollTop), scrolled, "the view didn't scroll");
      assert.equal(await two.evaluate((e) => e.getBoundingClientRect().top), twoAt, "the account stays where it was");
      got = await cards(page);
      assert.deepEqual([by("Codex").full, by("Codex").brief, by("Codex").more], [["one@example.com", "two@example.com"], [], w.brief]);
      await wheelTo(acct("workbuddy", "alpha@example.com").locator(".quota-acct-fold"));
      await acct("workbuddy", "alpha@example.com").locator(".quota-acct-fold").click();
      got = await cards(page);
      assert.deepEqual(by("WorkBuddy").brief, ["alpha@example.com"]);
      assert.equal(by("WorkBuddy").more, w.full1);

      // remembered across a reload (a restart), by provider and account
      await page.reload();
      await page.waitForSelector(".subscription-account .user", { state: "attached" });
      got = await cards(page);
      assert.deepEqual([by("WorkBuddy").full, by("WorkBuddy").brief], [["bravo@example.com", "charlie@example.com", "delta@example.com"], ["alpha@example.com"]]);
      assert.deepEqual([by("Codex").full, by("Codex").brief], [["one@example.com", "two@example.com"], []]);
      // the card's button opens the rest
      await page.locator("#subscriptionUsage > [data-key=workbuddy] .quota-more").click();
      got = await cards(page);
      assert.equal(by("WorkBuddy").full.length, 4);

      // Hide accounts still hides every account, the folded ones too
      await page.locator("#usageMask").click();
      await page.waitForFunction(() => document.querySelectorAll("#subscriptionUsage .subscription-account .user .pii").length === 7);
      assert.ok(!/example\.com/.test(await page.locator("#subscriptionUsage").innerText()));
      await page.locator("#usageMask").click();

      // the tray panel folds the same way, and follows what was opened
      const panel = await open("http://magpie.test/?mode=panel");
      await panel.setViewportSize({ width: 380, height: 900 });
      await panel.evaluate(() => setPanelTab("usage"));
      await panel.waitForSelector("#panelQuota .pq-card .pq-user");
      const pq = () => panel.evaluate(() => Object.fromEntries([...document.querySelectorAll("#panelQuota .pq-group")].map((g) => [g.querySelector(".pq-gn").textContent, { users: [...g.querySelectorAll(".pq-user")].map((u) => u.textContent), more: g.querySelector(".pq-more")?.textContent ?? null }])));
      let p = await pq();
      assert.deepEqual(p.WorkBuddy, { users: ["alpha@example.com", "bravo@example.com", "charlie@example.com", "delta@example.com"], more: w.fewer });
      assert.deepEqual(p.Codex, { users: ["one@example.com", "two@example.com"], more: w.fewer });
      assert.equal(p.ZCode.more, null);
      await panel.locator("#panelQuota .pq-group", { hasText: "WorkBuddy" }).locator(".pq-more").click();
      p = await pq();
      assert.deepEqual(p.WorkBuddy, { users: ["charlie@example.com"], more: w.more3 });
      // and the window follows the panel
      await page.waitForFunction(() => document.querySelector("#subscriptionUsage > [data-key=workbuddy] .quota-more")?.getAttribute("aria-expanded") === "false");
      got = await cards(page);
      assert.deepEqual([by("WorkBuddy").full, by("WorkBuddy").brief], [["charlie@example.com"], ["alpha@example.com", "bravo@example.com", "delta@example.com"]]);
      assert.deepEqual(errors, []);
    });
  }

  // WuSix on Discord (#help): three accounts, one deleted, and the two
  // left were both in brief. The button said 展开其余 2 个账号 though no
  // account was in full, opened both, and then 收起其余账号 folded only the
  // second, leaving 展开其余 1 个账号. The count names the accounts the
  // button opens, its fold puts back in brief the ones it opened, and an
  // account's own fold still works on its own. At a narrow width.
  for (const lang of ["en", "zh"]) {
    const w = {
      en: { all2: "Show all 2 accounts in full", more2: "Show 2 more accounts in full", more1: "Show 1 more account in full", every: "Show every account in brief", other: "Show the other accounts in brief" },
      zh: { all2: "展开全部 2 个账号", more2: "展开其余 2 个账号", more1: "展开其余 1 个账号", every: "收起全部账号", other: "收起其余账号" },
    }[lang];
    test(`${engine} ${lang}: an account deleted, the button opens and folds the ones it names`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 420, height: 800 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      let list = [wb("alpha@example.com", 10), wb("bravo@example.com", 20, ago(30)), wb("charlie@example.com", 30, ago(2))];
      await page.route("**/*", serve(lang, false, () => list));
      const card = async () => (await cards(page)).find((c) => c.name === "WorkBuddy");
      const more = page.locator("#subscriptionUsage > [data-key=workbuddy] .quota-more");
      const settle = (text) => page.waitForFunction((text) => document.querySelector("#subscriptionUsage > [data-key=workbuddy] .quota-more")?.textContent === text, text);
      await page.goto("http://magpie.test/?view=usage");
      await page.waitForSelector(".subscription-account .user", { state: "attached" });

      // three accounts: the one that answered last in full; opened and
      // folded again, the card is as it was
      assert.deepEqual([(await card()).full, (await card()).more], [["charlie@example.com"], w.more2]);
      await more.click();
      await settle(w.other);
      assert.equal((await card()).full.length, 3);
      await more.click();
      await settle(w.more2);
      assert.deepEqual([(await card()).full, (await card()).brief], [["charlie@example.com"], ["alpha@example.com", "bravo@example.com"]]);

      // charlie deleted: the two left both in brief, and the button says so
      list = list.filter((q) => q.user !== "charlie@example.com");
      await page.reload();
      await page.waitForSelector(".subscription-account .user", { state: "attached" });
      let c = await card();
      assert.deepEqual([c.full, c.brief, c.more], [[], ["alpha@example.com", "bravo@example.com"], w.all2]);
      // it opens both
      await more.click();
      await settle(w.every);
      c = await card();
      assert.deepEqual([c.full, c.brief], [["alpha@example.com", "bravo@example.com"], []]);
      assert.equal(await more.getAttribute("aria-expanded"), "true");
      // and folds both
      await more.click();
      await settle(w.all2);
      c = await card();
      assert.deepEqual([c.full, c.brief], [[], ["alpha@example.com", "bravo@example.com"]]);
      assert.equal(await more.getAttribute("aria-expanded"), "false");

      // an account's own fold still works on its own
      const fold = (user) => page.locator("#subscriptionUsage > [data-key=workbuddy] .subscription-account", { hasText: user }).locator(".quota-acct-fold");
      await fold("alpha@example.com").click();
      await settle(w.more1);
      assert.deepEqual((await card()).full, ["alpha@example.com"]);
      // the button opens bravo, and folds bravo alone
      await more.click();
      await settle(w.other);
      await more.click();
      await settle(w.more1);
      assert.deepEqual([(await card()).full, (await card()).brief], [["alpha@example.com"], ["bravo@example.com"]]);
      await fold("alpha@example.com").click();
      await settle(w.all2);
      assert.deepEqual((await card()).full, []);
      assert.deepEqual(errors, []);
    });
  }

  // ARNO's Usage page: 17 subscriptions, 29 accounts. With Trends off, at
  // 1280×800, every account is on the page and most of their bars are on
  // the first screen without opening anything, as many as before the curves.
  test(`${engine}: 17 subscriptions and 29 accounts, most on one screen`, async (t) => {
    const later = (h) => new Date(Date.now() + h * 3600e3).toISOString();
    const two = (a, b) => [{ name: "5 hours", used: a, resetsAt: later(2) }, { name: "Weekly", used: b, resetsAt: later(72) }];
    const one = (name, used, h = 288) => [{ name, used, resetsAt: later(h) }];
    const sub = (provider, name, users, windows) => users.map((user, i) => ({ provider, name, kind: "subscription", icon: "generic", plan: "Pro", ...(user ? { user } : {}), windows: windows(i) }));
    const ag = (i) => ["Gemini 3.8 Flash", "Gemini 3.7 Pro", "Claude Sonnet 4.6", "Claude Opus 4.6 Thinking", "GPT-OSS 120B"].map((name, j) => ({ name, used: 10 * (i + j), resetsAt: later(j < 2 ? 2 : 72) }));
    const many = () => [
      ...sub("claude", "Claude", ["a@c.test", "b@c.test", "c@c.test", "d@c.test"], (i) => i ? two(5 * i, 20) : [...two(62, 41), ...two(77, 12).map((w) => ({ ...w, name: w.name + " · Opus" }))]),
      ...sub("codex", "Codex", ["a@x.test", "b@x.test", "c@x.test", "d@x.test"], (i) => two(10 * i, 30)),
      ...sub("antigravity", "Antigravity", ["g1@g.test", "g2@g.test"], ag),
      ...sub("gemini", "Gemini CLI", ["h1@g.test", "h2@g.test"], () => one("Daily", 14, 2)),
      ...sub("kiro", "Kiro", ["k1@k.test", "k2@k.test"], () => one("Monthly", 44)),
      ...sub("workbuddy", "WorkBuddy", ["w1@w.test", "w2@w.test", "w3@w.test"], () => one("Credits", 20)),
      ...sub("qoder", "Qoder", ["q1@q.test", "q2@q.test"], () => one("Credits", 25)),
      ...[["cursor", "Cursor"], ["zcode", "ZCode"], ["copilot", "GitHub Copilot"], ["devin", "Devin"], ["factory", "Factory"], ["grok", "Grok"], ["mimo-app", "Xiaomi MiMo"]]
        .flatMap(([id, name]) => sub(id, name, [`${id}@solo.test`], () => one("Monthly", 30))),
      ...sub("glm", "GLM Coding", [""], () => two(22, 3)),
      ...sub("kimi", "Kimi", [""], () => two(10, 30)),
      ...sub("minimax", "MiniMax", [""], () => one("5 hours", 5, 2)),
    ];
    assert.equal(many().length, 29);
    assert.equal(new Set(many().map((q) => q.provider)).size, 17);
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 1280, height: 800 }, reducedMotion: "reduce" })).newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    await page.route("**/*", serve("zh", false, many));
    await page.addInitScript(() => { try { if (!sessionStorage.getItem("set")) { localStorage.clear(); localStorage.setItem("magpie.quotaRange", "off"); sessionStorage.setItem("set", "1"); } } catch {} });
    await page.goto("http://magpie.test/?view=usage");
    await page.waitForSelector(".subscription-account .user");
    const seen = () => page.evaluate(() => {
      const box = document.querySelector("#subscriptionUsage");
      // an account's bars: what follows its row; a card without accounts: its bars
      const bars = [...box.querySelectorAll(".subscription-account")].map((a) => a.nextElementSibling);
      for (const c of box.children) if (!c.querySelector(".subscription-account")) bars.push(c.querySelector(".quota-windows"));
      return { all: bars.length, shown: bars.filter((b) => b.offsetParent).length, onScreen: bars.filter((b) => b.offsetParent && b.getBoundingClientRect().bottom <= innerHeight).length };
    });
    const s = await seen();
    assert.equal(s.all, 29);
    assert.equal(s.shown, 29, "every account's bars are on the page, none behind a button");
    // before the curves (9ccabdfb^) 15 were; v0.1.752 showed 12, and 12 more only behind buttons
    assert.ok(s.onScreen >= 15, `as many accounts on the first screen as before the curves: ${s.onScreen} of 29`);
  });
}
