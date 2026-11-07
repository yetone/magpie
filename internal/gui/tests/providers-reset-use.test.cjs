// Run with Node's test runner and Playwright on the module path; see README.md.
// A Codex account's "↺ 2 resets" on its Providers editor row uses one, as
// Usage's "Use a reset" does (roamlog on X: 使用重置卡的功能哪去了 — the
// row showed the resets and offered no way to use them). Clicked, or Enter
// on it, it opens the same confirmation; Cancel spends nothing; confirmed,
// the reset is asked for that account alone and the row reads again. A
// GLM team's resets, counted by window and used on the vendor's page, stay
// words. Codex's editor's "Auto-use resets" lists the same
// accounts, and husky_bobo (X, Windows 10) looked for the use there: each
// account holding resets says so under its name with Use beside it, both
// auto-use times on as theirs were, as the Windows app (navigator.platform
// Win32, not a web page). Every language, Chromium and WebKit, a phone's
// width and a desk's; no backend, the API is faked here and nothing real is
// spent.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, openProvPrefs, inEditor } = require("./reader.cjs");

const scrolls = (page) => page.evaluate(() => [scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join(","));

const assets = path.resolve(process.env.ASSET_DIR || path.resolve(__dirname, "../assets"));
const later = new Date(Date.now() + 3 * 864e5).toISOString();
const user = "me@example.com";

function serve(lang, posts, web = true) {
  let spent = false;
  const windows = () => [{ name: "5 hours", used: spent ? 0 : 100, resetsAt: later }, { name: "7 days", used: spent ? 0 : 62, resetsAt: later }];
  const prov = (id, name, agent, plan) => ({
    id, name, icon: id, chat: "", responses: "", anthropic: "", catalog: "",
    models: [{ id: "m-1", name: "", on: true }], agents: [], fallback: [], headers: {}, keyList: [], proxy: "",
    account: { agent, agentName: name, user, plan, logins: [{ user, plan, active: true, on: true }] },
  });
  const providers = [prov("codex", "Codex", "codex", "PLUS"), prov("zcode", "GLM Team", "zcode", "TEAM")];
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", quotaLeft: true, codexAutoReset: [user], codexAutoResetOnly: {} } });
    // husky_bobo's: both times on for the account
    if (url.pathname === "/api/settings") return json({ lang, theme: "light", quotaLeft: true, version: "0.1.2597", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425", fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, codexUsers: [user], codexAutoReset: [user], codexAutoResetOnly: {}, codexResetLead: 30 });
    if (url.pathname === "/api/providers") return json({ providers, presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/login/usage") {
      const agent = url.searchParams.get("agent");
      if (agent === "codex") return json({ [user]: { provider: "codex", windows: windows(), resets: { count: spent ? 1 : 2, until: later } } });
      if (agent === "zcode") return json({ [user]: { provider: "zcode", windows: windows(), resets: { byWindow: true, count: 2, fiveHour: 1, weekly: 1, team: true } } });
      return json({});
    }
    if (url.pathname === "/api/usage/codex-reset") {
      posts.push(req.postDataJSON());
      spent = true;
      return json({ code: "reset", windows: 2 });
    }
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json(url.pathname === "/api/usage/quotas" ? [] : {});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [420, 1000]) {
      test(`${engine} ${lang} ${width}px: a Codex account's resets on its Providers row use one`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [];
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");
        const w = await page.evaluate((lang) => {
          const tr = (s) => (lang === "en" ? s : I18N[lang]?.[s]);
          return { two: "↺ " + tr("{n} resets")?.replace("{n}", 2), one: "↺ " + tr("1 reset"), use: tr("Use a reset"), ask: tr("Use a Codex reset?"), cancel: tr("Cancel") };
        }, lang);
        for (const [k, v] of Object.entries(w)) assert.ok(v && !v.includes("undefined"), `${lang} has no words for ${k}`);

        await page.locator(".row.provider", { hasText: "Codex" }).first().click();
        const chip = page.locator(`.editor .acc[data-account-id="${user}"] .aq-resets`);
        await chip.waitFor();
        assert.equal((await chip.textContent()).trim(), w.two);
        assert.equal(await chip.getAttribute("role"), "button", "the resets are words only, nothing uses one");
        assert.equal(await chip.getAttribute("aria-haspopup"), "dialog");
        // its tooltip says which one a use spends, before anything is asked
        assert.match(await chip.getAttribute("title"), /\n\n/);
        // the same quiet look as the window meters' buttons: no colour of its own
        const look = await chip.evaluate((n) => { const s = getComputedStyle(n); return { color: s.color, border: s.borderLeftWidth, bg: s.backgroundColor }; });
        const meterColor = await page.locator(`.editor .acc[data-account-id="${user}"] .aq`).evaluate((n) => getComputedStyle(n).color);
        assert.equal(look.color, meterColor);
        assert.equal(look.border, "0px");
        assert.equal(look.bg, "rgba(0, 0, 0, 0)");
        // the row stays one line high where the meters fit: the chip is no taller than the meters
        const box = await chip.boundingBox();
        assert.ok(box.height < 24, `chip is ${box.height}px high`);

        // Cancel spends nothing
        const y = await page.evaluate(() => document.scrollingElement.scrollTop);
        await chip.click();
        const ask = page.locator(".reset-ask");
        await ask.waitFor();
        assert.equal((await ask.locator(".ehead b").textContent()).trim(), w.ask);
        assert.ok((await ask.textContent()).includes(user), "the confirmation doesn't name the account");
        await ask.locator("button", { hasText: w.cancel }).click();
        await ask.waitFor({ state: "detached" });
        assert.deepEqual(posts, []);
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), y, "the click moved the page");
        // the editor is still open: the click didn't reach the row behind it
        await chip.waitFor();

        // from the keyboard, then confirmed: that account's reset, once
        await chip.focus();
        await page.keyboard.press("Enter");
        await ask.waitFor();
        await ask.locator("button.primary", { hasText: w.use }).click();
        await ask.waitFor({ state: "detached" });
        assert.deepEqual(posts, [{ user }]);
        await page.waitForFunction((one) => document.querySelector('.editor .acc[data-account-id="me@example.com"] .aq-resets')?.textContent.trim() === one, w.one, { timeout: 3000 })
          .catch(async () => assert.fail(`the row still says ${(await chip.textContent()).trim()}`));

        // a GLM team's resets are used on the vendor's page: words only
        await page.keyboard.press("Escape");
        await page.locator(".editor").waitFor({ state: "detached" });
        await page.locator(".row.provider", { hasText: "GLM Team" }).first().click();
        const glm = page.locator(`.editor .acc[data-account-id="${user}"] .aq-resets`);
        await glm.waitFor();
        assert.equal(await glm.getAttribute("role"), null);
        await glm.click();
        assert.equal(await page.locator(".reset-ask").count(), 0);
        assert.equal(posts.length, 1);
        assert.deepEqual(errors, []);
      });
    }
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [420, 1100]) {
      test(`${engine} ${lang} ${width}px Windows: the Codex editor's auto-use rows use a reset`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [];
        await page.addInitScript(() => Object.defineProperty(navigator, "platform", { value: "Win32" }));
        await page.route("**/*", serve(lang, posts, false));
        await watchSettingsReads(page);
        await openProvPrefs(page, "codex");
        await settingsDrawn(page);
        const w = await page.evaluate((lang) => {
          const tr = (s) => (lang === "en" ? s : I18N[lang]?.[s]);
          return { two: "↺ " + tr("{n} resets")?.replace("{n}", 2), one: "↺ " + tr("1 reset"), use: tr("Use a reset"), cancel: tr("Cancel"), kept: tr("auto-used before it expires") };
        }, lang);
        for (const [k, v] of Object.entries(w)) assert.ok(v && !v.includes("undefined"), `${lang} has no words for ${k}`);
        const row = page.locator(`.editor .prov-prefs .ar-own[data-user="${user}"]`);
        const held = row.locator(".ar-own-resets");
        await held.waitFor();
        assert.equal((await held.locator(".resets-n").textContent()).trim(), w.two);
        assert.ok((await held.textContent()).includes(w.kept), "doesn't say the one running out is auto-used");
        const use = held.locator("button", { hasText: w.use });
        // nothing runs past the editor
        const over = await page.evaluate(() => {
          const v = document.querySelector(".editor .prov-prefs").getBoundingClientRect();
          return [...document.querySelectorAll(".ar-own, .ar-own-resets, .ar-own-resets button")].filter((n) => n.getBoundingClientRect().right > v.right + 1).length;
        });
        assert.equal(over, 0, "a row runs past the editor");
        await inEditor(page, use);
        await use.click();
        const ask = page.locator(".reset-ask");
        await ask.waitFor();
        // where the click left it
        const top = await scrolls(page);
        await ask.locator("button", { hasText: w.cancel }).click();
        await ask.waitFor({ state: "detached" });
        assert.deepEqual(posts, []);
        assert.equal(await scrolls(page), top, "Cancel moved the page");
        await use.click();
        await ask.waitFor();
        await ask.locator("button.primary", { hasText: w.use }).click();
        await ask.waitFor({ state: "detached" });
        assert.deepEqual(posts, [{ user }]);
        await page.waitForFunction(({ one, user }) => document.querySelector(`.editor .ar-own[data-user="${user}"] .ar-own-resets .resets-n`)?.textContent.trim() === one, { one: w.one, user }, { timeout: 3000 })
          .catch(async () => assert.fail(`the editor still says ${(await held.textContent()).trim()}`));
        assert.equal(await scrolls(page), top, "a click moved the page");
        assert.deepEqual(errors, []);
      });
    }
  }
}
