// Run with Node's test runner and Playwright on the module path; see README.md.
// What's new (a Discord user: after an update, show what changed, to see
// whether their issue was fixed). After an upgrade the window opens a dialog
// with the notes of every release since, newest first, once: the app is told
// they were seen, and a reload doesn't show them again. A fresh install shows
// nothing, nor does the tray's panel; magpie web's page shows it as the
// window does, and opens its links in a tab. #464 links the issue and opens it in
// the browser through the app, as markdown links do; a javascript: link is
// text, and HTML in the notes is shown as text, never run. Settings' What's new
// row, under Version, opens them again, the waiting update's first. No click moves the
// page, no left-border accent. In English and Chinese, Chromium and WebKit;
// the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const NOTES_604 = [
  "## Bug Fixes",
  "",
  "- Fixed **model picks** leaking between editors in `providers.json`. (#464)",
  "- <img src=x onerror=\"window.__pwned=1\"> <b>raw</b> tags stay text. (#450)",
  "",
  "## Improvements",
  "",
  "- See [the guide](https://usemagpie.ai/docs) and [not a link](javascript:window.__pwned=2).",
].join("\n");
const RELEASES = [
  { version: "0.1.604", notes: NOTES_604, url: "https://github.com/yetone/magpie-releases/releases/tag/v0.1.604" },
  { version: "0.1.603", notes: "## Bug Fixes\n\n- Fixed reading Kimi keys. (#446)" },
];

function settingsPayload(lang) {
  return {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.604", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
}

// ctl.upgraded: magpie started on a newer version than it last ran
function serve(lang, ctl) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${!!ctl.web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/settings") return json(settingsPayload(lang));
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/update") return json(ctl.update);
    if (url.pathname === "/api/whatsnew") {
      const all = url.searchParams.has("all");
      ctl.asked.push(all ? "all" : "start");
      const show = ctl.upgraded && !ctl.seen && !all;
      return json({ show, current: "0.1.604", releases: show || (all && ctl.upgraded) ? RELEASES : all ? [RELEASES[0]] : [] });
    }
    if (url.pathname === "/api/whatsnew/seen") { ctl.seen++; return route.fulfill({ status: 204 }); }
    if (url.pathname === "/api/open") { ctl.opened.push(req.postDataJSON().url); return route.fulfill({ status: 204 }); }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { title: "What's new in v0.1.604", pendingTitle: "What's new in v0.1.605", close: "Close", again: "What's new", open: "Open", pending: "Not installed yet" },
  zh: { title: "v0.1.604 更新内容", pendingTitle: "v0.1.605 更新内容", close: "关闭", again: "更新内容", open: "打开", pending: "尚未安装" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: what's new after an update`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" });
      t.after(async () => browser.close());
      const open = async (ctl, q = "") => {
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        page.on("dialog", (d) => { errors.push("dialog: " + d.message()); d.dismiss(); });
        await page.route("**/*", serve(lang, ctl));
        await page.goto("http://magpie.test/" + q);
        await page.locator("#agents").waitFor({ state: "attached" });
        return { page, errors };
      };
      const fresh = (over) => ({ upgraded: false, seen: 0, asked: [], opened: [], update: { state: "latest", current: "0.1.604" }, ...over });
      const dialog = (page) => page.locator("#modal .editor.whatsnew");

      // a fresh install: nothing shown, nothing marked seen
      {
        const ctl = fresh();
        const { page, errors } = await open(ctl);
        for (let i = 0; i < 40 && !ctl.asked.length; i++) await page.waitForTimeout(25);
        await page.waitForTimeout(300);
        assert.deepEqual(ctl.asked, ["start"], "the window asks once");
        assert.equal(await dialog(page).count(), 0, "no dialog on a fresh install");
        assert.equal(ctl.seen, 0);
        assert.deepEqual(errors, []);
        await page.close();
      }

      // magpie web's page shows it too, and opens a link in a tab of its own
      {
        const ctl = fresh({ upgraded: true, web: true });
        const { page, errors } = await open(ctl);
        await dialog(page).waitFor();
        await page.evaluate(() => { window.open = (u) => { window.__opened = u; return null; }; });
        await dialog(page).getByRole("link", { name: "#446" }).click();
        assert.equal(await page.evaluate(() => window.__opened), "https://github.com/yetone/magpie/issues/446");
        assert.equal(page.url(), "http://magpie.test/");
        assert.equal(ctl.seen, 1);
        assert.deepEqual(errors, []);
        await page.close();
      }

      // the tray's panel never shows it, nor asks
      {
        const ctl = fresh({ upgraded: true });
        const { page, errors } = await open(ctl, "?mode=panel");
        await page.waitForTimeout(500);
        assert.equal(await dialog(page).count(), 0, "no dialog in the panel");
        assert.deepEqual(ctl.asked, []);
        assert.deepEqual(errors, []);
        await page.close();
      }

      // upgraded: the notes since, newest first, once
      const ctl = fresh({ upgraded: true });
      const { page, errors } = await open(ctl);
      await dialog(page).waitFor();
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.waitForTimeout(400);
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-whats-new.png`) });
      }
      assert.equal(await dialog(page).locator(".ehead b").textContent(), w.title);
      assert.deepEqual(await dialog(page).locator(".wn-ver b").allTextContents(), ["v0.1.604", "v0.1.603"]);
      const notes = dialog(page).locator(".wn-rel").first();
      assert.deepEqual(await notes.locator(".wn-h").allTextContents(), ["Bug Fixes", "Improvements"]);
      assert.equal(await notes.locator("li").count(), 3);
      assert.equal(await notes.locator("li b").first().textContent(), "model picks");
      assert.equal(await notes.locator("li code").first().textContent(), "providers.json");
      // HTML is text
      assert.equal(await dialog(page).locator("img, li > b:has-text('raw')").count(), 0, "no HTML from the notes");
      assert((await notes.locator("li").nth(1).textContent()).startsWith('<img src=x onerror="window.__pwned=1"> <b>raw</b> tags stay text.'));
      // links: issues, markdown http(s) links; javascript: is text
      const issue = notes.getByRole("link", { name: "#464" });
      assert.equal(await issue.getAttribute("href"), "https://github.com/yetone/magpie/issues/464");
      assert.equal(await dialog(page).getByRole("link", { name: "#446" }).getAttribute("href"), "https://github.com/yetone/magpie/issues/446");
      assert.equal(await notes.getByRole("link", { name: "the guide" }).getAttribute("href"), "https://usemagpie.ai/docs");
      assert.equal(await notes.getByRole("link", { name: "not a link" }).count(), 0, "a javascript: link is not a link");
      assert.equal(await dialog(page).locator("a[href^='javascript']").count(), 0);
      assert.equal(ctl.seen, 1, "the app is told they were seen");
      // a link opens in the browser through the app, and moves nothing
      const body = dialog(page).locator(".ebody");
      const before = await body.evaluate((e) => [e.scrollTop, document.scrollingElement.scrollTop]);
      await issue.click();
      for (let i = 0; i < 40 && !ctl.opened.length; i++) await page.waitForTimeout(25);
      assert.deepEqual(ctl.opened, ["https://github.com/yetone/magpie/issues/464"]);
      assert.deepEqual(await body.evaluate((e) => [e.scrollTop, document.scrollingElement.scrollTop]), before, "the click moved the page");
      assert.equal(page.url(), "http://magpie.test/", "the page didn't navigate");
      assert.equal(await page.evaluate(() => window.__pwned), undefined, "nothing in the notes ran");
      const border = await page.evaluate(() => [...document.querySelectorAll("#modal .whatsnew, #modal .whatsnew *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no left-border accent");
      // dismissed: Close, and Escape on the next one
      await dialog(page).getByRole("button", { name: w.close }).click();
      await page.locator("#modal").waitFor({ state: "hidden" });
      // seen: a reload asks and shows nothing
      await page.reload();
      await page.waitForTimeout(500);
      assert.equal(await dialog(page).count(), 0, "shown once");

      // Settings → What's new → Open opens them again, the waiting update's first
      ctl.update = { state: "ready", current: "0.1.604", latest: "0.1.605", notes: "## New Features\n\n- A new thing. (#470)", url: "https://github.com/yetone/magpie-releases/releases/tag/v0.1.605" };
      await page.goto("http://magpie.test/?view=settings&tab=about");
      const notesRow = page.locator("#about .row.pref.whatsnew-row");
      assert.equal(await notesRow.locator(".name").textContent(), w.again);
      const again = notesRow.getByRole("button", { name: w.open, exact: true });
      await again.waitFor();
      const view = page.locator("#view-settings");
      // brought into sight by the wheel, as a reader does: the page puts back
      // a scroll no wheel, key or drag asked for
      await page.mouse.move(450, 400);
      for (let i = 0; i < 60; i++) {
        const b = await again.boundingBox();
        if (b && b.y > 80 && b.y + b.height < 640) break;
        await page.mouse.wheel(0, !b || b.y > 80 ? 120 : -120);
        await page.waitForTimeout(30);
      }
      await page.waitForTimeout(200);
      const at = await view.evaluate((v) => v.scrollTop);
      await again.click();
      await dialog(page).waitFor();
      assert.equal(await view.evaluate((v) => v.scrollTop), at, "the click moved the page");
      assert.equal(await dialog(page).locator(".ehead b").textContent(), w.pendingTitle);
      assert.deepEqual(await dialog(page).locator(".wn-ver b").allTextContents(), ["v0.1.605", "v0.1.604", "v0.1.603"]);
      assert.equal(await dialog(page).locator(".wn-rel").first().locator(".badge").textContent(), w.pending);
      assert.equal(ctl.asked.at(-1), "all");
      assert.equal(ctl.seen, 1, "asking again isn't a first showing");
      await page.keyboard.press("Escape");
      await page.locator("#modal").waitFor({ state: "hidden" });

      const missing = await page.evaluate(() => ["What's new", "What's new in {v}", "The release notes since the last update", "Not installed yet", "Couldn't load the release notes"].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
