// Run with Node's test runner and Playwright on the module path; see README.md.
// StringKe on Discord: the menu bar showed one account's use, never "the
// one in use". Settings → Usage in the menu bar lists, for a subscription
// with several accounts, "Account in use" before each account by name;
// ticked, it is saved as "claude|*" (the tray follows whichever account the
// gateway goes to first), and the pill names the subscription. One with a
// single account gets no such line. No click moves the page. English and
// Chinese, Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const quotas = [
  { provider: "claude", name: "Claude Code", icon: "claude-color", user: "a@b.c", plan: "Max", windows: [{ name: "5-hour", used: 42 }] },
  { provider: "claude", name: "Claude Code", icon: "claude-color", user: "d@e.f", plan: "Pro", windows: [{ name: "5-hour", used: 7 }] },
  { provider: "codex", name: "Codex", icon: "codex-color", user: "x@y.z", plan: "Plus", windows: [{ name: "5-hour", used: 8 }] },
];

function server(lang, posts) {
  let cur = {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsages: [], trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
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
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const W = { en: { off: "Off", inUse: "Account in use" }, zh: { off: "关闭", inUse: "正在使用的账号" } };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: the menu bar follows the account in use`, async (t) => {
      const w = W[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-tray-inuse.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, posts));
      await page.goto("http://magpie.test/?view=usage");
      await page.locator("#prefs").click();
      await page.locator("#setTab-usage").click();
      const pill = page.locator("#trayUsagePick button");
      await pill.waitFor();
      await pill.scrollIntoViewIfNeeded();
      const top = () => page.locator("#view-settings").evaluate((v) => v.scrollTop);
      const before = await top();

      await pill.click();
      const items = page.locator(".proto-menu .pm-item");
      await items.first().waitFor();
      const rows = await items.evaluateAll((bs) => bs.map((b) => [b.querySelector(".pm-name").textContent.trim(), b.querySelector(".pm-note").textContent.trim()]));
      assert.deepEqual(rows, [
        [w.off, ""], ["Claude Code", w.inUse], ["Claude Code", "Max · a@b.c"], ["Claude Code", "Pro · d@e.f"], ["Codex", "Plus · x@y.z"],
      ], "the account in use before Claude's two accounts; none for Codex's one");
      await items.nth(1).click();
      await page.keyboard.press("Escape");
      await page.waitForFunction(() => document.querySelector("#trayUsagePick button").textContent.trim() === "Claude Code");
      assert.deepEqual(posts.at(-1).trayUsages, ["claude|*"]);

      // ticked when opened again
      await pill.click();
      await items.first().waitFor();
      assert.deepEqual(await items.evaluateAll((bs) => bs.map((b) => b.getAttribute("aria-checked"))), ["false", "true", "false", "false", "false"]);
      await page.keyboard.press("Escape");
      await page.waitForTimeout(200);
      assert.equal(await top(), before, "no click moved the page");
      const missing = await page.evaluate(() => ["Account in use"].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, []);
      assert.deepEqual(errors, []);
    });
  }
}
