// Run with Node's test runner and Playwright on the module path; see README.md.
// Lemon on Discord: "provider 每日签到的方法能放到插件里面去做吗". A plugin
// whose auth hook has checkin presses its vendor's daily check-in itself, and
// magpie shows it as it shows its own: the account's Usage card has the row
// (checkinBy "plugin:<provider>"), with Auto check-in posting
// /api/settings/plugin-checkin {provider, on} and Check in now posting
// /api/usage/plugin-checkin {provider}; a captcha the vendor asks for is said
// as such, never as a failure, as magpie never solves one. Settings' check-in
// tabs gain Plugins while such a plugin is signed in, a row a provider with
// Off/On. English, Simplified and Traditional Chinese, Japanese and German,
// the window and the panel's 440px; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const vm = require("node:vm");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
test("plugin check-in saved choices take precedence over derived defaults", async () => {
  const source = await fs.readFile(path.join(assets, "app.js"), "utf8");
  const fn = source.match(/const PLUGIN_CHECKIN_INHERITS = \{[\s\S]*?\n\};\nfunction pluginCheckinOn\(id\) \{[\s\S]*?\n\}/)[0];
  const on = (settings, id = "fakeco") => vm.runInNewContext(`${fn}; pluginCheckinOn(${JSON.stringify(id)})`, { state: { settings } });
  assert.equal(on({ pluginCheckins: { fakeco: true } }), true);
  assert.equal(on({ pluginCheckins: { fakeco: false }, checkinPlugins: [{ id: "fakeco", on: true }] }), false);
  assert.equal(on({ pluginCheckins: { fakeco: true }, checkinPlugins: [{ id: "fakeco", on: false }] }), true);
  assert.equal(on({ checkinPlugins: [{ id: "fakeco", on: true }] }), true);
  assert.equal(on({ checkinPlugins: [{ id: "another", on: true }] }), false);
  assert.equal(on({}), false);
  assert.equal(on(undefined), false);
  // a plugin that took a built-in's check-in over follows the built-in's
  // switch until its own is set, as provider.PluginCheckinOn does, also in
  // /api/state's shape, which has no list (#1912)
  assert.equal(on({ traeCheckin: true }, "trae-cn"), true);
  assert.equal(on({ traeCheckin: true, pluginCheckins: { "trae-cn": false } }, "trae-cn"), false);
  assert.equal(on({ workbuddyCheckin: true }, "workbuddy"), true);
  assert.equal(on({ minimaxCheckin: true }, "minimax-code-global"), true);
  assert.equal(on({ qoderCheckin: true }, "qoder-cn"), true);
  assert.equal(on({ traeCheckin: true }, "fakeco"), false);
  assert.equal(on({ traeCheckin: true }, "constructor"), false);
});

// the built-ins a plugin's check-in inherits are the ones Go's
// PluginCheckinOn names, each by the setting's own JSON key
test("the inherited switches are provider.PluginCheckinOn's", async () => {
  const root = path.resolve(__dirname, "../../..");
  const go = await fs.readFile(path.join(root, "internal/provider/plugin_checkin.go"), "utf8");
  const sw = go.match(/func PluginCheckinOn\([\s\S]*?\n\}/)[0];
  const cases = [...sw.matchAll(/case ([^:]+):\s*return s\.(\w+)/g)].map((m) => [m[1].split(",").map((x) => x.trim()), m[2]]);
  const ids = { "wbCN.id": "workbuddy", TraeCNID: "trae-cn", MiniMaxCodeID: "minimax-code", MiniMaxCodeGlobalID: "minimax-code-global", '"qoder"': "qoder", QoderCNID: "qoder-cn" };
  const settings = await fs.readFile(path.join(root, "internal/settings/settings.go"), "utf8");
  const want = {};
  for (const [names, field] of cases) {
    const key = settings.match(new RegExp(`\\b${field} +bool +\`json:"(\\w+)`))[1];
    for (const n of names) {
      assert(ids[n], "an id the test doesn't know: " + n);
      want[ids[n]] = key;
    }
  }
  const source = await fs.readFile(path.join(assets, "app.js"), "utf8");
  const got = vm.runInNewContext(source.match(/const PLUGIN_CHECKIN_INHERITS = \{[\s\S]*?\n\};/)[0] + "; PLUGIN_CHECKIN_INHERITS");
  assert.deepEqual({ ...got }, want);
});

const today = new Date(Date.now() + 8 * 3600e3).toISOString().slice(0, 10);
const quotas = () => [
  { provider: "plugin:fakeco", name: "FakeCo", plan: "Pro", user: "ann", windows: [{ name: "Credits", used: 12 }],
    checkins: true, checkinBy: "plugin:fakeco", checkin: { user: "ann", by: "plugin:fakeco", vendor: "FakeCo", day: today, outcome: "claimed", credit: 50, streak: 3 } },
  { provider: "plugin:fakeco", name: "FakeCo", plan: "Free", user: "bob", windows: [{ name: "Credits", used: 2 }],
    checkins: true, checkinBy: "plugin:fakeco", checkin: { user: "bob", by: "plugin:fakeco", vendor: "FakeCo", day: today, outcome: "captcha", msg: "slide the puzzle" } },
  { provider: "codex", name: "Codex", icon: "openai", plan: "Plus", windows: [{ name: "5 hours", used: 20 }] },
];

function serve(lang, asked, more = []) {
  const settings = { theme: "light", lang, quotaLeft: false, currency: "usd", pluginCheckins: { fakeco: false },
    checkinPlugins: [{ id: "fakeco", name: "FakeCo", on: false, checkins: [
      { user: "ann", by: "plugin:fakeco", vendor: "FakeCo", day: today, outcome: "claimed", credit: 50, streak: 3 },
      { user: "bob", by: "plugin:fakeco", vendor: "FakeCo", day: today, outcome: "captcha" },
    ] }, ...more] };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") {
      // stateJSON embeds settings.Settings; only settingsJSON adds this list.
      const { checkinPlugins, ...saved } = settings;
      return json({ agents: [], profiles: [], settings: saved });
    }
    if (url.pathname === "/api/settings/plugin-checkin") {
      const b = route.request().postDataJSON();
      asked.push(["set", b]);
      settings.pluginCheckins[b.provider] = b.on;
      settings.checkinPlugins = settings.checkinPlugins.map((p) => (p.id === b.provider ? { ...p, on: b.on } : p));
      return json(settings);
    }
    if (url.pathname === "/api/usage/plugin-checkin") {
      asked.push(["now", route.request().postDataJSON()]);
      return json([{ user: "bob", by: "plugin:fakeco", day: today, outcome: "captcha" }]);
    }
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
  en: { done: "Checked in today +50 · 3-day streak", captcha: "Asks for a captcha; check in in its own app · slide the puzzle", auto: "Auto check-in", tab: "Plugins", sub: /FakeCo plugin check each signed-in account.*bob is asked for a captcha/ },
  zh: { done: "今日已签到 +50 · 连续 3 天", captcha: "需要验证码，请在它自己的 App 里签到 · slide the puzzle", auto: "自动签到", tab: "插件", sub: /FakeCo 插件.*bob 需要验证码/ },
  "zh-TW": { captcha: "需要驗證碼，請在它自己的 App 裡簽到 · slide the puzzle", tab: "外掛", sub: /FakeCo 外掛.*bob 需要驗證碼/ },
  ja: { captcha: "CAPTCHA が必要です。公式アプリでチェックインしてください · slide the puzzle", tab: "プラグイン", sub: /FakeCo プラグイン.*bob は CAPTCHA が必要です/ },
  de: { captcha: "Verlangt ein Captcha; in der eigenen App einchecken · slide the puzzle", tab: "Erweiterungen", sub: /FakeCo-Plugin.*bob soll ein Captcha lösen/ },
};

const scrolls = (page) => page.evaluate(() => [scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join(","));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a plugin's own check-in on its Usage card`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 900 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], asked = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, asked));
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/?view=usage");
      await settingsDrawn(page); // rows found are the ones that stay (#1659)
      const card = page.locator(".subscription-card", { hasText: "FakeCo" });
      await card.locator(".wb-checkin").first().waitFor();
      const rows = card.locator(".wb-checkin");
      assert.equal(await rows.count(), 2, "one line an account");
      const says = (await rows.locator(".ci-say").allInnerTexts()).map((s) => s.trim());
      if (w.done) assert.equal(says[0], w.done);
      assert.equal(says[1], w.captcha, "a captcha is said as such");
      assert.notEqual(await rows.nth(1).getAttribute("data-state"), "bad", "a captcha isn't a failure");
      assert.match(await rows.first().locator(".ci-say").getAttribute("title"), /FakeCo/);
      assert.equal(await page.locator(".subscription-card", { hasText: "Codex" }).locator(".wb-checkin").count(), 0);
      const auto = card.locator(".ci-auto");
      assert.equal(await auto.count(), 1);
      if (w.auto) assert.equal((await auto.innerText()).trim(), w.auto);
      assert.match(await auto.getAttribute("title"), /FakeCo/);
      assert.equal(await auto.getAttribute("aria-pressed"), "false");

      const y = await scrolls(page);
      await auto.click();
      await page.waitForFunction(() => document.querySelector(".ci-auto")?.getAttribute("aria-pressed") === "true");
      assert.deepEqual(asked, [["set", { provider: "fakeco", on: true }]]);
      assert.equal(await scrolls(page), y, "the toggle moved the page");

      // Returning to the window calls load(true), replacing settings with
      // /api/state's raw shape. Wait for its allowance redraw as well.
      await page.evaluate(async () => { await load(true); await loadQuotas(); });
      assert.equal(await auto.getAttribute("aria-pressed"), "true", "saved auto check-in stays on after a state refresh");
      await auto.click();
      await page.waitForFunction(() => document.querySelector(".ci-auto")?.getAttribute("aria-pressed") === "false");
      assert.deepEqual(asked, [
        ["set", { provider: "fakeco", on: true }],
        ["set", { provider: "fakeco", on: false }],
      ], "the next click turns the saved setting off");
      await page.evaluate(async () => { await load(true); await loadQuotas(); });
      assert.equal(await auto.getAttribute("aria-pressed"), "false", "saved off stays off after a state refresh");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: two plugins checking in share one Plugins tab`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 900 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, [], [{ id: "otherco", name: "OtherCo", on: true, checkins: [] }]));
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/?view=settings&tab=usage");
      await settingsDrawn(page);
      const tab = page.locator("#warmTab-plugins");
      await tab.waitFor();
      assert.equal((await tab.textContent()).trim(), w.tab);
      await tab.click();
      for (const id of ["fakeco", "otherco"]) await page.locator(`#pluginCheckinList .row[data-provider="${id}"]`).waitFor();
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: Settings has a Plugins check-in tab at 440px`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 440, height: 900 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], asked = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, asked));
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/?view=settings&tab=usage");
      await settingsDrawn(page); // rows found are the ones that stay (#1659)
      const tab = page.locator("#warmTab-plugins");
      await tab.waitFor();
      assert.equal(await tab.isHidden(), false);
      // one plugin checks in: the tab is its name (WorkBuddy's users looked for "WorkBuddy")
      assert.equal((await tab.textContent()).trim(), "FakeCo");
      assert.equal(await page.locator("#warmTab-qoder").isHidden(), true, "Qoder's tab with no Qoder account");
      // at 440x900 the tabs are below the fold: the reader scrolls to them
      await page.mouse.move(220, 400);
      for (let i = 0; i < 10; i++) {
        const r = await tab.boundingBox(), foot = await page.locator("footer.foot").boundingBox();
        if (r.y + r.height < foot.y - 8) break;
        await page.mouse.wheel(0, 200);
        await page.waitForTimeout(100);
      }
      await tab.click();
      const row = page.locator('#pluginCheckinList .row[data-provider="fakeco"]');
      await row.waitFor();
      assert.match(await row.locator(".name").innerText(), /FakeCo/);
      assert.match((await row.locator(".sub").innerText()).replace(/\s+/g, " "), w.sub);
      const box = await row.boundingBox();
      assert(box.width > 0 && box.x + box.width <= 440, "the row fits at 440px");
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
      if (process.env.ARTIFACT_DIR) await row.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `plugin-checkin-${engine}-${lang}.png`) });
      // and on to its switch, under the foot in a long language
      const on = row.locator(".segs button").nth(1);
      for (let i = 0; i < 10; i++) {
        const r = await on.boundingBox(), foot = await page.locator("footer.foot").boundingBox();
        if (r.y + r.height < foot.y - 8) break;
        await page.mouse.wheel(0, 200);
        await page.waitForTimeout(100);
      }
      const saved = page.waitForRequest((r) => new URL(r.url()).pathname === "/api/settings/plugin-checkin");
      await on.click();
      await saved;
      assert.deepEqual(asked, [["set", { provider: "fakeco", on: true }]]);
      assert.equal(await page.locator("select").count(), 0);
      assert.deepEqual(errors, []);
    });
  }
}
