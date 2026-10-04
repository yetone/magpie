// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings → Menu bar logos (Jevin on Discord: the small logos make the menu
// bar's usage look worse): on a Mac with a card in the menu bar, an On/Off
// row under the refresh row; Off posts trayNoLogos and On takes it back; a
// save of another setting keeps it; no row without a card, nor off a Mac;
// no click moves the page. English and Chinese; no backend, the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const quotas = [
  { provider: "claude", name: "Claude Code", icon: "claude-color", user: "a@b.c", plan: "Max",
    windows: [{ name: "5-hour", used: 42 }, { name: "Weekly", used: 18 }] },
];

function settingsPayload(over) {
  return {
    theme: "light", lang: "en", tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "claude|a@b.c", trayUsages: ["claude|a@b.c"], trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

function server(lang, posts, over) {
  let cur = settingsPayload({ lang, ...over });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        posts.push(body);
        cur = { ...cur, ...body, trayUsage: (body.trayUsages || [])[0] || "" };
      }
      return json(cur);
    }
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const view = (page) => page.locator("#view-settings").evaluate((v) => v.scrollTop);

async function open(browser, lang, posts, platform, over) {
  const context = await browser.newContext({ viewport: { width: 900, height: 420 }, reducedMotion: "reduce" });
  await context.addInitScript((p) => Object.defineProperty(Navigator.prototype, "platform", { get: () => p }), platform);
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  await page.route("**/*", server(lang, posts, over));
  await page.goto("http://magpie.test/?view=usage");
  await page.locator("#prefs").click();
  await page.locator("#setTab-usage").click();
  await page.locator("#trayUsagePick button").waitFor();
  return { context, page };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the menu bar's logos on or off", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const pages = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-tray-logos-${i}.png`) });
      }
      await browser.close();
    });

    for (const [lang, name, off, on] of [
      ["en", "Menu bar logos", "Off", "On"],
      ["zh", "菜单栏订阅图标", "关闭", "开启"],
    ]) {
      await t.test(lang, async () => {
        const errors = [], posts = [];
        const { context, page } = await open(browser, lang, posts, "MacIntel");
        pages.push(page);
        page.on("pageerror", (e) => errors.push(e.message));
        const row = page.locator("#trayLogosRow");
        await row.waitFor();
        assert.equal((await row.locator(".name").textContent()).trim(), name);
        const opts = row.locator("#trayLogosSegs .opt");
        assert.deepEqual((await opts.allTextContents()).map((s) => s.trim()), [off, on]);
        // logos are on, as before
        assert.equal(await opts.nth(1).evaluate((b) => b.classList.contains("on")), true, "logos on by default");
        assert.equal(await row.evaluate((r) => getComputedStyle(r).borderLeftWidth), "0px");

        // scrolled down with a real wheel, once the page has settled, the
        // row in sight; then clicked: no scroll
        await page.waitForTimeout(500);
        await page.locator("#trayUsagePick button").hover();
        for (let i = 0; i < 20 && !(await view(page)); i++) { await page.mouse.wheel(0, 120); await page.waitForTimeout(20); }
        await row.scrollIntoViewIfNeeded();
        await page.waitForTimeout(300);
        const before = await view(page);
        assert(before > 0, "the settings list must be scrolled");

        await opts.nth(0).click();
        for (let i = 0; i < 50 && !posts.length; i++) await page.waitForTimeout(50);
        assert.equal(posts.at(-1).trayNoLogos, true, "Off posts trayNoLogos");
        await page.waitForFunction(() => document.querySelector("#trayLogosSegs .opt").classList.contains("on"));
        await page.waitForTimeout(300);
        assert.equal(await view(page), before, "Off moved the page");

        // another setting saved keeps it
        let sent = posts.length;
        await page.locator("#currencySegs .opt").nth(1).evaluate((b) => b.click()); // out of sight, under the footer
        for (let i = 0; i < 50 && posts.length === sent; i++) await page.waitForTimeout(50);
        assert.equal(posts.at(-1).currency, "cny");
        assert.equal(posts.at(-1).trayNoLogos, true, "a save of another setting keeps the logos off");

        // and On takes it back
        await row.scrollIntoViewIfNeeded();
        await page.waitForTimeout(300);
        const back = await view(page);
        sent = posts.length;
        await page.locator("#trayLogosSegs .opt").nth(1).click();
        for (let i = 0; i < 50 && posts.length === sent; i++) await page.waitForTimeout(50);
        assert.equal(posts.at(-1).trayNoLogos, false, "On posts logos back");
        await page.waitForTimeout(300);
        assert.equal(await view(page), back, "On moved the page");
        assert.deepEqual(errors, []);
        await context.close();

        // no card in the menu bar, or not a Mac: no row
        for (const [platform, over] of [["MacIntel", { trayUsage: "", trayUsages: [] }], ["Win32", {}]]) {
          const { context, page } = await open(browser, lang, [], platform, over);
          await page.waitForTimeout(200);
          assert.equal(await page.locator("#trayLogosRow").isVisible(), false, platform + " " + JSON.stringify(over));
          await context.close();
        }
      });
    }
  });
}
