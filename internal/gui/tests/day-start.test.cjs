// Run with Node's test runner and Playwright on the module path; see README.md.
// The "Day starts at" setting (ssharpcc on X): the Settings page offers hours
// 0 to 6, picking one saves dayStart without scrolling the page, and the
// choice is still picked after a reload. No backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { openPart } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

const usage = {
  calls: 5, errors: 0, input: 12000, output: 4000, cache_read: 500, cache_write: 100, reasoning: 200,
  unpriced: 0, cost: 12.34, bucket: "day",
  series: [{ label: "Mon", input: 6000, output: 2000, calls: 3, cost: 7 }],
  agents: [{ name: "Claude Code", calls: 5, cost: 12.34 }],
  models: [{ name: "claude-opus", calls: 5, cost: 12.34 }],
};

function settingsPayload(over) {
  return {
    theme: "light", lang: "en", tray: "panel", quotaLeft: false, currency: "usd", dayStart: 0,
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

function server(lang, saved) {
  let cur = settingsPayload({ lang });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", dayStart: cur.dayStart }, fx: cur.fx });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") { cur = { ...cur, ...req.postDataJSON() }; saved.push(req.postDataJSON()); }
      return json(cur);
    }
    if (url.pathname === "/api/usage") return json(usage);
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const view = (page) => page.locator("#view-settings").evaluate((v) => v.scrollTop);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the day starts at the hour picked", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(lang, async () => {
        const errors = [], saved = [];
        const context = await browser.newContext({ viewport: { width: 900, height: 480 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, saved));

        await page.goto("http://magpie.test/?view=usage");
        await page.locator("#usageCost b").waitFor();
        await page.locator("#prefs").click();
        await openPart(page, "usage");
        const segs = page.locator("#dayStartSegs .opt");
        await segs.first().waitFor();
        assert.equal(await segs.count(), 7);
        assert.equal((await segs.nth(4).textContent()).trim(), "4:00");
        assert.equal(await segs.nth(0).evaluate((b) => b.classList.contains("on")), true, "0:00 starts picked");

        // a real wheel down, then back until the row is clear of the header
        await page.locator("#dayStartSegs").hover();
        for (let i = 0; i < 20 && !(await view(page)); i++) { await page.mouse.wheel(0, 200); await page.waitForTimeout(20); }
        for (let i = 0; i < 40; i++) {
          const box = await segs.nth(4).boundingBox();
          if (box && box.y >= 60) break;
          await page.mouse.wheel(0, -60);
          await page.waitForTimeout(30);
        }
        const before = await view(page);
        const box = await segs.nth(4).boundingBox();
        await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2);
        await page.locator("#dayStartSegs .opt.on", { hasText: "4:00" }).waitFor();
        await page.waitForTimeout(400);
        assert.equal(await view(page), before, "picking an hour must not scroll the settings page");
        assert.equal(saved.at(-1).dayStart, 4, "the pick is saved");

        await page.reload();
        await page.locator("#prefs").click();
        await openPart(page, "usage");
        await segs.first().waitFor();
        assert.equal(await page.locator("#dayStartSegs .opt", { hasText: "4:00" }).evaluate((b) => b.classList.contains("on")), true);
        assert.deepEqual(errors, []);
      });
    }
  });
}
