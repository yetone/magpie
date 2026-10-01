// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings → Automatic updates turns magpie's own update checks (and the
// downloads they start) off and on, and Check every picks how often they
// run: 30 min, 1 h, 6 h (the default, nothing set) or 24 h (#472). Off, the
// interval stays, dimmed, kept for when it is on again, and the version
// row, not asked yet, says so and offers Check, which asks the backend.
// The rows are in Settings' About tab. No click moves the page. English
// and Chinese; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const words = {
  en: { auto: "Automatic updates", every: "Check every", off: "Off", on: "On", m30: "30 min", h6: "6 h", h24: "24 h",
    version: "Version", check: "Check", isOff: "Automatic updates are off", latest: "Up to date", whileOn: "While automatic updates are on", often: "How often magpie looks for a newer version" },
  zh: { auto: "自动更新", every: "检查间隔", off: "关闭", on: "开启", m30: "30 分钟", h6: "6 小时", h24: "24 小时",
    version: "版本", check: "检查", isOff: "自动更新已关闭", latest: "已是最新", whileOn: "自动更新开启时生效", often: "magpie 多久检查一次新版本" },
};

function settingsPayload(lang, ctl) {
  const s = {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
  // as the backend sends them: left out while they are their defaults
  if (ctl.off) s.noAutoUpdate = true;
  if (ctl.every) s.updateEvery = ctl.every;
  return s;
}

function server(lang, ctl) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settingsPayload(lang, ctl), fx: settingsPayload(lang, ctl).fx });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        ctl.off = !!body.noAutoUpdate;
        ctl.every = body.updateEvery === 360 ? 0 : body.updateEvery;
        ctl.saves.push(body);
      }
      return json(settingsPayload(lang, ctl));
    }
    if (url.pathname === "/api/update/check") {
      ctl.checks++;
      ctl.state = "latest";
      return json({ state: "latest", current: "0.1.400", latest: "0.1.400" });
    }
    if (url.pathname === "/api/update") return json({ state: ctl.state, current: "0.1.400" });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

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
  test(engine + ": automatic updates and how often", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      const w = words[lang];
      await t.test(lang + ": on every 6 h by default; the interval and the switch are saved", async () => {
        const ctl = { off: false, every: 0, state: "", checks: 0, saves: [] };
        const errors = [];
        const page = await (await browser.newContext({ viewport: { width: 900, height: 560 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, ctl));
        await page.goto("http://magpie.test/?view=settings");
        // the rows are in Settings' About part
        const aboutTab = async () => {
          await page.locator("#setTab-about").click();
          await page.locator("#setPage-about").waitFor({ state: "visible" });
        };
        await aboutTab();
        const rowOf = (name) => page.locator("#about .row.pref", { has: page.locator(".name", { hasText: new RegExp("^" + name + "$") }) });
        const auto = rowOf(w.auto), every = rowOf(w.every), version = rowOf(w.version);
        await auto.waitFor();
        assert.equal((await auto.locator(".opt.on").innerText()).trim(), w.on);
        assert.equal((await every.locator(".opt.on").innerText()).trim(), w.h6);
        assert.deepEqual((await every.locator(".opt").allInnerTexts()).map((s) => s.trim()), [w.m30, lang === "zh" ? "1 小时" : "1 h", w.h6, w.h24]);

        // every 30 minutes
        await showRow(page, every);
        let before = await scrolls(page);
        await every.locator(".opt", { hasText: w.m30 }).click();
        await page.waitForFunction(() => document.querySelector(".update-every-row .opt.on")?.textContent.startsWith("30"));
        assert.equal(ctl.saves.at(-1).updateEvery, 30);
        assert.equal(ctl.saves.at(-1).noAutoUpdate, false);
        assert.equal(await scrolls(page), before, "the click must not scroll the page");

        // off: the interval stays, dimmed, as it was
        await showRow(page, auto);
        before = await scrolls(page);
        await auto.locator(".opt", { hasText: w.off }).click();
        await every.locator(".sub", { hasText: w.whileOn }).waitFor();
        assert.equal(await every.evaluate((r) => r.classList.contains("off")), true);
        assert.equal(ctl.saves.at(-1).noAutoUpdate, true);
        assert.equal(ctl.saves.at(-1).updateEvery, 30);
        assert.equal(await scrolls(page), before, "the click must not scroll the page");

        // after a reload: still off; the version row says so and Check asks
        await page.reload();
        await aboutTab();
        await auto.waitFor();
        assert.equal((await auto.locator(".opt.on").innerText()).trim(), w.off);
        assert.equal((await every.locator(".opt.on").innerText()).trim(), w.m30);
        await version.locator(".sub", { hasText: w.isOff }).waitFor();
        await showRow(page, version);
        before = await scrolls(page);
        await version.locator("button", { hasText: w.check }).click();
        await version.locator(".sub", { hasText: w.latest }).waitFor();
        assert.equal(ctl.checks, 1);
        assert.equal(await scrolls(page), before, "the click must not scroll the page");

        // on again, every 24 hours
        await showRow(page, auto);
        before = await scrolls(page);
        await auto.locator(".opt", { hasText: w.on }).click();
        await every.locator(".sub", { hasText: w.often }).waitFor();
        assert.equal(ctl.saves.at(-1).noAutoUpdate, false);
        assert.equal(await every.evaluate((r) => r.classList.contains("off")), false);
        assert.equal((await every.locator(".opt.on").innerText()).trim(), w.m30);
        assert.equal(await scrolls(page), before, "the click must not scroll the page");
        await showRow(page, every);
        before = await scrolls(page);
        await every.locator(".opt", { hasText: w.h24 }).click();
        await page.waitForFunction(() => document.querySelector(".update-every-row .opt.on")?.textContent.startsWith("24"));
        assert.equal(ctl.saves.at(-1).updateEvery, 1440);
        assert.equal(await scrolls(page), before, "the click must not scroll the page");
        assert.deepEqual(errors, []);
      });
    }
  });
}
