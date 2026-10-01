// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings → Usage in the menu bar picks any number of the Usage page's
// cards, shown side by side beside the tray icon: the menu keeps open as
// each is ticked or unticked, and posts the ticked ones once, as it closes,
// in the menu's order; Off unticks them all; the pill says how many; a save
// of another setting keeps them; no click moves the page. English and
// Chinese; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const quotas = [
  { provider: "claude", name: "Claude Code", icon: "claude-color", user: "a@b.c", plan: "Max",
    windows: [{ name: "5-hour", used: 42 }, { name: "Weekly", used: 18 }] },
  { provider: "codex", name: "Codex", icon: "codex-color", user: "x@y.z", plan: "Plus",
    windows: [{ name: "5-hour", used: 8 }, { name: "Weekly", used: 100 }] },
  { provider: "copilot", name: "Copilot", icon: "githubcopilot", windows: [{ name: "Premium", used: 63 }] },
  { provider: "kimi", name: "Kimi", icon: "kimi", error: "signed out" },
];

function settingsPayload(over) {
  return {
    theme: "light", lang: "en", tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "copilot", trayUsages: ["copilot"], trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

// server answers as magpie does: trayUsage kept as the first of trayUsages
function server(lang, posts) {
  let cur = settingsPayload({ lang });
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
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const view = (page) => page.locator("#view-settings").evaluate((v) => v.scrollTop);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": several cards in the menu bar", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const pages = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-tray-usages-${i}.png`) });
      }
      await browser.close();
    });

    for (const [lang, off, three, two] of [
      ["en", "Off", "3 subscriptions", "2 subscriptions"],
      ["zh", "关闭", "3 个订阅", "2 个订阅"],
    ]) {
      await t.test(lang, async () => {
        const errors = [], posts = [];
        const context = await browser.newContext({ viewport: { width: 900, height: 420 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        pages.push(page);
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posts));

        await page.goto("http://magpie.test/?view=usage");
        await page.locator("#prefs").click();
        await page.locator("#setTab-usage").click();
        const pill = page.locator("#trayUsagePick button");
        await pill.waitFor();
        await page.waitForFunction(() => document.querySelector("#trayUsagePick button").textContent.trim() === "Copilot");
        assert.equal(await page.locator("#trayEveryRow").isVisible(), true, "the refresh row shows with a card picked");

        // scrolled down with a real wheel, the pill still in sight
        await pill.hover();
        for (let i = 0; i < 20 && !(await view(page)); i++) { await page.mouse.wheel(0, 20); await page.waitForTimeout(20); }
        await pill.scrollIntoViewIfNeeded();
        const before = await view(page);
        assert(before > 0, "the settings list must be scrolled");

        // the menu: Off, the cards that can show (not the signed-out one),
        // each with a box to tick; Copilot ticked
        await pill.click();
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        const items = menu.locator(".pm-item");
        assert.deepEqual((await items.locator(".pm-name").allTextContents()).map((s) => s.trim()), [off, "Claude Code", "Codex", "Copilot"]);
        assert.deepEqual(await items.evaluateAll((bs) => bs.map((b) => b.getAttribute("role"))),
          ["menuitemradio", "menuitemcheckbox", "menuitemcheckbox", "menuitemcheckbox"]);
        const checked = () => items.evaluateAll((bs) => bs.map((b) => b.getAttribute("aria-checked")));
        assert.deepEqual(await checked(), ["false", "false", "false", "true"]);
        // no coloured stripe down a ticked row's side
        assert.equal(await items.nth(3).evaluate((b) => getComputedStyle(b).borderLeftWidth), "0px");

        // ticking two more keeps it open and sends nothing yet
        await items.nth(2).click();
        await items.nth(1).click();
        assert.equal(await menu.isVisible(), true, "the menu stays open while ticking");
        assert.deepEqual(await checked(), ["false", "true", "true", "true"]);
        assert.equal(posts.length, 0, "nothing is saved till the menu closes");
        assert.equal(await view(page), before, "ticking must not scroll the page");

        // closing it saves them once, in the menu's order
        await page.keyboard.press("Escape");
        await page.waitForFunction((s) => document.querySelector("#trayUsagePick button").textContent.trim() === s, three);
        assert.equal(posts.length, 1);
        assert.deepEqual(posts[0].trayUsages, ["claude|a@b.c", "codex|x@y.z", "copilot"]);
        await page.waitForTimeout(300);
        assert.equal(await view(page), before, "saving must not scroll the page");

        // opened and closed with nothing changed: nothing sent
        await pill.click();
        await menu.waitFor();
        await pill.click();
        await menu.waitFor({ state: "detached" });
        assert.equal(posts.length, 1, "an unchanged pick sends nothing");

        // one unticked, then a click elsewhere: the other two
        await pill.click();
        await items.nth(3).click();
        await page.locator("#trayUsageRow .name").click();
        await page.waitForFunction((s) => document.querySelector("#trayUsagePick button").textContent.trim() === s, two);
        assert.deepEqual(posts.at(-1).trayUsages, ["claude|a@b.c", "codex|x@y.z"]);

        // another setting saved keeps them
        const sent = posts.length;
        // clicked where it is: Playwright would scroll to it, under the footer
        await page.locator("#currencySegs .opt").nth(1).evaluate((b) => b.click());
        for (let i = 0; i < 50 && posts.length === sent; i++) await page.waitForTimeout(50);
        assert.equal(posts.at(-1).currency, "cny");
        assert.deepEqual(posts.at(-1).trayUsages, ["claude|a@b.c", "codex|x@y.z"], "a save of another setting keeps the cards");

        // Off unticks them all and closes the menu
        await pill.click();
        await items.nth(0).click();
        await menu.waitFor({ state: "detached" });
        await page.waitForFunction((s) => document.querySelector("#trayUsagePick button").textContent.trim() === s, off);
        assert.deepEqual(posts.at(-1).trayUsages, []);
        assert.equal(await page.locator("#trayEveryRow").isVisible(), false, "no refresh row with none picked");
        await page.waitForTimeout(300);
        assert.equal(await view(page), before, "no click moved the page");

        assert.deepEqual(errors, []);
        await context.close();
      });
    }
  });
}
