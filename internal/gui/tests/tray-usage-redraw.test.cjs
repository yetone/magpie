// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings → Allowances in the menu bar, clicked before the allowances are
// in, with Settings drawn again while they load (another setting saved,
// coming back to the window): the menu comes up at the redrawn pill, not in
// the window's corner, and what it saves keeps the setting changed meanwhile.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const quotas = [
  { provider: "claude", name: "Claude Code", icon: "claude-color", user: "a@b.c", plan: "Max", windows: [{ name: "5-hour", used: 42 }] },
  { provider: "copilot", name: "Copilot", icon: "githubcopilot", windows: [{ name: "Premium", used: 63 }] },
];

function server(posts) {
  let cur = {
    theme: "light", lang: "en", tray: "panel", quotaLeft: false, currency: "usd",
    trayUsage: "", trayUsages: [], trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxy: "", proxyNow: "none", proxySource: "none", login: false, redactWords: [],
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang: "en", theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        posts.push(body);
        cur = { ...cur, ...body, trayUsage: (body.trayUsages || [])[0] || "" };
      }
      return json(cur);
    }
    if (url.pathname === "/api/usage/quotas") {
      await new Promise((r) => setTimeout(r, 800)); // a vendor slow to answer
      return json(quotas);
    }
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [900, 560]) {
    test(`${engine} ${width}px: the menu bar's allowances, picked while Settings is drawn again`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(6000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(posts));
      // from Agents: the Usage page would have read the allowances already
      await page.goto("http://magpie.test/?view=agents");
      await page.locator("#prefs").click();
      await page.locator("#setTab-usage").click();
      const pill = page.locator("#trayUsagePick button");
      await pill.waitFor();
      assert.equal(await page.evaluate(() => quotas), null, "the allowances were read before the click");
      const menu = page.locator(".proto-menu");

      // the allowances aren't in: the pill waits on them while CNY is saved
      await pill.click();
      await page.locator("#currencySegs button", { hasText: "CNY" }).click();
      await page.waitForFunction(() => document.querySelector("#currencySegs .on")?.textContent.includes("CNY"));
      await menu.waitFor({ timeout: 3000 });
      assert.equal(await pill.evaluate((b) => b.classList.contains("open")), true, "the redrawn pill isn't marked open");
      assert.equal(await pill.evaluate((b) => b.classList.contains("busy")), false, "the pill still waits");
      const b = await pill.boundingBox(), m = await menu.boundingBox();
      const up = await menu.evaluate((e) => e.classList.contains("up"));
      const gap = up ? b.y - (m.y + m.height) : m.y - (b.y + b.height);
      assert(Math.abs(gap - 5) <= 2, `the menu is ${gap}px from its pill, at ${m.x},${m.y}`);

      // ticked and closed: saved with CNY kept
      await menu.locator(".pm-item", { hasText: "Copilot" }).click();
      await page.keyboard.press("Escape");
      await menu.waitFor({ state: "detached" });
      await page.waitForFunction(() => document.querySelector("#trayUsagePick button")?.textContent.trim() === "Copilot");
      assert.deepEqual(posts.map((p) => [p.currency, p.trayUsages]), [["cny", []], ["cny", ["copilot"]]]);
      assert.deepEqual(errors, []);
    });
  }
}
