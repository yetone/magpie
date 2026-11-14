// Run with Node's test runner and Playwright on the module path; see README.md.
// The header's Update pill can be kept away (Wang Hsiaohi on Discord). Its ×,
// shown with the pointer over it, hides it for 3 days: it stays away after a
// reload and through the releases out meanwhile (a release comes every few
// minutes), and comes back once the 3 days are over; a right-click
// does the same, and the × sits off the pill, so a click meant for Update
// never lands on it (#2291). Settings → Update button turns it off for good, and says
// that it is hidden for 3 days, with a way to show it again. The version
// row still offers the restart. No click moves the page. English and
// Chinese, in the tray panel and the window; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

const words = {
  en: { update: "Update", hide: "Hide for 3 days", pill: "Update button", off: "Off", on: "On",
    hidden: "Hidden for 3 days", again: "Show again", restart: "Restart to update", version: "Version" },
  zh: { update: "更新", hide: "隐藏 3 天", pill: "更新按钮", off: "关闭", on: "开启",
    hidden: "已隐藏 3 天", again: "重新显示", restart: "重启以更新", version: "版本" },
};

function settingsPayload(lang, ctl) {
  return {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    noUpdatePill: ctl.off, updateSkip: ctl.skip,
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
}

// ctl holds what the backend would: the version out, the two settings, and
// whether the × still holds (hushed: /api/update's quiet)
function server(lang, ctl) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settingsPayload(lang, ctl), fx: settingsPayload(lang, ctl).fx });
    if (url.pathname === "/api/settings/update-skip") {
      ctl.skips.push(req.postDataJSON().version);
      ctl.skip = req.postDataJSON().version;
      ctl.hushed = !!ctl.skip;
      return json(settingsPayload(lang, ctl));
    }
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        ctl.off = !!body.noUpdatePill;
        ctl.saves.push(body);
      }
      return json(settingsPayload(lang, ctl));
    }
    if (url.pathname === "/api/update") return json({ state: "ready", current: "0.1.400", latest: ctl.latest, ...(ctl.hushed ? { quiet: true } : {}) });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const fresh = (over) => ({ latest: "0.1.401", off: false, skip: "", hushed: false, skips: [], saves: [], ...over });
// wheel a row into sight, as a user would; a click must then not move the page
async function showRow(page, row) {
  await page.locator("#view-settings").hover();
  for (let i = 0; i < 40; i++) {
    const box = await row.boundingBox();
    const view = await page.locator("#view-settings").boundingBox();
    if (box && view && box.y >= view.y + 4 && box.y + box.height <= view.y + view.height - 4) break;
    await page.mouse.wheel(0, 180);
    await page.waitForTimeout(16);
  }
  await page.waitForTimeout(250);
}
const scrolls = (page) => page.evaluate(() => [window.scrollY, document.scrollingElement.scrollTop, ...[...document.querySelectorAll(".view")].map((v) => v.scrollTop)].join(","));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Update pill can be hidden", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const open = async (lang, ctl, query) => {
      const errors = [];
      const page = await (await browser.newContext({ viewport: { width: query.includes("panel") ? 420 : 900, height: 560 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      // WebKit's notice when a layout outruns its observers, not a fault
      page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
      await page.route("**/*", server(lang, ctl));
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/" + query);
      await settingsDrawn(page); // rows found are the ones that stay (#1659)
      return { page, errors };
    };
    for (const lang of ["en", "zh"]) {
      const w = words[lang];
      await t.test(lang + ": the × hides it for 3 days, through newer versions", async () => {
        const ctl = fresh();
        const { page, errors } = await open(lang, ctl, "?mode=panel");
        const pill = page.locator("#update"), x = page.locator("#updateHide");
        await pill.waitFor({ state: "visible" });
        assert.equal((await pill.innerText()).trim(), w.update);
        assert.equal(await x.getAttribute("aria-label"), w.hide);
        assert.equal(await x.getAttribute("title"), w.hide);
        // out of sight until the pointer is over the pill
        assert.equal(await x.evaluate((e) => getComputedStyle(e).opacity), "0");
        await pill.hover();
        await page.waitForFunction(() => getComputedStyle(document.querySelector("#updateHide")).opacity === "1");
        const before = await scrolls(page);
        await x.click();
        await pill.waitFor({ state: "hidden" });
        assert.deepEqual(ctl.skips, ["0.1.401"]);
        assert.equal(await scrolls(page), before, "the click must not scroll the page");
        assert.equal(await x.isVisible(), false);
        await page.reload();
        await page.locator("#sync").waitFor();
        await page.waitForTimeout(400);
        assert.equal(await pill.isHidden(), true, "still hidden for 0.1.401");
        ctl.latest = "0.1.402";
        await page.reload();
        await page.locator("#sync").waitFor();
        await page.waitForTimeout(400);
        assert.equal(await pill.isHidden(), true, "a release minutes later brought it back");
        ctl.hushed = false; // the 3 days are over
        await page.reload();
        await pill.waitFor({ state: "visible" });
        // and a right-click hides it as well
        await pill.click({ button: "right" });
        await pill.waitFor({ state: "hidden" });
        assert.deepEqual(ctl.skips, ["0.1.401", "0.1.402"]);
        assert.deepEqual(errors, []);
      });

      // #2291 (MuiseDestiny): the × sat over the pill's top right, a fifth
      // of the round arrow in a narrow header, and a click meant for Update
      // hid it. It sits off the pill now: no point of the pill is the ×'s.
      for (const tight of [false, true]) {
        await t.test(lang + (tight ? ": in a narrow header" : ": in the panel") + ", the × covers no part of the pill", async () => {
          const ctl = fresh();
          const { page, errors } = await open(lang, ctl, "?mode=panel");
          const pill = page.locator("#update"), x = page.locator("#updateHide");
          await pill.waitFor({ state: "visible" });
          if (tight) {
            // the header as fitTop leaves it when the tabs don't fit (#442)
            await page.evaluate(() => { topFit.disconnect(); document.querySelector(".top").classList.add("tight"); });
            assert.equal(await pill.locator("span").evaluate((e) => getComputedStyle(e).display), "none", "the pill is its round arrow");
          }
          await pill.hover();
          await page.waitForFunction(() => getComputedStyle(document.querySelector("#updateHide")).opacity === "1");
          const seen = await page.evaluate(() => {
            const p = document.querySelector("#update"), x = document.querySelector("#updateHide");
            const a = p.getBoundingClientRect(), c = x.getBoundingClientRect();
            const rad = Math.min(a.height / 2, parseFloat(getComputedStyle(p).borderTopLeftRadius));
            const taken = [];
            for (let i = 1; i < a.width; i += 0.5) for (let j = 1; j < a.height; j += 0.5) {
              const px = a.left + i, py = a.top + j;
              const cx = Math.min(Math.max(px, a.left + rad), a.right - rad), cy = Math.min(Math.max(py, a.top + rad), a.bottom - rad);
              if (Math.hypot(px - cx, py - cy) > rad - 0.5) continue; // outside the pill's round
              if (!p.contains(document.elementFromPoint(px, py))) taken.push([i, j]);
            }
            const mid = document.elementFromPoint(c.left + c.width / 2, c.top + c.height / 2);
            return { taken, own: x.contains(mid), inside: c.top >= 0 && c.left >= 0 && c.right <= innerWidth };
          });
          assert.deepEqual(seen.taken, [], "the × takes clicks meant for the pill");
          assert(seen.own && seen.inside, "the × itself can still be clicked, in the window");
          await x.click();
          await pill.waitFor({ state: "hidden" });
          assert.deepEqual(ctl.skips, ["0.1.401"]);
          assert.deepEqual(errors, []);
        });
      }

      await t.test(lang + ": Settings turns it off, and shows it again", async () => {
        const ctl = fresh({ skip: "0.1.400", hushed: true, latest: "0.1.402" });
        const { page, errors } = await open(lang, ctl, "?view=settings&tab=about");
        const row = page.locator("#about .row.pref", { has: page.locator(".name", { hasText: w.pill }) });
        await row.locator(".sub", { hasText: w.hidden }).waitFor();
        // the version row still offers the update
        await page.locator("#about .row.pref", { has: page.locator(".name", { hasText: w.version }) }).locator("button", { hasText: w.restart }).waitFor();
        assert.equal(await page.locator("#update").isHidden(), true);
        await showRow(page, row);
        await row.locator("button", { hasText: w.again }).click();
        await page.locator("#update").waitFor({ state: "visible" });
        assert.deepEqual(ctl.skips, [""]);
        await row.locator(".sub", { hasText: w.hidden }).waitFor({ state: "detached" });
        // off for good: no pill, whatever version is out
        await showRow(page, row);
        const before = await scrolls(page);
        await row.locator("button", { hasText: w.off }).click();
        await page.locator("#update").waitFor({ state: "hidden" });
        assert.equal(ctl.saves.at(-1).noUpdatePill, true);
        assert.equal(await scrolls(page), before, "the click must not scroll the page");
        ctl.latest = "0.1.402";
        await page.reload();
        await page.locator("#sync").waitFor();
        await page.waitForTimeout(400);
        assert.equal(await page.locator("#update").isHidden(), true);
        await showRow(page, row);
        await page.locator("#about .row.pref", { has: page.locator(".name", { hasText: w.pill }) }).locator("button", { hasText: w.on }).click();
        await page.locator("#update").waitFor({ state: "visible" });
        assert.equal(ctl.saves.at(-1).noUpdatePill, false);
        assert.deepEqual(errors, []);
      });
    }
  });
}
