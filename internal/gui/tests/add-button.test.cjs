// Run with Node's test runner and Playwright on the module path; see README.md.
// The Providers page's "Add provider" (Image #28: 添加供应商要固定在底栏底部，
// 然后点击的时候要自动滚动到供应商列表。现在只有第二次点击的时候才会滚到
// 供应商列表): the button stays at the view's foot over a long list, one click
// with the list scrolled to its end opens the sheet and takes the view down
// to it (WebKit clamped the view as the sheet began at no height and the
// unroll took that for the reader), the button steps aside while the sheet's
// head is in sight and, scrolled back up, takes the view down to it again.
// And a dialog opened and closed over the page keeps every logo it drew (a
// logo made afresh loads again and blinks: 每次打开或者关闭弹窗的时候，
// Provider的logo都会重新刷新一遍). No backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const presets = ["anthropic", "openai", "deepseek", "moonshot", "xai", "openrouter", "siliconflow", "groq"]
  .map((id, i) => ({ id, name: id, icon: "openai", kind: i < 5 ? "vendor" : "relay", chat: `https://api.${id}.example.com/v1`, added: false }));
const providers = Array.from({ length: 22 }, (_, i) => ({
  id: "p" + i, name: "Provider " + i, icon: ["openai", "deepseek", "claude"][i % 3], preset: "", host: "api.example.com",
  models: [], agents: [], key: { set: true, masked: "sk-…ab12" },
}));

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers, presets, excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Add provider stays at the foot and takes the view to the sheet", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const page = await (await browser.newContext({ viewport: { width: 900, height: 800 } })).newPage();
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator("#providers .row").first().waitFor();
        await page.waitForTimeout(400);
        const at = () => page.evaluate(() => {
          const v = document.querySelector("#view-providers"), vr = v.getBoundingClientRect();
          const bar = v.querySelector(".after-list"), sheet = document.querySelector("#addSheet");
          return {
            top: v.scrollTop, end: v.scrollHeight - v.clientHeight,
            bar: vr.bottom - bar.getBoundingClientRect().bottom, away: getComputedStyle(bar).visibility === "hidden",
            sheet: sheet.hidden ? null : sheet.getBoundingClientRect().top - vr.top,
          };
        });

        // the sheet's head 12px under the view's top, or as near as the view goes
        const reached = () => {
          const v = document.querySelector("#view-providers"), sh = document.querySelector("#addSheet");
          const y = sh.getBoundingClientRect().top - v.getBoundingClientRect().top;
          return !sh.hidden && sh.offsetHeight > 300 && y < v.clientHeight / 3 && (Math.abs(y - 12) <= 2 || v.scrollTop >= v.scrollHeight - v.clientHeight - 1);
        };

        // at the top of a long list the button is at the view's foot, over the rows
        let s = await at();
        assert(s.end > 200, "the list is longer than the view");
        assert(Math.abs(s.bar) <= 1, "the bar sits on the view's foot: " + s.bar);
        assert(!s.away);
        assert(await page.locator("#addProvider").isVisible());
        // and at the end, where it is, not a pixel off
        await page.mouse.move(450, 400);
        for (let i = 0; i < 12; i++) await page.mouse.wheel(0, 400);
        await page.waitForTimeout(500);
        s = await at();
        assert(s.top >= s.end - 1, "scrolled to the end");
        assert(Math.abs(s.bar) <= 1, "the bar at the end: " + s.bar);

        // one click: the sheet opens and the view goes down to it
        await page.locator("#addProvider").click();
        await page.waitForFunction(reached, null, { timeout: 3000 });
        await page.waitForTimeout(300);
        s = await at();
        assert(s.away, "the button steps aside while the sheet's head is in sight");

        // scrolled back up, the button is there again and takes the view to the sheet
        for (let i = 0; i < 12; i++) await page.mouse.wheel(0, -400);
        await page.waitForTimeout(500);
        s = await at();
        assert(s.top < 5 && s.sheet > 700 && !s.away, JSON.stringify(s));
        await page.locator("#addProvider").click();
        await page.waitForFunction(reached, null, { timeout: 3000 });

        // a dialog opened and closed keeps every logo on the page
        await page.evaluate(() => document.querySelectorAll("#providers .ic, #addSheet .ic").forEach((e) => { e.dataset.was = "1"; }));
        const count = await page.locator("#providers .ic, #addSheet .ic").count();
        await page.locator('#addSheet .tile[data-pick="deepseek"]').click();
        await page.locator("#modal .editor").waitFor();
        const fresh = () => page.evaluate(() => [...document.querySelectorAll("#providers .ic:not([data-was]), #addSheet .ic:not([data-was])")].map((e) => (e.closest("[data-id], [data-pick]")?.outerHTML || e.parentElement.outerHTML).slice(0, 120)));
        assert.deepEqual(await fresh(), [], "no logo made afresh as it opened");
        await page.keyboard.press("Escape");
        await page.locator("#modal").waitFor({ state: "hidden" });
        assert.equal(await page.locator("#providers .ic[data-was], #addSheet .ic[data-was]").count(), count, "every logo kept as it closed");
        assert.equal(await page.locator("#providers .ic:not([data-was]), #addSheet .ic:not([data-was])").count(), 0);
        assert.deepEqual(errors, []);
      });
    }
  });
}
