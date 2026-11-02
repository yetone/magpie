// Run with Node's test runner and Playwright on the module path; see README.md.
// Hide accounts hides every account, not only an email address (#568: a
// WorkBuddy account's nickname stayed in sight while 邮箱已打码): a name
// the page was told of — an allowance's user, a route's account — is
// blurred in its row, a sentence and a tooltip, on Usage and Routing; a
// card's own name ("ZCode" signed in with no name of its own, WorkBuddy)
// stays, as do magpie's own labels for a nameless account ("API key") and
// a name inside a longer word. The tray panel follows the
// window's setting, as it is turned. English and Chinese; no backend, the
// API is faked here. A one-letter name (Shinji_Zl's WorkBuddy account 他)
// is hidden too, where it stands apart, not the 他 of 其他.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const quotas = () => [
  { provider: "workbuddy", name: "WorkBuddy", icon: "workbuddy-color", plan: "Free", user: "李雷", windows: [{ name: "Credits", used: 8.9, display: "500.52 / 5600" }] },
  { provider: "workbuddy", name: "WorkBuddy", icon: "workbuddy-color", plan: "Free", user: "138****5678", windows: [{ name: "Credits", used: 1, display: "56 / 5600" }] },
  { provider: "workbuddy", name: "WorkBuddy", icon: "workbuddy-color", plan: "Free", user: "他", windows: [{ name: "Credits", used: 2, display: "112 / 5600" }] },
  { provider: "zcode", name: "ZCode", icon: "zcode", plan: "Start", user: "ZCode", windows: [{ name: "5 hours", used: 10 }] },
  { provider: "qoder", name: "Qoder", icon: "qoder", plan: "Pro", user: "Lee", windows: [{ name: "Credits", used: 3 }] },
  { provider: "codex", name: "Codex", icon: "openai", plan: "Plus", user: "dev.one@example.com", windows: [{ name: "5 hours", used: 20 }] },
  // a plugin's accounts with no name, as magpie labels them (pluginLabels)
  { provider: "kiro", name: "Kiro", icon: "kiro-color", plan: "Pro", user: "API key", windows: [{ name: "Credits", used: 2 }] },
  { provider: "kiro", name: "Kiro", icon: "kiro-color", plan: "Pro", user: "Signed in (2)", windows: [{ name: "Credits", used: 4 }] },
];

function serve(lang, panel) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${!panel}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(quotas());
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { hide: "Hide accounts", hidden: "Accounts hidden", tip: /^Mask the accounts/, back: /click to show them/ },
  zh: { hide: "账号打码", hidden: "账号已打码", tip: /^给本页的账号/, back: /点击显示/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: every account is hidden, not only an address`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const context = await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" });
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
      // a new context's storage is empty: no reload to clear it, which
      // would cancel the page's first reads, each a page error in WebKit (#1350)
      const page = await open("http://magpie.test/?view=usage");
      await page.waitForSelector(".subscription-account .user");
      const btn = page.locator("#usageMask");
      assert.equal((await btn.textContent()).trim(), w.hide);
      // the button has a tooltip, and once on it says how to have them back (#653)
      assert.match(await btn.getAttribute("title"), w.tip);
      // a sentence and a tooltip naming the accounts, and a word with one inside
      await page.evaluate(() => {
        const d = document.createElement("div");
        d.id = "sample";
        d.textContent = "李雷 checked in · Sleeper Lee · ZCode · or an API key · Signed in · 其他 · 他";
        d.title = "WorkBuddy · 138****5678";
        document.querySelector("#view-usage").append(d);
      });
      await btn.click();
      assert.equal((await btn.textContent()).trim(), w.hidden);
      assert.match(await btn.getAttribute("title"), w.back);
      assert.match(await page.locator("#rtMask").getAttribute("title"), w.back);
      await page.waitForFunction(() => document.querySelectorAll("#sample .pii").length === 3);
      const got = await page.evaluate(() => ({
        users: [...document.querySelectorAll(".subscription-account .user")].map((u) => ({ raw: u.querySelector(".pii")?.dataset.raw ?? null, text: u.textContent, title: u.title })),
        heads: [...document.querySelectorAll(".subscription-head b")].map((b) => b.textContent),
        sample: document.querySelector("#sample").textContent,
        sampleRaws: [...document.querySelectorAll("#sample .pii")].map((e) => e.dataset.raw),
        sampleTitle: document.querySelector("#sample").title,
      }));
      const byRaw = Object.fromEntries(got.users.map((u) => [u.raw, u]));
      for (const name of ["李雷", "138****5678", "Lee", "dev.one@example.com", "他"]) {
        assert.ok(byRaw[name], `${name} is hidden: ${JSON.stringify(got.users)}`);
        assert.ok(!byRaw[name].text.includes(name), `${name} isn't in sight`);
        assert.ok(!byRaw[name].title.includes(name), `${name} isn't in its tooltip: ${byRaw[name].title}`);
      }
      assert.equal(byRaw["138****5678"].title, "•••••••••••");
      // ZCode signed in with no name of its own is the card's name: kept
      assert.ok(got.users.some((u) => u.raw === null && u.text === "ZCode"), "ZCode's own name stays");
      assert.ok(got.heads.includes("WorkBuddy") && got.heads.includes("ZCode"), got.heads.join());
      assert.deepEqual(got.sampleRaws, ["李雷", "Lee", "他"]);
      // magpie's labels for a plugin's nameless accounts are words, kept
      // (outloudvi: Kiro's summary "…or an API key" was blurred)
      assert.match(got.sample, /^\S+ checked in · Sleeper \S+ · ZCode · or an API key · Signed in · 其他 · \S+$/);
      assert.equal(got.sampleTitle, "WorkBuddy · •••••••••••");

      // Routing: a route's account, learnt from what the gateway answered
      await page.evaluate(() => {
        noteAccounts({ order: [{ kind: "account", who: "韩梅梅", name: "WorkBuddy" }, { kind: "key", who: "team key" }] });
        const d = document.createElement("div");
        d.id = "route";
        d.textContent = "韩梅梅 answered; team key waited";
        document.querySelector("#view-routing").append(d);
      });
      await page.waitForFunction(() => document.querySelector("#route .pii")?.dataset.raw === "韩梅梅");
      assert.match(await page.locator("#route").textContent(), /^\S+ answered; team key waited$/);

      // the tray panel follows the window's setting, as it is turned
      const panel = await open("http://magpie.test/?mode=panel");
      await panel.evaluate(() => setPanelTab("usage"));
      await panel.waitForFunction(() => [...document.querySelectorAll("#panelQuota .pq-user .pii")].some((e) => e.dataset.raw === "李雷"));
      const pq = await panel.evaluate(() => [...document.querySelectorAll("#panelQuota .pq-card")].map((c) => ({ text: c.querySelector(".pq-user").textContent, title: c.title })));
      for (const c of pq) assert.ok(!/李雷|138\*|dev\.one|Lee\b|^他$/.test(c.text) && !/李雷|138\*|dev\.one|Lee\b|他/.test(c.title), JSON.stringify(c));
      // and its model picker, which sits outside the panel's view: a Codex
      // account's address beside its model (PaulT on Discord)
      await panel.evaluate(() => {
        const li = document.createElement("li");
        li.id = "pickRow";
        li.textContent = "GPT-6-Astra via magpie dev.one@example.com";
        li.title = "dev.one@example.com";
        document.querySelector("#pop #list").append(li);
      });
      await panel.waitForFunction(() => document.querySelector("#pickRow .pii")?.dataset.raw === "dev.one@example.com");
      const row = await panel.evaluate(() => ({ text: document.querySelector("#pickRow").textContent, title: document.querySelector("#pickRow").title }));
      assert.ok(!/dev\.one/.test(row.text + row.title), JSON.stringify(row));
      await btn.click();
      assert.equal((await btn.textContent()).trim(), w.hide);
      await panel.waitForFunction(() => !document.querySelector("#panelQuota .pii"));
      await panel.waitForFunction(() => !document.querySelector("#pickRow .pii"));
      assert.equal(await panel.locator("#pickRow").textContent(), "GPT-6-Astra via magpie dev.one@example.com");
      assert.ok(await panel.evaluate(() => [...document.querySelectorAll("#panelQuota .pq-user")].some((e) => e.textContent === "李雷")));
      // and back as they were in the window
      assert.equal(await page.locator("#sample").textContent(), "李雷 checked in · Sleeper Lee · ZCode · or an API key · Signed in · 其他 · 他");
      assert.equal(await page.locator("#sample").getAttribute("title"), "WorkBuddy · 138****5678");
      assert.equal(await page.locator(".subscription-account .user", { hasText: "李雷" }).getAttribute("title"), "李雷");
      assert.deepEqual(errors, []);
    });
  }
}
