// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider's accounts together (CryptoSarom on X: 有时候更关心某个模型还能
// 用多少). On the Usage page a card of several accounts has "All N accounts"
// under its head, each window's figure added up or averaged over one plan,
// how many couldn't be read, and each account as it was below it; the tray
// panel's group has the same strip above its accounts' cards, and a routing
// group's row a line a provider of its members. A card whose accounts have
// nothing in common (a Plus and a Pro Codex) and one of a single account
// have none. Used or left turns it with the rest. No account is named in
// it. At 440px nothing in it is cut or spills. English, Chinese (both),
// Japanese and German, light and dark; no backend, the API is faked here.
// SHOTS=<dir> keeps a screenshot of each place.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const iso = (ms) => new Date(ms).toISOString();
const credits = (user, amount, extra) => ({ provider: "workbuddy-ai", name: "WorkBuddy AI", kind: "subscription", icon: "generic", plan: "Pro", user, readAt: iso(now),
  windows: [{ name: "Credits", amount, limit: 500, unit: "credits", used: (100 * amount) / 500, resetsAt: iso(now + 9 * 24 * 3600e3) }], ...extra });
const quotas = () => [
  credits("wb-1@example.com", 100),
  credits("wb-2@example.com", 250),
  credits("wb-3@example.com", 5),
  { provider: "workbuddy-ai", name: "WorkBuddy AI", kind: "subscription", icon: "generic", user: "wb-4@example.com", error: "401 Unauthorized", windows: [] },
  { provider: "codex", name: "Codex", kind: "subscription", icon: "openai", plan: "Plus", user: "c1@example.com", windows: [{ name: "5 hours", used: 40, resetsAt: iso(now + 2 * 3600e3) }] },
  { provider: "codex", name: "Codex", kind: "subscription", icon: "openai", plan: "Pro", user: "c2@example.com", windows: [{ name: "5 hours", used: 10, resetsAt: iso(now + 2 * 3600e3) }] },
  // two on one plan: each window the mean of the two, side by side
  { provider: "claude", name: "Claude", kind: "subscription", icon: "claudecode-color", plan: "Max", user: "m1@example.com", windows: [{ name: "5 hours", used: 30, resetsAt: iso(now + 3600e3) }, { name: "Weekly", used: 50, resetsAt: iso(now + 4 * 24 * 3600e3) }] },
  { provider: "claude", name: "Claude", kind: "subscription", icon: "claudecode-color", plan: "Max", user: "m2@example.com", windows: [{ name: "5 hours", used: 70, resetsAt: iso(now + 2 * 3600e3) }, { name: "Weekly", used: 20, resetsAt: iso(now + 5 * 24 * 3600e3) }] },
  { provider: "kimi", name: "Kimi", kind: "subscription", icon: "kimi-color", windows: [{ name: "Weekly", used: 10 }] },
];
const models = [{ id: "workbuddy-ai/glm-5", name: "GLM-5", providerName: "WorkBuddy AI", provider: "workbuddy-ai", icon: "generic" }];
const groups = () => ({ models, pools: [], deciders: [], found: true, groups: [
  { id: "glm", name: "GLM", members: ["workbuddy-ai/glm-5"], ready: true, memberInfo: [{ id: "workbuddy-ai/glm-5", ready: true, provider: "workbuddy-ai", name: "WorkBuddy AI", icon: "generic", model: "glm-5" }] },
] });

function serve(lang, theme, tray, cards = quotas) {
  const settings = { theme, lang, tray, quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/usage/quotas") return json(cards());
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: iso(Date.now()), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { all: "All 4 accounts", unknown: "1 couldn't be read", used: "355 / 1,500 credits · 23.7% used", left: "1,145 / 1,500 credits · 76.3% left", line: "WorkBuddy AI, 4 accounts: Credits 355 / 1,500 credits · 23.7% used · 1 couldn't be read" },
  zh: { all: "全部 4 个账号", unknown: "1 个读取失败" },
  "zh-TW": { all: "全部 4 個帳號", unknown: "1 個讀取失敗" },
  ja: { all: "全 4 アカウント", unknown: "1 件は読み取れませんでした" },
  de: { all: "Alle 4 Konten", unknown: "1 nicht lesbar" },
};

// nothing in the strip cut short or past its box, nor the page sideways
const fits = (page, sel) => page.evaluate((sel) => {
  const bad = [];
  if (document.documentElement.scrollWidth > innerWidth) bad.push("page scrolls sideways");
  for (const box of document.querySelectorAll(sel)) {
    const b = box.getBoundingClientRect();
    if (box.scrollWidth > box.clientWidth + 0.5) bad.push("strip spills");
    for (const e of box.querySelectorAll("*")) {
      const r = e.getBoundingClientRect();
      if (!r.width) continue;
      if (r.left < b.left - 0.5 || r.right > b.right + 0.5) bad.push(`${e.className || e.tagName} past the strip`);
    }
  }
  return bad;
}, sel);

const shot = async (page, name) => {
  if (!process.env.SHOTS) return;
  await fs.mkdir(process.env.SHOTS, { recursive: true });
  await page.screenshot({ path: path.join(process.env.SHOTS, name + ".png") });
};

async function open(browser, lang, theme, tray, url, width = 440, height = 900, cards = quotas) {
  const context = await browser.newContext({ viewport: { width, height }, reducedMotion: "reduce", colorScheme: theme });
  const page = await context.newPage();
  page.setDefaultTimeout(8000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, theme, tray, cards));
  await page.addInitScript(() => { try { if (!sessionStorage.getItem("set")) { localStorage.clear(); localStorage.setItem("magpie.quotaRange", "off"); sessionStorage.setItem("set", "1"); } } catch {} });
  await page.goto(url);
  return { page, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    for (const theme of lang === "en" || lang === "zh" ? ["light", "dark"] : ["light"]) {
      test(`${engine} ${lang} ${theme}: a provider's accounts together on the Usage page, the panel and a routing group`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());

        // the Usage page
        const { page, errors } = await open(browser, lang, theme, "window", "http://magpie.test/?view=usage");
        const card = '#subscriptionUsage > [data-key="workbuddy-ai"]';
        await page.waitForSelector(`${card} .quota-all`);
        const strip = page.locator(`${card} .quota-all`);
        assert.equal(await strip.locator(".quota-all-head").textContent(), w.all);
        assert.equal(await strip.locator(".quota-all-unknown").textContent(), w.unknown);
        assert.equal(await strip.locator(".quota-all-w").count(), 1);
        const n = await strip.locator(".quota-all-n").textContent();
        assert.ok(n.includes("355 / 1,500"), n);
        if (w.used) assert.equal(n, w.used);
        // under the head, before the accounts, each still there
        assert.ok(await page.evaluate((card) => {
          const c = document.querySelector(card), s = c.querySelector(".quota-all");
          return s.previousElementSibling.matches(".subscription-head") && c.querySelectorAll(".subscription-account").length === 4;
        }, card));
        // no account named in it
        assert.ok(!(await strip.textContent()).includes("@example.com"));
        // nothing in common, or one account: no strip
        assert.equal(await page.locator('#subscriptionUsage > [data-key="codex"] .quota-all').count(), 0);
        assert.equal(await page.locator('#subscriptionUsage > [data-key="kimi"] .quota-all').count(), 0);
        // one plan's shares: the mean, each window apart, side by side
        const max = page.locator('#subscriptionUsage > [data-key="claude"] .quota-all .quota-all-n');
        assert.equal(await max.count(), 2);
        assert.ok((await max.nth(0).textContent()).includes("50%"));
        assert.ok((await max.nth(1).textContent()).includes("35%"));
        assert.ok(await page.evaluate(() => { const [a, b] = document.querySelectorAll('#subscriptionUsage > [data-key="claude"] .quota-all-w'); return Math.abs(a.getBoundingClientRect().top - b.getBoundingClientRect().top) < 1; }), "side by side");
        assert.deepEqual(await fits(page, ".quota-all"), []);
        await shot(page, `usage-${engine}-${lang}-${theme}`);
        // used or left turns it with the rest
        await page.locator("#quotaMode .opt").nth(1).click();
        await page.waitForFunction((card) => !document.querySelector(card + " .quota-all-n").textContent.startsWith("355"), card);
        const left = await strip.locator(".quota-all-n").textContent();
        assert.ok(left.includes("1,145 / 1,500"), left);
        if (w.left) assert.equal(left, w.left);
        await page.locator("#quotaMode .opt").nth(0).click();

        // a routing group's row
        await page.goto("http://magpie.test/?view=groups");
        const line = page.locator('.rt-groups > .rt-group[data-id="glm"] .rt-gquota');
        await line.waitFor();
        const said = await line.textContent();
        assert.ok(said.includes("WorkBuddy AI") && said.includes("355 / 1,500") && said.includes(w.unknown), said);
        if (w.line) assert.equal(said, w.line);
        assert.ok(!said.includes("@example.com"));
        if (process.env.SHOTS) { await line.evaluate((e) => e.scrollIntoView({ block: "center" })); await page.waitForTimeout(300); }
        await shot(page, `routing-${engine}-${lang}-${theme}`);
        assert.deepEqual(errors, []);

        // the tray panel
        const panel = await open(browser, lang, theme, "panel", "http://magpie.test/?mode=panel", 360, 760);
        await panel.page.locator('#ptabs [data-ptab="usage"]').click();
        const pq = panel.page.locator("#panelQuota .pq-group .pq-all");
        await pq.first().waitFor();
        assert.equal(await pq.count(), 2, "WorkBuddy AI's and Claude's: Codex's two plans have nothing in common");
        // WorkBuddy AI's: the one with an account that couldn't be read
        const wbp = pq.filter({ has: panel.page.locator(".quota-all-unknown") });
        assert.equal(await wbp.count(), 1);
        assert.equal(await wbp.locator(".quota-all-head").textContent(), w.all);
        assert.equal(await wbp.locator(".quota-all-unknown").textContent(), w.unknown);
        assert.ok((await wbp.locator(".quota-all-n").textContent()).includes("355 / 1,500"));
        // above the accounts' cards, which are as many as before
        assert.ok(await panel.page.evaluate(() => {
          const s = document.querySelector("#panelQuota .pq-all");
          return s.previousElementSibling.matches(".pq-gh") && s.nextElementSibling.matches(".pq-card");
        }));
        assert.deepEqual(await fits(panel.page, ".pq-all"), []);
        await shot(panel.page, `panel-${engine}-${lang}-${theme}`);
        assert.deepEqual(panel.errors, []);
      });
    }
  }
}

// #4791 (hayden4608 on Discord, handong66): three Codex accounts with 100%
// of 5 hours left and 0% of 7 days, one with 0% of 5 hours and 22% of 7
// days. The server marks the three's 5 hours held by their week
// (provider.WithHeldWindows): they can't be spent before it starts again.
// The strip read 75% of 5 hours left; it says 0% left, its tooltip says
// three are held, and the week is 5.5% left as before. Each account's own
// card still shows the vendor's 100% left.
const heldWords = {
  en: { held: "3 of them can't spend theirs before a longer window used up starts again: counted as none left", five: "0% left", week: "5.5% left" },
  zh: { held: "其中 3 个账号的更长周期已用完、重置前用不上这部分额度，按无剩余计" },
  "zh-TW": { held: "其中 3 個帳號的更長週期已用完、重設前用不上這部分額度，按無剩餘計" },
  ja: { held: "うち 3 件は、より長い枠を使い切っており、そのリセットまで使えないため残りなしとして計上" },
  de: { held: "3 davon können ihren Rest erst nach dem Neustart eines aufgebrauchten längeren Fensters nutzen: als leer gezählt" },
};
const pct = (s) => Number(/([\d.]+)%/.exec(s)?.[1]);
const heldCards = () => [["a", 0, 100, true], ["b", 0, 100, true], ["c", 0, 100, true], ["d", 100, 78, false]].map(([u, five, week, held]) => ({
  provider: "codex", name: "Codex", kind: "subscription", icon: "openai", plan: "Plus", user: u + "@example.com", readAt: iso(now),
  windows: [{ name: "5 hours", used: five, resetsAt: iso(now + 4 * 3600e3), ...(held ? { heldBy: "7 days" } : {}) }, { name: "7 days", used: week, resetsAt: iso(now + 3 * 24 * 3600e3) }],
}));
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(heldWords)) {
    test(`${engine} ${lang}: 5 hours an account can't spend before its week count as none left in all (#4791)`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const w = heldWords[lang];
      const check = async (page, strip) => {
        await strip.first().waitFor();
        const ws = strip.locator(".quota-all-w");
        assert.equal(await ws.count(), 2);
        const [five, week] = [ws.nth(0), ws.nth(1)];
        const fiveTip = await five.getAttribute("title");
        assert.ok(fiveTip.includes(w.held), fiveTip);
        assert.ok(!(await week.getAttribute("title")).includes(w.held));
        return [await five.locator(".quota-all-n").textContent(), await week.locator(".quota-all-n").textContent()];
      };
      // the Usage page, left
      const { page, errors } = await open(browser, lang, "light", "window", "http://magpie.test/?view=usage", 440, 900, heldCards);
      const strip = page.locator('#subscriptionUsage > [data-key="codex"] .quota-all');
      await strip.waitFor();
      await page.locator("#quotaMode .opt").nth(1).click();
      await page.waitForFunction(() => /left|剩|残|übrig/i.test(document.querySelector('#subscriptionUsage > [data-key="codex"] .quota-all .quota-all-n')?.textContent || ""));
      const [five, week] = await check(page, strip);
      assert.equal(pct(five), 0, "no 5 hours left in all, not 75%: " + five);
      assert.equal(pct(week), 5.5, week);
      if (w.five) assert.equal(five, w.five);
      if (w.week) assert.equal(week, w.week);
      assert.deepEqual(await fits(page, ".quota-all"), []);
      await shot(page, `held-usage-${engine}-${lang}`);
      assert.deepEqual(errors, []);
      // the tray panel
      const panel = await open(browser, lang, "light", "panel", "http://magpie.test/?mode=panel", 360, 760, heldCards);
      await panel.page.locator('#ptabs [data-ptab="usage"]').click();
      const pq = panel.page.locator("#panelQuota .pq-group .pq-all");
      const [pfive, pweek] = await check(panel.page, pq);
      // the panel says what is used, as its settings have it
      assert.equal(pct(pfive), 100, "every 5 hours used or held, not 25% used: " + pfive);
      assert.equal(pct(pweek), 94.5, pweek);
      assert.deepEqual(await fits(panel.page, ".pq-all"), []);
      await shot(panel.page, `held-panel-${engine}-${lang}`);
      assert.deepEqual(panel.errors, []);
    });
  }
}
