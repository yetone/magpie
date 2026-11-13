// Run with Node's test runner and Playwright on the module path; see README.md.
// wxkwxk666 on X: a provider switched off still has accounts to check in
// each day, and checking every account in meant a press per vendor. The
// Usage page lists the switched-off providers' accounts a check-in reaches
// in a line at the cards' end, folded until clicked (GET
// /api/usage/checkins/off), each with its own Check in; and Check in all,
// at the allowances' head, presses every account not checked in today
// (POST /api/usage/checkin-account each), a switched-off provider's too,
// saying how far it has got and then what came of it. Nothing switches a
// provider on. Every language, wide and narrow, both engines; the API is
// faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const today = new Date(Date.now() + 8 * 3600e3).toISOString().slice(0, 10);

function serve(lang, w) {
  const settings = { theme: "light", lang, quotaLeft: false, currency: "usd", workbuddyCheckin: true, traeCheckin: false };
  const done = new Set(["fakeco|b"]);
  const ci = (key, by, user) => done.has(key)
    ? { user, by, day: today, outcome: "claimed", credit: 10 }
    : { user, by, day: "2026-10-01", outcome: "claimed", credit: 10 };
  const quotas = () => [
    { provider: "workbuddy", name: "WorkBuddy", icon: "workbuddy-color", plan: "Pro", user: "lilei", windows: [{ name: "Credits", used: 8 }],
      checkins: true, checkinKey: "wb|lilei", checkin: { user: "lilei", day: today, outcome: "claimed", credit: 50 } },
    { provider: "trae-cn", name: "Trae CN", plan: "Free", user: "hu", windows: [{ name: "Credits", used: 3 }],
      checkins: true, checkinBy: "trae", checkinKey: "trae|hu", checkin: ci("trae|hu", "trae", "hu") },
    { provider: "codex", name: "Codex", icon: "openai", plan: "Plus", windows: [{ name: "5 hours", used: 20 }] },
  ];
  const off = () => [
    { provider: "fakeco", name: "FakeCo", icon: "", user: "alice", windows: [], checkins: true, checkinBy: "plugin:fakeco", checkinKey: "fakeco|a", checkin: ci("fakeco|a", "plugin:fakeco", "alice") },
    { provider: "fakeco", name: "FakeCo", icon: "", user: "bob", windows: [], checkins: true, checkinBy: "plugin:fakeco", checkinKey: "fakeco|b", checkin: ci("fakeco|b", "plugin:fakeco", "bob") },
    { provider: "fakeco", name: "FakeCo", icon: "", user: "carol", windows: [], checkins: true, checkinBy: "plugin:fakeco", checkinKey: "fakeco|c", checkin: ci("fakeco|c", "plugin:fakeco", "carol") },
  ];
  w.posts = [];
  w.hold = null; // a promise the presses wait on, to see the progress
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    // what a press could change: a check-in, a setting, a provider (not the window's own pings)
    if (req.method() === "POST" && /^\/api\/(usage|settings|provider)/.test(url.pathname)) w.posts.push([url.pathname, req.postDataJSON()]);
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/usage/checkin-account") {
      const { by, key } = req.postDataJSON();
      if (w.hold) await w.hold;
      done.add(key);
      return json({ user: key, by, day: today, outcome: "claimed", asked: true, credit: 10 });
    }
    if (url.pathname === "/api/usage/checkins/off") return json(off());
    if (url.pathname === "/api/usage/quotas") return json(quotas());
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { line: "Switched off · 3", all: "Check in all", busy: "Checking in 0/2…", ok: "Claimed: 2 (+20 credits)", none: "Every account is checked in today", wait: "Not checked in yet today", in: "Check in" },
  zh: { line: "已禁用 · 3", all: "全部签到", busy: "正在签到 0/2…", ok: "已领取：2（+20 积分）", none: "所有账号今天都已签到", wait: "今日尚未签到", in: "签到" },
  "zh-TW": { line: "已停用 · 3", all: "全部簽到", busy: "正在簽到 0/2…", ok: null, none: "所有帳號今天都已簽到", wait: "今日尚未簽到", in: "簽到" },
  ja: { line: "オフ · 3", all: "すべてチェックイン", busy: "チェックイン中 0/2…", ok: null, none: "すべてのアカウントは今日チェックイン済みです", wait: "今日はまだチェックインしていません", in: "チェックイン" },
  de: { line: "Ausgeschaltet · 3", all: "Alle einchecken", busy: "Einchecken 0/2…", ok: null, none: "Alle Konten sind heute eingecheckt", wait: "Heute noch nicht eingecheckt", in: "Einchecken" },
};

const scrolls = (page) => page.evaluate(() => [scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join(","));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [900, 420]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}: switched-off providers' check-ins, and Check in all`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], fake = {};
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, fake));
        await page.goto("http://magpie.test/?view=usage");

        // the switched-off line: after the cards and the hidden line, folded
        const off = page.locator("#quotaOff");
        await off.waitFor();
        const line = off.locator(".us-hidline");
        assert.equal((await line.innerText()).trim(), w.line);
        assert.equal(await line.getAttribute("aria-expanded"), "false");
        assert.equal(await off.locator(".list").isHidden(), true, "folded until clicked");
        assert.equal(await page.evaluate(() => {
          const a = document.querySelector("#subscriptionUsage"), b = document.querySelector("#quotaHid"), c = document.querySelector("#quotaOff");
          return !!(a.compareDocumentPosition(c) & Node.DOCUMENT_POSITION_FOLLOWING) && !!(b.compareDocumentPosition(c) & Node.DOCUMENT_POSITION_FOLLOWING);
        }), true, "the line is at the cards' end");
        // nothing of FakeCo among the cards themselves
        assert.equal(await page.locator("#subscriptionUsage", { hasText: "FakeCo" }).count(), 0);

        // opened: each account, the one not in today with its own Check in
        const y = await scrolls(page);
        await line.click();
        const rows = off.locator(".us-offrow");
        await rows.first().waitFor();
        assert.equal(await rows.count(), 3);
        assert.equal(await scrolls(page), y, "opening moved the page");
        assert.equal((await rows.nth(0).locator(".ci-say").innerText()).trim(), w.wait);
        assert.equal(await rows.nth(0).locator(".ci-acct").count(), 1);
        assert.equal((await rows.nth(0).locator(".ci-acct").innerText()).trim(), w.in);
        assert.equal(await rows.nth(1).locator(".ci-acct").count(), 0, "bob is in today");
        for (const r of await rows.all()) {
          const box = await r.boundingBox();
          assert.ok(box.x >= 0 && box.x + box.width <= width + 1, `a row runs off the window: ${JSON.stringify(box)}`);
        }
        // carol's own Check in: that account alone, and read again
        const reread = page.waitForRequest((r) => new URL(r.url()).pathname === "/api/usage/checkins/off");
        await rows.nth(2).locator(".ci-acct").click();
        await reread;
        await page.waitForFunction(() => !document.querySelector('#quotaOff .us-offrow[data-key="fakeco|c"] .ci-acct'));
        assert.deepEqual(fake.posts, [["/api/usage/checkin-account", { by: "plugin:fakeco", key: "fakeco|c" }]]);
        fake.posts.length = 0;

        // Check in all: only the accounts not in today, shown as it goes
        const all = page.locator("#usageCheckinAll");
        assert.equal((await all.innerText()).trim(), w.all);
        let let_go;
        fake.hold = new Promise((r) => { let_go = r; });
        await all.click();
        await page.waitForFunction((s) => document.querySelector("#usageCheckinAll")?.textContent === s, w.busy);
        assert.equal(await all.isDisabled(), true);
        let_go();
        fake.hold = null;
        await page.waitForFunction((s) => document.querySelector("#usageCheckinAll")?.textContent === s, w.all);
        const pressed = fake.posts.filter(([p]) => p === "/api/usage/checkin-account").map(([, b]) => b.key).sort();
        assert.deepEqual(pressed, ["fakeco|a", "trae|hu"]);
        assert.deepEqual(fake.posts.filter(([p]) => p !== "/api/usage/checkin-account"), [], "nothing else was asked, no provider switched on");
        if (w.ok) assert.equal((await page.locator("#status").innerText()).trim(), w.ok);
        // read again: alice is in now, and no Check in is left
        await page.waitForFunction(() => document.querySelectorAll("#quotaOff .ci-acct").length === 0);
        assert.equal(await scrolls(page), y, "Check in all moved the page");

        // pressed again with every account in: nothing asked, said so
        const before = fake.posts.length;
        await all.click();
        await page.waitForFunction((s) => document.querySelector("#status")?.textContent === s, w.none);
        assert.equal(fake.posts.length, before);
        if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `usage-checkin-all-${engine}-${lang}-${width}.png`) });
        assert.equal(await page.locator("select").count(), 0);
        assert.deepEqual(errors, []);
      });
    }
  }
}
