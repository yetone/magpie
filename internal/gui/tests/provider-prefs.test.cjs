// Run with Node's test runner and Playwright on the module path; see README.md.
// The owner, 11-07: "这些配置难道不是应该在对应的Provider那个弹窗里面吗？为什么会散落在Settings里？"
// A subscription's warm-up, daily check-in and Codex's auto-use resets are
// in its provider's editor (PROV_PREFS), not in Settings › Allowances:
// Codex's has its resets and warm-up, Claude's its warm-up, WorkBuddy's its
// check-in, a plugin's that checks in itself its own row; Settings' search
// still finds them, each a row that opens the editor. Chromium and WebKit,
// at 1100 and 420, in every language; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { provPrefsProviders, openProvPrefs, watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, posted) {
  const settings = { theme: "light", lang, currency: "usd", guideHidden: true,
    codexUsers: ["ann@example.test"], claudeUsers: ["ann@example.test"], codexWarmup: "week",
    workbuddy: true, workbuddyCheckin: true,
    checkinAccounts: [{ key: "workbuddy|u1", by: "", user: "Ann", on: true }],
    checkinPlugins: [{ id: "fakeco", name: "FakeCo", on: true }] };
  const providers = provPrefsProviders(["codex", "claude", "workbuddy", "fakeco", "relayco"], { codex: "Codex", claude: "Claude Code", workbuddy: "WorkBuddy", fakeco: "FakeCo", relayco: "RelayCo" });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings" && req.method() === "POST") {
      const b = req.postDataJSON();
      posted.push(b);
      Object.assign(settings, b);
      return json(settings);
    }
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const LANGS = ["en", "zh", "zh-TW", "ja", "de"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const lang of LANGS) {
      test(`${engine} ${width} ${lang}: each subscription's warm-up, check-in and resets are in its provider's editor`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posted = [];
        page.on("pageerror", (e) => {
          // a goto cuts off a fetch in flight, and WebKit reports it "due to
          // access control checks" (#1350): not this test's
          if (engine === "webkit" && /due to access control checks/.test(e.message)) return;
          errors.push(e.message);
        });
        await page.route("**/*", serve(lang, posted));

        await openProvPrefs(page, "codex");
        const box = page.locator(".editor .prov-prefs");
        await box.locator("#codexWarmList #warmSegs .opt").first().waitFor();
        assert.equal(await box.locator("#codexResetList").count(), 1, "Codex's resets");
        assert.equal(await box.locator("#claudeWarmList, #wbList, .ci-plugin").count(), 0, "only Codex's own");
        const fits = await box.evaluate((b) => { const r = b.getBoundingClientRect(); return r.width > 0 && r.right <= innerWidth; });
        assert.equal(fits, true, "the box fits the window");
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
        if (process.env.ARTIFACT_DIR) await page.locator(".editor").first().screenshot({ path: path.join(process.env.ARTIFACT_DIR, `provider-prefs-${engine}-${width}-${lang}-codex.png`) });
        // written from the editor as from Settings
        const weekly = box.locator("#warmSegs .opt").nth(2);
        await weekly.scrollIntoViewIfNeeded();
        const saved = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/settings" && r.request().method() === "POST");
        await weekly.click();
        await saved;
        assert.equal(posted.at(-1).codexWarmup, "all");
        await page.waitForFunction(() => document.querySelector(".editor .prov-prefs #warmSegs .opt.on") === document.querySelectorAll(".editor .prov-prefs #warmSegs .opt")[2]);

        await openProvPrefs(page, "claude");
        assert.equal(await box.locator("#claudeWarmList").count(), 1, "Claude's warm-up");
        assert.equal(await box.locator("#codexWarmList, #codexResetList").count(), 0);

        await openProvPrefs(page, "workbuddy");
        await box.locator("#wbCheckinSegs .opt").first().waitFor();
        assert.equal(await box.locator("#wbCheckinSegs .opt").count(), 2, "WorkBuddy's check-in switch");

        await openProvPrefs(page, "fakeco");
        assert.equal(await box.locator('.ci-plugin[data-provider="fakeco"]').count(), 1, "the plugin's own check-in");

        // a subscription with none of them: no box
        await page.goto("http://magpie.test/?view=providers&edit=relayco");
        await page.locator(".editor").first().waitFor();
        assert.equal(await page.locator(".editor .prov-prefs:not([hidden])").count(), 0, "nothing for RelayCo");

        // Settings › Allowances has none of them, and its search still finds them
        await watchSettingsReads(page);
        await page.goto("http://magpie.test/?view=settings&tab=usage");
        await settingsDrawn(page);
        assert.equal(await page.locator("#setPage-usage #warmSegs, #setPage-usage #codexAutoResetRow, #setPage-usage #wbCheckinSegs").count(), 0, "none left in Settings");
        assert.equal(await page.locator("#provPrefLinks").isVisible(), false, "the links show only while searching");
        const name = await page.evaluate(() => document.querySelector("#codexWarmList .row .name").textContent);
        await page.locator("#setQ").fill(name);
        const link = page.locator("#provPrefLinks .agent-pref-link", { hasText: name }).first();
        await link.waitFor();
        await link.locator("button").click();
        await page.waitForFunction(() => new URL(location.href).searchParams.get("edit") === "codex");
        await page.locator(".editor .prov-prefs #codexWarmList").waitFor();
        assert.deepEqual(errors, []);
      });
    }
  }
}
