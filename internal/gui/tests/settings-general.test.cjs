// Run with Node's test runner and Playwright on the module path; see README.md.
// 1ooogle on X redrew Settings → General the way macOS System Settings reads:
// a row says its name alone where the name says enough, a line under it only
// where it tells something the name can't, and Language is one menu button
// showing the language picked, not six segments. In every language, at
// 1000px and 440px, in both engines, on a Mac (the Dock and Session terminal
// rows show only there), with the API faked:
// - the rows whose names say enough have no line under them, and the lines
//   kept are never cut short, one line each on a wide window (narrower,
//   they wrap whole beside the control: arculongus on Discord); a line
//   that explains its row rests out of sight and shows whole in the
//   hover card instead (the owner, 11-02; settings-hints.test.cjs);
// - Codex's and Claude Code's own settings aren't here: they are on the
//   agents' rows (the owner, 10-20);
// - Language is a button (never a native <select>) showing the language;
//   its menu, the app's own, lists all six, and a pick saves lang and
//   draws the page in it, the button staying under the pointer;
// - no row has a coloured stripe down its left;
// - every row past the first has its line, the one after the agents' empty
//   link box too, and as much room above and below its words as any other:
//   the window's 7px left a row with a line under its name cramped, and the
//   one-line rows of one height (the owner, 10-22).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const names = { en: "English", zh: "简体中文", "zh-TW": "繁體中文", ja: "日本語", de: "Deutsch" };
const appearance = { en: "Appearance", zh: "外观", "zh-TW": "外觀", ja: "外観", de: "Darstellung" };
const system = { en: "System", zh: "跟随系统", "zh-TW": "跟隨系統", ja: "システム", de: "System" };
// the rows whose name says enough, found by the control they hold
const bare = ["#themeSegs", "#colorblindSegs", "#reduceMotionSegs", "#birdSegs", "#langPick", "#textSizeSegs", "#traySegs", "#plainNamesSegs", "#loginSegs", "#dockSegs"];
// the rows that keep a line, one each
const kept = ["#lightweightSegs", "#keepAwakeSegs", "#gatewayPauseSegs", "#quitAskSegs", "#sessionTerminalRow .sess-pick"];

function serve(lang, posted, st) {
  const settings = () => ({ lang, theme: "light", searchVendors: [], searchAPIs: [], ...st });
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data, status = 200) => r.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings() });
    if (url.pathname === "/api/settings") {
      if (r.request().method() === "POST") {
        const b = JSON.parse(r.request().postData());
        posted.push(b);
        Object.assign(st, b);
      }
      return json(settings());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1000, 440]) {
    test(`${engine} ${lang} ${width}px: Settings → General says each name alone where it says enough, Language one menu`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 760 } })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.addInitScript(() => Object.defineProperty(navigator, "platform", { get: () => "MacIntel" }));
      const posted = [], st = {};
      await page.route("**/*", serve(lang, posted, st));
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/?view=settings&tab=general");
      await settingsDrawn(page);
      await page.locator("#langPick span").waitFor();

      const rows = await page.evaluate(([bare, kept]) => {
        const row = (sel) => document.querySelector(sel)?.closest(".row");
        // an explanation rests out of sight, its words in the hover card
        // (settingshints.js); the row reads as its name alone
        const hint = (s) => getComputedStyle(s).getPropertyValue("--hint").trim() === "1";
        return {
          bare: bare.map((sel) => { const r = row(sel); return [sel, !r ? "missing" : r.hidden ? "hidden" : [...r.querySelectorAll(".sub")].filter((s) => s.getClientRects().length && s.textContent.trim()).map((s) => s.textContent)]; }),
          kept: kept.map((sel) => {
            const r = row(sel), s = r?.querySelector(".sub");
            return [sel, !r || r.hidden ? "missing" : !s ? "no line" : hint(s) ? { text: s.textContent, hint: true } : { text: s.textContent, lines: Math.round(s.getBoundingClientRect().height / parseFloat(getComputedStyle(s).lineHeight || 16)), cut: s.scrollWidth > s.clientWidth + 1 }];
          }),
          stripes: [...document.querySelectorAll("#setPage-general .row")].filter((r) => parseFloat(getComputedStyle(r).borderLeftWidth) > 1).length,
          sideways: document.documentElement.scrollWidth > innerWidth,
          rule: [...document.querySelectorAll("#setPage-general .row.pref")].filter((r) => r.getClientRects().length).map((r, i) => {
            const b = r.getBoundingClientRect(), w = r.querySelector(".who").getBoundingClientRect(), line = parseFloat(getComputedStyle(r).borderTopWidth);
            const oneLine = ![...r.querySelectorAll(".who .sub")].some((s) => s.getClientRects().length && s.textContent.trim() && !hint(s));
            return { id: r.id || r.querySelector(".name").textContent, lined: i === 0 || line >= 1, above: w.top - b.top - line, below: b.bottom - w.bottom, height: Math.round(b.height - line), oneLine };
          }),
        };
      }, [bare, kept]);
      for (const [sel, subs] of rows.bare) assert.deepEqual(subs, [], `${sel}'s row says its name alone`);
      // Long conversations stays here with no Codex or Claude Code, as in
      // this fixture (agent-prefs.test.cjs)
      assert.equal(await page.locator("#view-settings :is(#codexAgentsV1Row, #codexAsyncRow, #codexTitlesRow, #codexDescriptionsRow, #codexPredictionsRow, #codexAutoReviewRow, #codexMemoriesRow)").count(), 0, "an agent's own settings are on its row");
      for (const [sel, sub] of rows.kept) {
        assert.equal(typeof sub, "object", `${sel}: ${sub}`);
        assert.ok(sub.text.trim(), `${sel}: ${JSON.stringify(sub)}`);
        if (sub.hint) continue; // shown whole in the hover card (settings-hints.test.cjs)
        if (width === 1000) assert.ok(sub.lines <= 1, `${sel} takes more than one line: ${JSON.stringify(sub)}`);
        assert.equal(sub.cut, false, `${sel}'s line is cut short: ${sub.text}`);
      }
      assert.equal(rows.stripes, 0, "a row has a stripe down its left");
      assert.equal(rows.sideways, false, "the page scrolls sideways");
      for (const r of rows.rule) {
        assert.ok(r.lined, `${r.id} has no line above it`);
        assert.ok(r.above >= 11.5 && r.below >= 11.5, `${r.id}'s words sit close to its lines: ${JSON.stringify(r)}`);
      }
      if (width === 1000) assert.equal(new Set(rows.rule.filter((r) => r.oneLine).map((r) => r.height)).size, 1, `one-line rows of more than one height: ${JSON.stringify(rows.rule)}`);

      // Language: one button showing the language, the app's menu listing all six
      const pick = page.locator("#langPick");
      assert.equal(await pick.evaluate((b) => b.tagName), "BUTTON");
      assert.equal(await page.locator("#setPage-general select").count(), 0, "no native <select>");
      assert.equal(await pick.innerText(), names[lang]);
      const fits = await pick.evaluate((b) => { const r = b.closest(".row").getBoundingClientRect(), x = b.getBoundingClientRect(), s = b.querySelector("span"); return x.right <= r.right + 0.5 && x.left >= r.left && s.scrollWidth <= s.clientWidth + 1; });
      assert.ok(fits, "the language button doesn't fit its row");
      // the click moves nothing: the button stays under the pointer, even as
      // the page drawn in another language grows above it (the view scrolls
      // to hold it, as every click on Settings does)
      const where = () => pick.evaluate((b) => { const v = document.querySelector("#view-settings"); return { top: b.getBoundingClientRect().top, inPage: b.getBoundingClientRect().top - b.closest(".set-page, section, [id^=setPage-]").getBoundingClientRect().top, scrolled: v.scrollTop }; });
      // the page comes in animated: a place read mid-way isn't where it rests
      const rest = () => page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished.catch(() => {}))));
      await rest();
      const before = await where();
      const b = await pick.boundingBox();
      await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
      const items = page.locator(".proto-menu .pm-item");
      await items.first().waitFor();
      assert.equal(await pick.getAttribute("aria-expanded"), "true");
      const menu = await items.evaluateAll((bs) => bs.map((b) => ({ name: b.querySelector(".pm-name").textContent, on: b.classList.contains("on"), inside: b.getBoundingClientRect().right <= innerWidth && b.getBoundingClientRect().left >= 0 })));
      assert.deepEqual(menu.map((m) => m.name), [system[lang], "English", "简体中文", "繁體中文", "日本語", "Deutsch"]);
      assert.deepEqual(menu.filter((m) => m.on).map((m) => m.name), [names[lang]]);
      assert.ok(menu.every((m) => m.inside), `the menu runs off the window: ${JSON.stringify(menu)}`);
      // pick another language: saved, and the page drawn in it
      const to = lang === "ja" ? "de" : "ja";
      const item = items.nth(["system", "en", "zh", "zh-TW", "ja", "de"].indexOf(to));
      const ib = await item.boundingBox();
      await page.mouse.click(ib.x + ib.width / 2, ib.y + ib.height / 2);
      for (let i = 0; i < 50 && posted.at(-1)?.lang !== to; i++) await page.waitForTimeout(50);
      assert.equal(posted.at(-1)?.lang, to, `posted ${JSON.stringify(posted)}`);
      await page.waitForFunction(([n]) => document.querySelector("#langPick")?.textContent === n, [names[to]]);
      const head = page.locator("#setPage-general .row .name").first();
      for (let i = 0; i < 50 && (await head.innerText()) !== appearance[to]; i++) await page.waitForTimeout(50);
      assert.equal(await head.innerText(), appearance[to]);
      assert.equal(await page.locator(".proto-menu").count(), 0, "the menu stays open");
      await rest();
      const after = await where();
      assert.ok(Math.abs(after.inPage - before.inPage) <= 1, `the rows above the language moved: ${JSON.stringify([before, after])}`);
      // a view at its top can't scroll up to hold it as the header above shrinks
      if (after.scrolled > 0) assert.ok(Math.abs(after.top - before.top) <= 1, `the click moved the language button: ${JSON.stringify([before, after])}`);
      assert.deepEqual(errors, []);
    });
  }
}
