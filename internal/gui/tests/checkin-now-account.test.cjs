// Run with Node's test runner and Playwright on the module path; see README.md.
// xiaowu_95660 on Discord: a manual "check in once now" beside the daily
// check-in switch. The last row of each check-in list in its provider's
// editor, Check in now, checks one account in at a press (POST
// /api/usage/checkin-account {by, key}); with several the press opens the
// app's menu of them (no <select>), an account left out of the daily
// check-in among them. What came of it stays on the row's line: checked in,
// already in today, not eligible, or failed with the vendor's reason in
// red. No switch is written. Chromium and WebKit, 1100 and 420 wide, en,
// zh, zh-TW, ja, de; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, provPrefsProviders, openProvPrefs, inEditor } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const today = new Date(Date.now() + 8 * 3600e3).toISOString().slice(0, 10);
const WHY = "500 Internal Server Error: the check-in service is unavailable right now, try again later";

// answers is the reply to each press by account key, in order
function serve(lang, asked, answers) {
  const settings = { theme: "light", lang, quotaLeft: false, currency: "usd", workbuddy: true, workbuddyCheckin: true, trae: true, traeCheckin: false,
    checkinAccounts: [
      { key: "workbuddy|u1", by: "", user: "Ann", on: true },
      { key: "workbuddy|u2", by: "", user: "Bob", on: false },
      { key: "trae-cn|u9", by: "trae", user: "Cat", on: true },
    ] };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname.startsWith("/api/settings/")) {
      asked.push(["write", url.pathname]);
      return json(settings);
    }
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/usage/checkin-account") {
      const b = route.request().postDataJSON();
      asked.push([b.by, b.key]);
      const a = answers[b.key].shift();
      return a.error ? json(a, 400) : json(a);
    }
    if (url.pathname === "/api/providers") return json(provPrefsProviders(["workbuddy", "trae-cn"], { workbuddy: "WorkBuddy", "trae-cn": "Trae CN" }));
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const W = {
  en: { name: "Check in now", go: "Check in", pick: "Check in…", head: "Check in which account", claimed: "Checked in Bob +50 · 3-day streak", already: "Bob was already checked in today", ineligible: "Ann is not eligible · new users only", failed: "Couldn't check in Ann: " + WHY, trae: "Checked in Cat +100" },
  zh: { name: "立即签到", go: "签到", pick: "签到…", head: "给哪个账号签到", claimed: "Bob 已签到 +50 · 连续 3 天", already: "Bob 今天已经签过到了", ineligible: "Ann 不符合签到条件 · new users only", failed: "Ann 签到失败：" + WHY, trae: "Cat 已签到 +100" },
  "zh-TW": { name: "立即簽到", go: "簽到", pick: "簽到…", head: "給哪個帳號簽到", claimed: "Bob 已簽到 +50 · 連續 3 天", already: "Bob 今天已經簽過到了", ineligible: "Ann 不符合簽到條件 · new users only", failed: "Ann 簽到失敗：" + WHY, trae: "Cat 已簽到 +100" },
  ja: { name: "今すぐチェックイン", go: "チェックイン", pick: "チェックイン…", head: "チェックインするアカウント", claimed: "Bob をチェックインしました +50 · 3 日連続", already: "Bob は今日すでにチェックイン済みです", ineligible: "Ann は対象外です · new users only", failed: "Ann をチェックインできませんでした: " + WHY, trae: "Cat をチェックインしました +100" },
  de: { name: "Jetzt einchecken", go: "Einchecken", pick: "Einchecken…", head: "Welches Konto einchecken", claimed: "Bob eingecheckt +50 · 3 Tage in Folge", already: "Bob war heute schon eingecheckt", ineligible: "Ann ist nicht berechtigt · new users only", failed: "Ann konnte nicht eingecheckt werden: " + WHY, trae: "Cat eingecheckt +100" },
};

const at = (loc) => loc.evaluate((b) => Math.round(b.getBoundingClientRect().top));
const sideways = (page) => page.evaluate(() => {
  const out = [];
  const de = document.documentElement, v = document.querySelector(".editor .ebody") || document.querySelector(".editor");
  if (de.scrollWidth > de.clientWidth) out.push("page");
  if (v.scrollWidth > v.clientWidth) {
    // named by what sticks out past the editor's inner right edge, the
    // scrollbar's left
    const right = v.getBoundingClientRect().left + v.clientLeft + v.clientWidth;
    const past = [...v.querySelectorAll("*")].filter((e) => e.getClientRects().length && e.getBoundingClientRect().right > right + 0.5)
      .map((e) => e.tagName.toLowerCase() + (e.id ? "#" + e.id : "") + "." + [...e.classList].join(".") + "=" + Math.round(e.getBoundingClientRect().right - right));
    out.push("editor " + v.scrollWidth + ">" + v.clientWidth + " " + past.slice(0, 6).join(" "));
  }
  for (const r of document.querySelectorAll(".row.ci-now-row")) {
    if (!r.offsetParent) continue;
    const rr = r.getBoundingClientRect();
    for (const e of r.querySelectorAll(".name, .sub, button")) {
      const b = e.getBoundingClientRect();
      if (b.right > rr.right + 0.5 || b.left < rr.left - 0.5) out.push(r.dataset.by + " " + e.className);
    }
  }
  return out;
});
const launch = (engine) => engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" });
const subOf = (by) => `.ci-now-row[data-by="${by}"] .sub`;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(W)) {
    const w = W[lang];
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: Check in now checks one account in and says what came of it`, async (t) => {
        const browser = await launch(engine);
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], asked = [];
        page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); }); // a goto in WebKit can cut off a fetch (#1350)
        const answers = {
          "workbuddy|u2": [
            { user: "Bob", day: today, outcome: "claimed", credit: 50, streak: 3, asked: true },
            { user: "Bob", day: today, outcome: "claimed", credit: 50, streak: 3 },
          ],
          "workbuddy|u1": [
            { user: "Ann", day: today, outcome: "ineligible", msg: "new users only", asked: true },
            { user: "Ann", day: today, outcome: "failed", msg: WHY, asked: true },
          ],
          "trae-cn|u9": [{ user: "Cat", by: "trae", day: today, outcome: "claimed", credit: 100, asked: true }],
        };
        await page.route("**/*", serve(lang, asked, answers));
        await watchSettingsReads(page);
        await openProvPrefs(page, "workbuddy");
        await settingsDrawn(page);
        const row = page.locator('.editor .prov-prefs #wbList .ci-now-row[data-by=""]');
        await row.locator("button").waitFor();
        assert.equal((await row.locator(".name").innerText()).trim(), w.name);
        assert.equal(await page.evaluate(() => document.querySelector("#wbList").lastElementChild.classList.contains("ci-now-row")), true, "last in the list, under each account's own row");
        const go = row.locator("button");
        assert.equal((await go.innerText()).trim(), w.pick);
        assert.equal(await go.getAttribute("aria-haspopup"), "menu");
        assert.deepEqual(await sideways(page), []);
        await inEditor(page, go);
        // the button in the editor's middle, with room on both sides to be
        // held where it was pressed while a line above it changes height: at
        // the top there is nothing to scroll back when that line gets
        // shorter, at the end the end itself comes up
        await go.evaluate((b) => b.scrollIntoView({ block: "center" }));
        await page.evaluate(() => new Promise((ok) => requestAnimationFrame(() => requestAnimationFrame(ok))));
        const y = await at(go);

        // claimed now, for the account left out of the daily check-in
        await go.click();
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        assert.equal((await menu.locator(".pm-head").innerText()).trim(), w.head);
        assert.deepEqual((await menu.locator(".pm-name").allInnerTexts()).map((s) => s.trim()), ["Ann", "Bob"]);
        assert.deepEqual(asked, [], "the menu opening sent nothing");
        await menu.locator(".pm-item").nth(1).click();
        await page.waitForFunction(([s, want]) => document.querySelector(s)?.textContent === want, [subOf(""), w.claimed]);
        assert.deepEqual(asked, [["", "workbuddy|u2"]], "that account alone, by its key");
        assert.equal(await page.locator(subOf("") + ".err").count(), 0);
        if (process.env.ARTIFACT_DIR) await page.locator("#wbList").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `checkin-now-${engine}-${lang}-${width}-claimed.png`) });

        // already in today
        await go.click();
        await menu.locator(".pm-item").nth(1).click();
        await page.waitForFunction(([s, want]) => document.querySelector(s)?.textContent === want, [subOf(""), w.already]);

        // not eligible, with the vendor's reason
        await go.click();
        await menu.locator(".pm-item").nth(0).click();
        await page.waitForFunction(([s, want]) => document.querySelector(s)?.textContent === want, [subOf(""), w.ineligible]);
        assert.equal(await page.locator(subOf("") + ".err").count(), 0);

        // failed: the reason, in red, on the line and in the status
        await go.click();
        await menu.locator(".pm-item").nth(0).click();
        await page.waitForFunction((s) => document.querySelector(s + ".err"), subOf(""));
        assert.equal((await page.locator(subOf("")).innerText()).trim(), w.failed);
        assert.ok((await page.locator("#status").textContent()).includes("check-in service is unavailable"));
        assert.equal(await go.isDisabled(), false, "pressable again");
        if (process.env.ARTIFACT_DIR) await page.locator("#wbList").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `checkin-now-${engine}-${lang}-${width}-failed.png`) });
        const settled = () => page.evaluate(() => new Promise((ok) => requestAnimationFrame(() => requestAnimationFrame(ok))));
        await settled();
        assert.ok(Math.abs(await at(go) - y) <= 1, `a click moved the page: the button went from ${y} to ${await at(go)}`);
        assert.deepEqual(await sideways(page), []);

        // Trae CN: one account, checked in at a press, its switch off and left off
        await openProvPrefs(page, "trae-cn");
        await settingsDrawn(page);
        const trow = page.locator('.editor .prov-prefs #traeList .ci-now-row[data-by="trae"]');
        await inEditor(page, trow.locator("button"));
        assert.equal((await trow.locator("button").innerText()).trim(), w.go);
        assert.equal(await trow.locator("button").getAttribute("aria-haspopup"), null);
        await trow.locator("button").click();
        await page.waitForFunction(([s, want]) => document.querySelector(s)?.textContent === want, [subOf("trae"), w.trae]);
        if (process.env.ARTIFACT_DIR) await page.locator("#traeList").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `checkin-now-${engine}-${lang}-${width}-trae.png`) });

        assert.deepEqual(asked.filter((a) => a[0] === "write"), [], "a press wrote a switch");
        assert.equal(asked.length, 5);
        assert.deepEqual(await sideways(page), []);
        assert.equal(await page.locator("select").count(), 0);
        assert.deepEqual(errors, []);
      });
    }
  }

  test(`${engine}: no account, no Check in now`, async (t) => {
    const browser = await launch(engine);
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 900, height: 800 }, reducedMotion: "reduce" })).newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); }); // a goto in WebKit can cut off a fetch (#1350)
    const route = serve("en", [], {});
    await page.route("**/*", async (r) => {
      if (new URL(r.request().url()).pathname === "/api/settings") return r.fulfill({ json: { theme: "light", lang: "en", workbuddy: true, workbuddyCheckin: true, checkinAccounts: [] } });
      return route(r);
    });
    await openProvPrefs(page, "workbuddy");
    await page.locator(".editor .prov-prefs #wbCheckinSegs .opt").first().waitFor();
    assert.equal(await page.locator(".ci-now-row").count(), 0);
    assert.deepEqual(errors, []);
  });
}
