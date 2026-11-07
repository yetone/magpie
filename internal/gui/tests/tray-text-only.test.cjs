// Run with Node's test runner and Playwright on the module path; see README.md.
// LuckymanJC on Discord: Google AI Studio ticked in Settings → Allowances in
// the menu bar, and the menu bar showed nothing for it. Its plugin reports
// no allowance, only its accounts' cooldowns beside two lines of text (its
// proxy, its admin page), so with nothing cooling its card is all text,
// which the menu bar has no figure for. The picker leaves such a card out;
// one cooling (a reading) is offered; one ticked before stays, under its
// card's name, to be unticked. English and Chinese, Chromium and WebKit; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { openPart } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

const service = [
  { name: "代理出口", used: 0, display: "不走代理", text: true },
  { name: "管理页", used: 0, display: "http://127.0.0.1:5000", text: true },
];
let quotas;
const idle = [
  { provider: "codex", name: "Codex", icon: "codex-color", user: "x@y.z", plan: "Plus", windows: [{ name: "5-hour", used: 8 }] },
  { provider: "ai-studio", name: "Google AI Studio", icon: "gemini-color", user: "a@gmail.com", windows: service },
];
const cooling = [
  idle[0],
  { ...idle[1], windows: [{ name: "所有模型", used: 100, display: "冷却中" }, ...service] },
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

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a card of text alone is not offered for the menu bar`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-tray-text-only.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      quotas = idle;
      await page.route("**/*", server(lang, posts));
      await page.goto("http://magpie.test/?view=usage");
      await page.locator("#prefs").click();
      await openPart(page, "usage");
      const pill = page.locator("#trayUsagePick button");
      await pill.waitFor();
      await pill.scrollIntoViewIfNeeded();
      const items = page.locator(".proto-menu .pm-item");
      const names = () => items.evaluateAll((bs) => bs.map((b) => b.querySelector(".pm-name").textContent.trim()));

      await pill.click();
      await items.first().waitFor();
      assert.ok(!(await names()).includes("Google AI Studio"), "nothing cooling: its card is text alone, not offered");
      assert.ok((await names()).includes("Codex"));
      await page.keyboard.press("Escape");

      // cooling: a reading the menu bar draws, so offered and ticked
      quotas = cooling;
      await page.evaluate(() => loadQuotas());
      await pill.click();
      await items.first().waitFor();
      const at = (await names()).indexOf("Google AI Studio");
      assert.ok(at > 0, "cooling: offered");
      await items.nth(at).click();
      await page.keyboard.press("Escape");
      await page.waitForFunction(() => document.querySelector("#trayUsagePick button").textContent.trim() === "Google AI Studio");
      assert.deepEqual(posts.at(-1).trayUsages, ["ai-studio|a@gmail.com"]);

      // the cooldown over: still ticked, under the card's name, to untick
      quotas = idle;
      await page.evaluate(() => loadQuotas());
      await pill.click();
      await items.first().waitFor();
      const rows = await items.evaluateAll((bs) => bs.map((b) => [b.querySelector(".pm-name").textContent.trim(), b.getAttribute("aria-checked")]));
      assert.deepEqual(rows.filter(([n]) => n === "Google AI Studio"), [["Google AI Studio", "true"]]);
      await page.keyboard.press("Escape");
      assert.deepEqual(errors, []);
    });
  }
}
