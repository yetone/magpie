// Run with Node's test runner and Playwright on the module path; see README.md.
// nam.haid on Discord: every SiliconFlow card said only "Allowance
// unavailable — hover for why". SiliconFlow retired its balance query on
// 2026-08-14 with nothing in its place, so the card says that, in the
// user's language, with the reason in its tooltip and a button that opens
// its console (seeAt). The editor's "Check balance" says the same
// (balanceError). A Zhipu key's balance card, read now, shows its amount.
// No backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
// siliconFlowRetired's words (internal/provider/balance.go)
const GONE = "SiliconFlow retired its balance query (/v1/user/info) and has no API for it in its place yet; the balance and vouchers are on https://cloud.siliconflow.cn (余额充值 → 代金券)";
const CONSOLE = "https://cloud.siliconflow.cn";

const quotas = [
  { provider: "siliconflow", name: "SiliconFlow", icon: "siliconcloud-color", error: GONE, seeAt: CONSOLE, windows: [] },
  { provider: "zhipu", name: "Zhipu GLM", icon: "zhipu-color", balance: "¥12.50", windows: [] },
  { provider: "kimi", name: "Kimi", icon: "kimi-color", error: "HTTP 503", windows: [] },
];

function serve(lang, opened) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (body) => route.fulfill({ json: body });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/open") {
      opened.push(JSON.parse(req.postData() || "{}").url);
      return route.fulfill({ status: 204, body: "" });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const said = "SiliconFlow no longer has a balance API — see the balance and vouchers on its console";
const button = "Open the console ↗";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [900, 420]) {
      test(`${engine} ${lang} ${width}px: a SiliconFlow key's card says its balance API is gone and opens the console; a Zhipu key shows its balance`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const errors = [];
        const opened = [];
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.addInitScript(() => {
          window.__opened = [];
          window.open = (url) => { window.__opened.push(url); return null; };
        });
        await page.route("**/*", serve(lang, opened));
        // app.js is large: its first load on a busy machine takes more than 5s
        await page.goto("http://magpie.test/?view=usage", { timeout: 30000 });

        const err = page.locator(".subscription-card", { hasText: "SiliconFlow" }).locator(".subscription-error");
        await err.waitFor();
        assert.equal(await err.getAttribute("title"), GONE, "the reason in the tooltip");
        const [words, open, generic] = await page.evaluate(([a, b]) => [t(a), t(b), t("Allowance unavailable — hover for why")], [said, button]);
        if (lang !== "en") {
          assert.notEqual(words, said, `${lang} has its own words for the card`);
          assert.notEqual(open, button, `${lang} has its own words for the button`);
        }
        assert.ok((await err.textContent()).includes(words), await err.textContent());
        assert.ok(!(await err.textContent()).includes(generic));
        const go = err.locator(".quota-fix");
        assert.equal((await go.textContent()).trim(), open);
        // a card that failed for another reason has no such button
        assert.equal(await page.locator(".subscription-card", { hasText: "Kimi" }).locator(".quota-fix").count(), 0);
        const glm = page.locator(".subscription-card", { hasText: "Zhipu GLM" });
        assert.ok((await glm.textContent()).includes("12.50"), await glm.textContent());
        assert.equal(await glm.locator(".subscription-error").count(), 0);
        const [e, b] = [await err.boundingBox(), await go.boundingBox()];
        assert.ok(e.x + e.width <= width && b.x + b.width <= width, "the error and its button fit the page");

        await go.click();
        // in a browser api("open") is window.open (the app posts /api/open)
        await page.waitForFunction(() => window.__opened.length > 0);
        assert.deepEqual(await page.evaluate(() => window.__opened), [CONSOLE]);
        assert.deepEqual(opened, []);
        assert.equal(new URL(page.url()).searchParams.get("view"), "usage", "the page stays where it was");
        // the editor's Check balance says the same
        assert.equal(await page.evaluate((z) => balanceError(z), GONE), words);
        assert.deepEqual(errors, []);
      });
    }
  }
}
