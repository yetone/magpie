// Run with Node's test runner and Playwright on the module path; see README.md.
// Warm up now (a Windows user on Discord): the last row of Codex's and
// Claude Code's warm-ups, in each one's provider editor (they were tabs in
// Settings › Usage until the owner moved them on 11-07), sends one account
// the warm-up's request at once (POST /api/warm-now {agent, user}). One
// account goes at a press; with more the press opens the app's menu of
// them (no <select>). What it came to stays on the row's line: done, or
// failed with the vendor's reason, in red. The row is hidden with no
// account; a click never moves the page; nothing runs off the side at
// 440px. en, zh, zh-TW, ja, de; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { provPrefsProviders, openProvPrefs, inEditor } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const WHY = "429 Too Many Requests: {\"detail\":\"usage limit reached\"} https://chatgpt.com/backend-api/codex/responses-with-a-very-long-unbroken-reason";

function serve(lang, asked, codexUsers, claudeUsers) {
  const settings = { theme: "light", lang, currency: "usd", codexWarmup: "", codexUsers, claudeUsers };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/warm-now") {
      const b = route.request().postDataJSON();
      asked.push([b.agent, b.user]);
      return b.user === "a@example.com" ? json({ error: WHY }, 400) : json({ ok: true });
    }
    if (url.pathname === "/api/providers") return json(provPrefsProviders(["codex", "claude"], { codex: "Codex", claude: "Claude Code" }));
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const W = {
  en: { name: "Warm up now", go: "Warm up", pick: "Warm up…", head: "Warm up which account", done: "Warmed up B@Example.com at ", failed: "Couldn't warm up a@example.com: ", own: "Warmed up own@example.com at " },
  zh: { name: "现在预热", go: "预热", pick: "预热…", head: "预热哪个账号", done: "已预热 B@Example.com，", failed: "a@example.com 预热失败：", own: "已预热 own@example.com，" },
  "zh-TW": { name: "現在預熱", go: "預熱", pick: "預熱…", head: "預熱哪個帳號", done: "已預熱 B@Example.com，", failed: "a@example.com 預熱失敗：", own: "已預熱 own@example.com，" },
  ja: { name: "今すぐウォームアップ", go: "ウォームアップ", pick: "ウォームアップ…", head: "ウォームアップするアカウント", done: "B@Example.com をウォームアップしました（", failed: "a@example.com をウォームアップできませんでした: ", own: "own@example.com をウォームアップしました（" },
  de: { name: "Jetzt vorwärmen", go: "Vorwärmen", pick: "Vorwärmen…", head: "Welches Konto vorwärmen", done: "B@Example.com vorgewärmt, ", failed: "a@example.com konnte nicht vorgewärmt werden: ", own: "own@example.com vorgewärmt, " },
};

// where the button the reader pressed is on the screen: it stays there while
// the line beside it grows to hold a reason (gui-shell.md, "A click never
// moves"), so the view's scrollTop may change, the button may not
const at = (loc) => loc.evaluate((b) => Math.round(b.getBoundingClientRect().top));
const sideways = (page) => page.evaluate(() => {
  const out = [];
  const de = document.documentElement, v = document.querySelector(".editor .ebody") || document.querySelector(".editor");
  if (de.scrollWidth > de.clientWidth) out.push("page");
  if (v.scrollWidth > v.clientWidth) out.push("view");
  for (const r of document.querySelectorAll(".row.warm-now:not([hidden])")) {
    const rr = r.getBoundingClientRect();
    for (const e of r.querySelectorAll(".name, .sub, button")) {
      const b = e.getBoundingClientRect();
      if (b.right > rr.right + 0.5 || b.left < rr.left - 0.5) out.push(r.id + " " + e.className);
    }
  }
  return out;
});
const launch = (engine) => engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" });

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    const w = W[lang];
    for (const width of [900, 440]) {
      test(`${engine} ${lang} ${width}px: Warm up now sends one account's warm-up and says what it came to`, async (t) => {
        const browser = await launch(engine);
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], asked = [];
        page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); }); // a goto in WebKit can cut off a fetch (#1350)
        await page.route("**/*", serve(lang, asked, ["a@example.com", "B@Example.com"], ["own@example.com"]));
        await openProvPrefs(page, "codex");
        const row = page.locator(".editor .prov-prefs #warmNowRow");
        await row.locator("button").waitFor();
        assert.equal((await row.locator(".name").innerText()).trim(), w.name);
        // last of Codex's warm-ups, under each account's own daily row
        assert.equal(await page.evaluate(() => document.querySelector("#codexWarmList").lastElementChild.id), "warmNowRow");
        assert.equal(await page.locator("#codexWarmList .warm-own").count(), 2);
        const go = row.locator("button");
        assert.equal((await go.innerText()).trim(), w.pick);
        assert.equal(await go.getAttribute("aria-haspopup"), "menu");
        assert.deepEqual(await sideways(page), []);

        await inEditor(page, go);
        const y = await at(go);
        // two accounts: the press asks which, in the app's menu
        await go.click();
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        assert.equal((await menu.locator(".pm-head").innerText()).trim(), w.head);
        assert.deepEqual((await menu.locator(".pm-name").allInnerTexts()).map((s) => s.trim()), ["a@example.com", "B@Example.com"]);
        assert.deepEqual(asked, [], "the menu opening sent nothing");
        await menu.locator(".pm-item").nth(1).click();
        await page.waitForFunction((s) => document.querySelector("#warmNowSub")?.textContent.startsWith(s), w.done);
        assert.deepEqual(asked, [["codex", "B@Example.com"]], "that account alone, by its name as it is");
        assert.equal(await page.locator("#warmNowSub.err").count(), 0);
        if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `warm-now-${engine}-${lang}-${width}-done.png`) });

        // refused: the vendor's reason, in red, on the line and in the status
        await go.click();
        await menu.locator(".pm-item").nth(0).click();
        await page.waitForFunction(() => document.querySelector("#warmNowSub.err"));
        assert.equal((await page.locator("#warmNowSub").innerText()).trim(), w.failed + WHY);
        assert.ok((await page.locator("#status").textContent()).includes("usage limit reached"));
        assert.deepEqual(asked, [["codex", "B@Example.com"], ["codex", "a@example.com"]]);
        assert.equal(await go.isDisabled(), false, "pressable again");
        if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `warm-now-${engine}-${lang}-${width}-failed.png`) });
        // the click guard puts a redraw's move back before it's painted, a
        // frame later in WebKit: the button's place once that frame is done
        const settled = () => page.evaluate(() => new Promise((ok) => requestAnimationFrame(() => requestAnimationFrame(ok))));
        await settled();
        assert.ok(Math.abs(await at(go) - y) <= 1, `a click moved the page: the button went from ${y} to ${await at(go)}`);
        assert.deepEqual(await sideways(page), []);

        // Claude Code's editor: one account, sent at a press
        await openProvPrefs(page, "claude");
        const crow = page.locator(".editor .prov-prefs #claudeWarmNowRow");
        await crow.locator("button").waitFor();
        await inEditor(page, crow.locator("button"));
        assert.equal((await crow.locator("button").innerText()).trim(), w.go);
        assert.equal(await crow.locator("button").getAttribute("aria-haspopup"), null);
        await crow.locator("button").click();
        await page.waitForFunction((s) => document.querySelector("#claudeWarmNowSub")?.textContent.startsWith(s), w.own);
        assert.deepEqual(asked.at(-1), ["claude", "own@example.com"]);
        assert.equal(asked.length, 3);
        assert.deepEqual(await sideways(page), []);
        assert.equal(await page.locator("select").count(), 0);
        assert.deepEqual(errors, []);
      });
    }
  }

  test(`${engine}: no account, no Warm up now`, async (t) => {
    const browser = await launch(engine);
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 900, height: 800 }, reducedMotion: "reduce" })).newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); }); // a goto in WebKit can cut off a fetch (#1350)
    await page.route("**/*", serve("en", [], undefined, undefined));
    await openProvPrefs(page, "codex");
    await page.locator(".editor .prov-prefs #warmAtSegs .opt").first().waitFor();
    assert.equal(await page.locator("#warmNowRow").isHidden(), true);
    await openProvPrefs(page, "claude");
    await page.locator(".editor .prov-prefs #claudeWarmAtSegs .opt").first().waitFor();
    assert.equal(await page.locator("#claudeWarmNowRow").isHidden(), true);
    assert.deepEqual(errors, []);
  });
}
