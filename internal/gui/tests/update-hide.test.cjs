// Run with Node's test runner and Playwright on the module path; see README.md.
// The header's Update pill can be kept away (Wang Hsiaohi on Discord). Its ×,
// shown with the pointer over it, hides it for that version: it stays away
// after a reload and comes back once a newer version is out; a right-click
// does the same. Settings → Update button turns it off for good, and says
// which version it is hidden for with a way to show it again. The version
// row still offers the restart. No click moves the page. English and
// Chinese, in the tray panel and the window; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const words = {
  en: { update: "Update", hide: "Hide until the next version", pill: "Update button", off: "Off", on: "On",
    hidden: "Hidden for 0.1.401 until a newer version is out", again: "Show again", restart: "Restart to update", version: "Version" },
  zh: { update: "更新", hide: "隐藏，直到下一个版本", pill: "更新按钮", off: "关闭", on: "开启",
    hidden: "0.1.401 已隐藏，更新的版本发布时再显示", again: "重新显示", restart: "重启以更新", version: "版本" },
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

// ctl holds what the backend would: the version out, and the two settings
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
    if (url.pathname === "/api/update") return json({ state: "ready", current: "0.1.400", latest: ctl.latest });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const fresh = (over) => ({ latest: "0.1.401", off: false, skip: "", skips: [], saves: [], ...over });
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
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, ctl));
      await page.goto("http://magpie.test/" + query);
      return { page, errors };
    };
    for (const lang of ["en", "zh"]) {
      const w = words[lang];
      await t.test(lang + ": the × hides it for this version, a newer one brings it back", async () => {
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
        await pill.waitFor({ state: "visible" });
        // and a right-click hides it as well
        await pill.click({ button: "right" });
        await pill.waitFor({ state: "hidden" });
        assert.deepEqual(ctl.skips, ["0.1.401", "0.1.402"]);
        assert.deepEqual(errors, []);
      });

      await t.test(lang + ": Settings turns it off, and shows it again", async () => {
        const ctl = fresh({ skip: "0.1.401" });
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
