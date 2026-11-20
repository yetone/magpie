// Run with Node's test runner and Playwright on the module path; see README.md.
// After Restart only or Restart to update, WaiooT on X found the window back
// on Settings › About with nothing under the header and sidebar:
// - Settings that couldn't be read left every page of them blank, with only
//   the footer saying why for 8 s. The page now says so in the view, with
//   Try again, and reads them again by itself;
// - a view shown while the page was out of sight (the Mac's window hidden,
//   or not yet told it is shown) held its entrance at its first frame,
//   transparent. A view's entrance now plays out of sight too.
// Both engines, three languages; no backend, the API is faked here.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

const words = {
  en: { unread: "Couldn't read the settings", again: "Try again" },
  zh: { unread: "无法读取设置", again: "重试" },
  ja: { unread: "設定を読み込めませんでした", again: "再試行" },
};

async function open(engine, lang, failing) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const ctx = await browser.newContext({ viewport: { width: 1000, height: 760 } });
  await ctx.addInitScript(() => { try { localStorage.setItem("magpie.settingsTab", "about"); } catch {} });
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const fx = fixture(lang, "light", []);
  // reads of the settings fail until the test lets them through
  const reads = { settings: 0, failing };
  await page.route("**/*", (route) => {
    const u = new URL(route.request().url());
    if (u.pathname === "/api/settings" && route.request().method() === "GET" && reads.settings++ >= 0 && reads.failing) {
      return route.fulfill({ status: 502, contentType: "text/plain", body: "magpie is starting" });
    }
    return fx(route);
  });
  return { browser, page, errors, reads };
}

const aboutRows = (page) => page.locator("#about .row").count();

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, w] of Object.entries(words)) {
    test(`${engine} ${lang}: Settings that couldn't be read say so, and Try again reads them`, async (t) => {
      const { browser, page, errors, reads } = await open(engine, lang, true);
      t.after(() => browser.close());
      await page.goto("http://magpie.test/?view=settings");
      const line = page.locator("#view-settings #setUnread");
      await line.waitFor({ state: "visible" });
      assert.match(await line.innerText(), new RegExp(w.unread));
      assert.equal(await aboutRows(page), 0);
      reads.failing = false;
      await line.getByRole("button", { name: w.again }).click();
      await page.waitForFunction(() => document.querySelectorAll("#about .row").length > 0);
      assert.equal(await line.count(), 0, "the line goes once the settings are read");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: Settings that couldn't be read are read again by themselves`, async (t) => {
      const { browser, page, errors, reads } = await open(engine, lang, true);
      t.after(() => browser.close());
      await page.goto("http://magpie.test/?view=settings");
      await page.locator("#view-settings #setUnread").waitFor({ state: "visible" });
      reads.failing = false;
      // the next read is at most 4 s away
      await page.waitForFunction(() => document.querySelectorAll("#about .row").length > 0, null, { timeout: 15000 });
      assert.ok(reads.settings >= 2);
      assert.equal(await page.locator("#setUnread").count(), 0);
      assert.deepEqual(errors, []);
    });
  }

  test(`${engine}: a view shown out of sight is not left transparent`, async (t) => {
    const { browser, page, errors } = await open(engine, "en", false);
    t.after(() => browser.close());
    await page.goto("http://magpie.test/?view=agents");
    await page.waitForFunction(() => typeof window.magpieWindowVisibility === "function" && !!document.querySelector("#view-agents:not([hidden])"));
    await page.evaluate(() => { window.magpieWindowVisibility(false); show("settings"); });
    await page.waitForFunction(() => document.querySelectorAll("#about .row").length > 0);
    // the entrance is .24 s; well past it, the view is drawn whole
    await page.waitForTimeout(800);
    const seen = await page.evaluate(() => {
      const v = document.querySelector("#view-settings");
      return { hidden: v.hidden, opacity: getComputedStyle(v).opacity, outOfSight: document.documentElement.classList.contains("magpie-out-of-sight") };
    });
    assert.deepEqual(seen, { hidden: false, opacity: "1", outOfSight: true });
    assert.deepEqual(errors, []);
  });
}
