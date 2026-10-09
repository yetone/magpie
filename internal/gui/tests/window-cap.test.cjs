// Run with Node's test runner and Playwright on the module path; see README.md.
// Each usage window of an account can have a cap of its own beside the
// account's (willz on Discord: a friend's account stops at 50% of its five
// hours, while its week has its own 40%). A window's meter on the account's
// row is a button: a click opens the app's own menu (no native select) of
// the account's cap, no cap on this window, a share, or Other… typed in
// place, each posted to provider/windowcap with the account, the window's
// capId and the cap (0: follow the account's; 100: none on this window). The
// meter says its own cap, its mark stands there, and the row's note says the
// account is at its cap when any window is past its own. A click moves
// nothing. Narrow and wide, in every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const inHours = (h) => new Date(Date.now() + h * 3600e3 + 60e3).toISOString();
const fresh = () => ({
  codex: {
    id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
    models: [{ id: "gpt-6", name: "", on: true }],
    agents: [], fallback: [], headers: {}, keyList: [], proxy: "",
    account: {
      agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS",
      logins: [
        { user: "me@example.com", plan: "PLUS", active: true, on: true },
        { user: "Friend@Example.com", plan: "PLUS", on: true },
      ],
    },
    // the friend's five hours stop at 50%, its week at 40%; no account cap
    accountWindowCaps: { "friend@example.com": { "5 hours": 50, weekly: 40 } },
  },
});
const win = (name, used, h) => ({ name, capId: name.toLowerCase(), used, resetsAt: inHours(h), capped: true });
// the friend at 20% of its five hours, 30% of its week: under both
const under = () => ({
  "me@example.com": { provider: "codex", windows: [win("5 hours", 10, 4), win("Weekly", 10, 100)] },
  "Friend@Example.com": { provider: "codex", windows: [win("5 hours", 20, 4), win("Weekly", 30, 100)] },
});
// the friend at 60% of its five hours, past its own 50%; the week at 10%
const past = () => ({
  "me@example.com": under()["me@example.com"],
  "Friend@Example.com": { provider: "codex", windows: [win("5 hours", 60, 3), win("Weekly", 10, 100)] },
});

function serve(lang, posts, use) {
  const ps = fresh();
  const providers = { providers: [ps.codex], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/login/usage") return json(url.searchParams.get("agent") === "codex" ? use() : {});
    if (url.pathname === "/api/provider/windowcap" && route.request().method() === "POST") {
      const body = route.request().postDataJSON();
      posts.push(body);
      const p = providers.providers.find((x) => x.id === body.id);
      const all = structuredClone(p.accountWindowCaps || {}), k = body.account.toLowerCase();
      const caps = all[k] || {};
      if (body.cap) caps[body.window] = body.cap;
      else delete caps[body.window];
      if (Object.keys(caps).length) all[k] = caps; else delete all[k];
      p.accountWindowCaps = all;
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { cap: (n) => `Cap ${n}%`, none: "No cap", byWindow: "Caps by window", held: "At its cap · back in 3h", head: "Cap for 5 hours", account: "Account's cap · none", off: "No cap on this window", other: "Other…" },
  zh: { cap: (n) => `上限 ${n}%`, none: "不设上限", byWindow: "按窗口设上限", held: "已达上限 · 3 小时后恢复", head: "5 小时的上限", account: "账号上限 · 无", off: "此窗口不设上限", other: "其他…" },
  "zh-TW": { cap: (n) => `上限 ${n}%`, none: null, byWindow: "按時段設上限", held: null, head: "5 小時的上限", account: "帳號上限 · 無", off: "此時段不設上限", other: null },
  ja: { cap: null, none: null, byWindow: "ウィンドウごとの上限", held: null, head: "5 時間の上限", account: "アカウントの上限 · なし", off: "このウィンドウは上限なし", other: null },
  de: { cap: null, none: null, byWindow: "Limits je Zeitfenster", held: null, head: "Limit für 5 Stunden", account: "Limit des Kontos · keins", off: "Kein Limit für dieses Zeitfenster", other: null },
};
const NEW = [
  "No cap on {window}: it is used to 100%, whatever this account's cap. Click to change",
  "{window} has a cap of its own: magpie counts this account as used up once {window} is at {n}%, until it renews. Click to change",
  "Click to give {window} a cap of its own, apart from this account's",
  "{window} of {who} follows the account's cap", "{window} of {who} has no cap", "{who} stops at {n}% of {window}",
  "Cap for {window}, in percent", "Account's cap · {n}%", "Account's cap · none", "As the account's other windows",
  "No cap on this window", "Use this window to 100%", "Cap for {window}", "Caps by window",
  "A window can have a cap of its own: click its meter below",
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [420, 1100]) {
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      const w = words[lang];
      const open = async (t, name, use) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${width}-${lang}-${name}-window-cap.png`) });
          }
          await browser.close();
        });
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [];
        await page.route("**/*", serve(lang, posts, use));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Codex" }).first().click();
        await page.locator(".editor .accts .acc .aq-w").first().waitFor();
        return { page, errors, posts };
      };
      const row = (page, id) => page.locator(`.editor .accts .acc[data-account-id="${id}"]`);
      const meter = (page, id, i) => row(page, id).locator('.aq-set[role="button"]').nth(i);
      const menu = (page) => page.locator(".proto-menu");
      const item = (page, name) => menu(page).locator(".pm-item", { has: page.locator(".pm-name", { hasText: new RegExp(`^${name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`) }) });
      const scrolled = (page) => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop > 0).map((e) => `${e.className}:${e.scrollTop}`)].join(" "));
      const still = async (page, id, b) => {
        await b.scrollIntoViewIfNeeded();
        await page.waitForTimeout(150);
        const before = await row(page, id).evaluate((e) => Math.round(e.getBoundingClientRect().top)), sc = await scrolled(page);
        await b.click();
        await page.waitForTimeout(200);
        assert.equal(await row(page, id).evaluate((e) => Math.round(e.getBoundingClientRect().top)), before, `${id}'s row moved`);
        assert.equal(await scrolled(page), sc, "a click scrolled");
      };
      const posted = async (posts, n) => {
        for (let i = 0; i < 60 && posts.length === n; i++) await new Promise((r) => setTimeout(r, 50));
        return posts.at(-1);
      };
      const label = `${engine} ${width} ${lang}`;

      test(`${label}: a window past its own cap holds the account; one under it doesn't`, async (t) => {
        const { page, errors } = await open(t, "past", past);
        // 60% of the five hours, past its own 50%: held, the mark at 50 and 40
        const held = row(page, "Friend@Example.com").locator(".acap-held");
        if (w.held) assert.equal(await held.textContent(), w.held);
        else assert.equal(await held.count(), 1);
        assert.match(await held.getAttribute("title"), /60/);
        assert.match(await held.getAttribute("title"), /50/);
        assert.deepEqual(await row(page, "Friend@Example.com").locator(".aq-cap").evaluateAll((ms) => ms.map((m) => m.style.left)), ["50%", "40%"]);
        // the pill says its caps are by window; each meter says its own
        assert.equal(await row(page, "Friend@Example.com").locator(".acap").textContent(), w.byWindow);
        if (w.cap) assert.deepEqual(await row(page, "Friend@Example.com").locator(".aq-own").allTextContents(), [w.cap(50), w.cap(40)]);
        else assert.equal(await row(page, "Friend@Example.com").locator(".aq-own").count(), 2);
        // me has no cap at all: no note, no marks, meters to set
        assert.equal(await row(page, "me@example.com").locator(".acap-held, .aq-cap, .aq-own").count(), 0);
        assert.equal(await row(page, "me@example.com").locator('.aq-set[role="button"]').count(), 2);
        // a meter is reached by the keyboard too: Enter opens its menu
        await meter(page, "me@example.com", 1).focus();
        await page.keyboard.press("Enter");
        await menu(page).waitFor();
        assert.deepEqual(errors, []);
      });

      test(`${label}: 20% and 30% under their own 50% and 40% leave the account in use`, async (t) => {
        const { page, errors } = await open(t, "under", under);
        assert.equal(await row(page, "Friend@Example.com").locator(".acap-held").count(), 0);
        assert.equal(await row(page, "Friend@Example.com").locator(".aq-cap").count(), 2);
        assert.deepEqual(errors, []);
      });

      test(`${label}: a window's cap is set from its meter in the app's menu, typed, and lifted`, async (t) => {
        const { page, errors, posts } = await open(t, "set", under);
        // me's five hours: the menu, headed by the window, no native select
        await still(page, "me@example.com", meter(page, "me@example.com", 0));
        await menu(page).waitFor();
        assert.equal(await menu(page).locator(".pm-head").textContent(), w.head);
        const names = await menu(page).locator(".pm-name").allTextContents();
        assert.equal(names[0], w.account);
        assert.equal(names[1], w.off);
        assert.deepEqual(names.slice(2, 7), ["50%", "60%", "70%", "80%", "90%"]);
        assert.equal(names.length, 8);
        assert.equal(await page.locator("select").count(), 0, "no native select");
        let n = posts.length;
        await item(page, "50%").click();
        assert.deepEqual(await posted(posts, n), { id: "codex", account: "me@example.com", window: "5 hours", cap: 50 });
        await row(page, "me@example.com").locator(".aq-own").first().waitFor();
        if (w.cap) assert.equal(await row(page, "me@example.com").locator(".aq-own").first().textContent(), w.cap(50));
        assert.deepEqual(await row(page, "me@example.com").locator(".aq-cap").evaluateAll((ms) => ms.map((m) => m.style.left)), ["50%"]);

        // the week: Other…, 45 typed in place
        await still(page, "me@example.com", meter(page, "me@example.com", 1));
        await menu(page).locator(".pm-item").last().click();
        const typed = row(page, "me@example.com").locator("input.acap-in");
        await typed.waitFor();
        await typed.fill("45");
        n = posts.length;
        await typed.press("Enter");
        assert.deepEqual(await posted(posts, n), { id: "codex", account: "me@example.com", window: "weekly", cap: 45 });
        await page.waitForFunction(() => document.querySelectorAll('.editor .acc[data-account-id="me@example.com"] .aq-own').length === 2);

        // the friend's five hours: no cap on this window, 100
        await still(page, "Friend@Example.com", meter(page, "Friend@Example.com", 0));
        n = posts.length;
        await item(page, w.off).click();
        assert.deepEqual(await posted(posts, n), { id: "codex", account: "Friend@Example.com", window: "5 hours", cap: 100 });
        await page.waitForFunction(() => document.querySelectorAll('.editor .acc[data-account-id="Friend@Example.com"] .aq-cap').length === 1);
        if (w.none) assert.equal(await row(page, "Friend@Example.com").locator(".aq-own").first().textContent(), w.none);

        // back to the account's: 0, its own goes
        await still(page, "Friend@Example.com", meter(page, "Friend@Example.com", 0));
        n = posts.length;
        await item(page, w.account).click();
        assert.deepEqual(await posted(posts, n), { id: "codex", account: "Friend@Example.com", window: "5 hours", cap: 0 });
        await page.waitForFunction(() => document.querySelectorAll('.editor .acc[data-account-id="Friend@Example.com"] .aq-own').length === 1);

        // the meters stay on the row's line, not wider than the page
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), "no page scroll sideways");
        const border = await page.evaluate(() => [...document.querySelectorAll(".accts .aq-set, .accts .aq-own, .accts .aq-cap, .proto-menu, .proto-menu *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
        assert.deepEqual(border, [], "no border stripes");
        if (lang !== "en") {
          const missing = await page.evaluate(([l, keys]) => keys.filter((k) => !I18N[l][k]), [lang, NEW]);
          assert.deepEqual(missing, [], `every string has its ${lang}`);
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
