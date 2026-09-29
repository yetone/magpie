// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings → Version. A check leaves its button in place, dimmed, and a
// second click does nothing. The answer puts the button back. A read still
// out cannot draw "checking" over that answer, and a row drawn again while
// the check runs keeps the button and catches up. English and Chinese; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const words = {
  en: { version: "Version", check: "Check", checking: "Checking for updates…", latest: "Up to date", failed: "Couldn't check for updates · timed out" },
  zh: { version: "版本", check: "检查", checking: "正在检查更新…", latest: "已是最新", failed: "检查更新失败 · timed out" },
};

function settingsPayload(lang) {
  return {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
}

// ctl.update is what a finished check reports. While a check is held, a read
// still says checking, which is what a second click used to draw.
function server(lang, ctl) {
  const postWait = [], getWait = [];
  ctl.releasePosts = () => { for (const go of postWait.splice(0)) go(); };
  ctl.releaseGets = () => { for (const go of getWait.splice(0)) go(); };
  const cur = settingsPayload(lang);
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/settings") return json(cur);
    if (url.pathname === "/api/update/check") {
      ctl.posts++;
      ctl.inflight++;
      if (ctl.holdPost) await new Promise((go) => postWait.push(go));
      ctl.inflight--;
      return json({ ...ctl.update });
    }
    if (url.pathname === "/api/update") {
      ctl.gets++;
      const body = ctl.inflight ? { state: "checking", current: ctl.update.current || "0.1.400" } : { ...ctl.update };
      if (ctl.holdGets) await new Promise((go) => getWait.push(go));
      return json(body);
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

function fresh(over) {
  return { update: { state: "latest", current: "0.1.400" }, posts: 0, gets: 0, inflight: 0, holdPost: false, holdGets: false, ...over };
}

const viewTop = (page) => page.locator("#view-settings").evaluate((v) => v.scrollTop);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a version check keeps its button", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const pages = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-update-check-${i}.png`) });
      }
      await browser.close();
    });

    const open = async (lang, ctl) => {
      const errors = [];
      const page = await (await browser.newContext({ viewport: { width: 900, height: 420 }, reducedMotion: "reduce" })).newPage();
      pages.push(page);
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, ctl));
      await page.goto("http://magpie.test/?view=settings");
      return { page, errors };
    };

    const rowOf = (page, name) => page.locator("#about .row.pref", { has: page.locator(".name", { hasText: name }) }).first();

    // wheel the version row into sight; a later click must not move the page
    const showRow = async (page, row) => {
      await page.locator("#view-settings").hover();
      for (let i = 0; i < 40; i++) {
        const box = await row.boundingBox();
        const view = await page.locator("#view-settings").boundingBox();
        if (box && view && box.y >= view.y + 4 && box.y + box.height <= view.y + view.height - 4) break;
        await page.mouse.wheel(0, 180);
        await page.waitForTimeout(16);
      }
      await page.waitForTimeout(250);
      const top = await viewTop(page);
      assert(top > 0, "the settings page must be long enough to scroll");
      return top;
    };

    for (const lang of ["en", "zh"]) {
      const w = words[lang];
      await t.test(lang + ": two clicks, one check, the button stays", async () => {
        const ctl = fresh({ holdPost: true });
        const { page, errors } = await open(lang, ctl);
        const row = rowOf(page, w.version);
        const button = row.locator("button", { hasText: w.check });
        await button.waitFor();
        assert.equal(await button.isEnabled(), true);
        assert.equal((await row.locator(".sub").textContent()).trim(), w.latest);
        const top = await showRow(page, row);

        await button.click();
        assert.equal(await viewTop(page), top, "the click must not scroll the page");
        assert.equal(await button.isDisabled(), true, "the button stays, dimmed");
        assert.equal((await row.locator(".sub").textContent()).trim(), w.checking);
        assert.equal(ctl.posts, 1, "one check was asked for");
        // the handler itself, not the disabled button: a second click is ignored
        await button.evaluate((b) => { const fn = b.onclick; fn(); fn(); });
        await page.waitForTimeout(50);
        assert.equal(ctl.posts, 1, "a second click must not ask again");
        assert.equal(await row.locator("button").count(), 1);

        // a read that left while the check ran, answered "checking" only after
        // the check itself has answered — it must not be drawn over that
        ctl.holdGets = true;
        const gets = ctl.gets;
        await page.evaluate(() => { void renderUpdate(document.querySelector("#about .row.pref")); });
        for (let i = 0; i < 50 && ctl.gets === gets; i++) await page.waitForTimeout(20);
        assert(ctl.gets > gets, "the late read left before the answer");
        ctl.update = { state: "latest", current: "0.1.400" };
        ctl.releasePosts();
        await button.waitFor({ state: "attached" });
        await page.waitForFunction((latest) => {
          const sub = document.querySelector("#about .row.pref .sub");
          const b = document.querySelector("#about .row.pref button");
          return sub && sub.textContent.trim() === latest && b && !b.disabled;
        }, w.latest);
        assert.equal(await viewTop(page), top, "the answer must not scroll the page");
        ctl.releaseGets();
        await page.waitForTimeout(1200);
        assert.equal((await row.locator(".sub").textContent()).trim(), w.latest, "a late checking read must not replace the answer");
        assert.equal(await row.locator("button").count(), 1);
        assert.equal(await button.isEnabled(), true);
        assert.equal(ctl.posts, 1);
        assert.deepEqual(errors, []);
      });

      await t.test(lang + ": drawing the row again while a check runs", async () => {
        const ctl = fresh({ holdPost: true });
        const { page, errors } = await open(lang, ctl);
        const row = rowOf(page, w.version);
        const button = row.locator("button", { hasText: w.check });
        await button.waitFor();
        await showRow(page, row);
        await button.click();
        assert.equal(ctl.posts, 1);
        await page.evaluate(() => renderSettings());
        const again = rowOf(page, w.version);
        await again.locator("button", { hasText: w.check }).waitFor();
        assert.equal(await again.locator("button").isDisabled(), true, "the new row keeps the button while the check runs");
        assert.equal((await again.locator(".sub").textContent()).trim(), w.checking);
        assert.equal(ctl.posts, 1);
        ctl.update = { state: "latest", current: "0.1.400" };
        ctl.releasePosts();
        await page.waitForFunction((latest) => {
          const sub = document.querySelector("#about .row.pref .sub");
          const b = document.querySelector("#about .row.pref button");
          return sub && sub.textContent.trim() === latest && b && !b.disabled;
        }, w.latest);
        assert.equal(ctl.posts, 1);
        assert.deepEqual(errors, []);
      });

      await t.test(lang + ": a failed check gives the button back", async () => {
        const ctl = fresh();
        const { page, errors } = await open(lang, ctl);
        const row = rowOf(page, w.version);
        const button = row.locator("button", { hasText: w.check });
        await button.waitFor();
        assert.equal((await row.locator(".sub").textContent()).trim(), w.latest);
        await showRow(page, row);
        const top = await viewTop(page);
        ctl.update = { state: "error", current: "0.1.400", error: "timed out" };
        await button.click();
        await page.waitForFunction((failed) => document.querySelector("#about .row.pref .sub")?.textContent.trim() === failed, w.failed);
        assert.equal(await row.locator("button", { hasText: w.check }).isEnabled(), true);
        assert.equal(ctl.posts, 1);
        assert.equal(await viewTop(page), top);
        assert.deepEqual(errors, []);
      });

      await t.test(lang + ": a check already under way still shows the button", async () => {
        const ctl = fresh({ update: { state: "checking", current: "0.1.400" } });
        const { page, errors } = await open(lang, ctl);
        const row = rowOf(page, w.version);
        const button = row.locator("button", { hasText: w.check });
        await button.waitFor();
        assert.equal(await button.isDisabled(), true);
        assert.equal((await row.locator(".sub").textContent()).trim(), w.checking);
        ctl.update = { state: "latest", current: "0.1.400" };
        await page.waitForFunction((latest) => {
          const sub = document.querySelector("#about .row.pref .sub");
          const b = document.querySelector("#about .row.pref button");
          return sub && sub.textContent.trim() === latest && b && !b.disabled;
        }, w.latest);
        assert.equal(ctl.posts, 0, "watching a check already under way does not start another");
        assert.deepEqual(errors, []);
      });
    }
  });
}
