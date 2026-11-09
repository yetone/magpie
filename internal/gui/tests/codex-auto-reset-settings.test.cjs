// Run with Node's test runner and Playwright on the module path; see README.md.
// Codex resets used by themselves (foobra12 on Discord: 自动给我使用了一张, 这个
//设置在哪儿找呢; Avalon: only the tray's quota showed the switch). Codex's
// provider editor lists each signed-in Codex account under "Auto-use resets"
// with an Off/On for each of the two times it uses one, its week used up
// and one about to expire (#2078, alanwalk): Off for every account to begin
// with, a pick posts settings/codex-auto-reset for that account and that
// time and turns that switch alone, and with no Codex account the row isn't
// there. Five languages, 1100 and 420 wide,
// both engines; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, provPrefsProviders, openProvPrefs, inEditor } = require("./reader.cjs");

// scrolls: where every box that scrolls is, so a click that moved one shows
const scrolls = (page) => page.evaluate(() => [scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join(","));
// onIs waits till the switch's pressed option reads want
const onIs = (seg, want) => seg.locator(".opt.on", { hasText: want }).waitFor();

const assets = path.resolve(__dirname, "../assets");

function server(lang, users, posts) {
  const fixed = {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", dock: false, dockWindow: false, proxy: "",
    redact: false, redactPersonal: false, redactWords: [], codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "",
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "", version: "0.1.900", dir: "~/.config/magpie",
    gateway: "http://127.0.0.1:3425", proxyNow: "none", proxySource: "none", visionModels: [], imageGenModels: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    codexUsers: users, codexAutoReset: [], codexAutoResetOnly: {},
  };
  let cur = fixed;
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: fixed.fx });
    if (url.pathname === "/api/settings/codex-auto-reset") {
      const body = req.postDataJSON();
      posts.push(body);
      // as the settings keep it: on, and narrowed to one time or not
      const u = body.user.toLowerCase(), only = { ...cur.codexAutoResetOnly };
      const was = cur.codexAutoReset.includes(u) ? { week: only[u] !== "expiring", expiring: only[u] !== "week" } : { week: false, expiring: false };
      const w = body.when ? { ...was, [body.when]: body.on } : { week: body.on, expiring: body.on };
      const on = cur.codexAutoReset.filter((x) => x !== u);
      delete only[u];
      if (w.week || w.expiring) on.push(u);
      if (w.week !== w.expiring) only[u] = w.week ? "week" : "expiring";
      cur = { ...cur, codexAutoReset: on, codexAutoResetOnly: only };
      return json(cur);
    }
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") cur = { ...cur, ...req.postDataJSON() };
      return json(cur);
    }
    if (url.pathname === "/api/providers") return json(provPrefsProviders(["codex"], { codex: "Codex" }));
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { name: "Auto-use resets", off: "Off", on: "On", week: "when its week runs out", expiring: "before one expires" },
  zh: { name: "自动使用重置卡", off: "关闭", on: "开启", week: "周额度用完时", expiring: "重置卡到期前" },
  "zh-TW": { name: "自動使用重置卡", off: "關閉", on: "開啟" },
  ja: { name: "リセットを自動使用", off: "オフ", on: "オン" },
  de: { name: "Resets automatisch einlösen", off: "Aus", on: "An" },
};

const users = ["alice@example.com", "a-much-longer-team-seat-address@some-company.example.org"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: Codex's editor turns each Codex account's auto-use resets on and off`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-codex-auto-reset-settings.png`) });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); }); // a goto in WebKit can cut off a fetch (#1350)
        await page.route("**/*", server(lang, users, posts));
        const posted = async (n) => { for (let i = 0; i < 100 && posts.length < n; i++) await page.waitForTimeout(20); assert.equal(posts.length, n, "posts"); return posts[n - 1]; };
        const w = words[lang];
        await watchSettingsReads(page);
        await openProvPrefs(page, "codex");
        await settingsDrawn(page);
        const head = page.locator(".editor .prov-prefs #codexAutoResetRow");
        await head.waitFor();
        assert.equal((await head.locator(".name").textContent()).trim(), w.name);
        const w2 = { ...w, ...(w.week ? {} : await page.evaluate((lang) => ({ week: I18N[lang]["when its week runs out"], expiring: I18N[lang]["before one expires"] }), lang)) };
        assert(w2.week && w2.expiring, "both times are translated");
        const rows = page.locator(".ar-own");
        const at = (i, c) => page.locator(".ar-own").nth(i).locator(`.ar-own-case[data-case="${c}"]`);
        assert.deepEqual(await rows.locator(".name").allTextContents(), users);
        for (let i = 0; i < users.length; i++) {
          assert.deepEqual(await rows.nth(i).locator(".ar-own-say").allTextContents(), [w2.week, w2.expiring], "each time its own line");
          for (const c of ["week", "expiring"]) {
            assert.deepEqual(await at(i, c).locator(".opt").allTextContents(), [w.off, w.on]);
            assert.equal(await at(i, c).locator(".opt.on").textContent(), w.off, "off to begin with");
          }
        }

        // the second account's before-it-expires On: that time alone
        // turns, its week kept off (#2078)
        await inEditor(page, at(1, "expiring").locator(".opt", { hasText: w.on }));
        const top = await scrolls(page);
        await at(1, "expiring").locator(".opt", { hasText: w.on }).click();
        assert.deepEqual(await posted(1), { user: users[1], on: true, when: "expiring" });
        await onIs(at(1, "expiring"), w.on);
        assert.equal(await at(1, "week").locator(".opt.on").textContent(), w.off, "its week kept off");
        for (const c of ["week", "expiring"]) assert.equal(await at(0, c).locator(".opt.on").textContent(), w.off, "only that account");
        assert.equal(await scrolls(page), top, "no click moved the page");

        // every row fits: nothing runs past the editor
        const over = await page.evaluate(() => {
          const view = document.querySelector(".editor .prov-prefs[data-list=codexResetList]").getBoundingClientRect();
          return [...document.querySelectorAll("#codexAutoResetRow, .ar-own, .ar-own-case, .ar-own-say, .ar-own .opt")]
            .filter((e) => { const r = e.getBoundingClientRect(); return r.right > view.right + 1 || r.left < view.left - 1; })
            .map((e) => e.className + " " + e.textContent.slice(0, 30));
        });
        assert.deepEqual(over, []);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "no sideways scroll");
        assert.equal(await head.evaluate((e) => getComputedStyle(e).borderLeftWidth), "0px");
        assert.equal(await page.locator(".editor select").count(), 0, "no native select");

        // the week on too, then the expiring off: the week kept
        await inEditor(page, at(1, "week").locator(".opt", { hasText: w.on }));
        await at(1, "week").locator(".opt", { hasText: w.on }).click();
        assert.deepEqual(await posted(2), { user: users[1], on: true, when: "week" });
        await onIs(at(1, "week"), w.on);
        await inEditor(page, at(1, "expiring").locator(".opt", { hasText: w.off }));
        await at(1, "expiring").locator(".opt", { hasText: w.off }).click();
        assert.deepEqual(await posted(3), { user: users[1], on: false, when: "expiring" });
        await onIs(at(1, "expiring"), w.off);
        assert.equal(await at(1, "week").locator(".opt.on").textContent(), w.on, "the week kept on");
        assert.deepEqual(errors, []);
      });
    }
  }

  test(`${engine}: with no Codex account the row isn't there`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const page = await (await browser.newContext({ viewport: { width: 900, height: 600 }, reducedMotion: "reduce" })).newPage();
    t.after(() => browser.close());
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); }); // a goto in WebKit can cut off a fetch (#1350)
    await page.route("**/*", server("en", [], []));
    await watchSettingsReads(page);
    await openProvPrefs(page, "codex");
    await settingsDrawn(page);
    await page.locator(".editor .prov-prefs #warmSegs .opt").first().waitFor();
    assert.equal(await page.locator("#codexAutoResetRow").isVisible(), false);
    assert.equal(await page.locator(".ar-own").count(), 0);
    assert.deepEqual(errors, []);
  });
}
