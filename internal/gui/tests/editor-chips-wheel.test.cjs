// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider's model chips in its editor: Chromium held the wheel in .mchips
// even when the chips fit (overscroll-behavior: contain on a box that
// doesn't overflow still holds it), so with the pointer over a short list
// the editor's body never scrolled on to its Advanced fields below. The
// chips hold the wheel only while they scroll themselves (:where(.holds)).
// Chromium and WebKit, a window taller than 640 (under it the chips pass
// the wheel on already); the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { provPrefsProviders } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function serve(n) {
  const providers = provPrefsProviders(["relayco"], { relayco: "RelayCo" });
  providers.providers[0].models = Array.from({ length: n }, (_, i) => ({ id: `model-${String(i).padStart(2, "0")}` }));
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang: "en", theme: "light", guideHidden: true } });
    if (url.pathname === "/api/settings") return json({ lang: "en", theme: "light", guideHidden: true });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// at: the chips' middle, when the body around them is what scrolls there
const at = (page) => page.locator(".editor .mchips").first().evaluate((c) => {
  const r = c.getBoundingClientRect(), body = c.closest(".ebody"), v = body.getBoundingClientRect();
  const top = Math.max(r.top, v.top) + 4, bottom = Math.min(r.bottom, v.bottom) - 4;
  return { x: r.left + Math.min(r.width / 2, 60), y: (top + bottom) / 2, fits: c.scrollHeight <= c.clientHeight + 1, room: body.scrollHeight - body.clientHeight };
});
const tops = (page) => page.locator(".editor .mchips").first().evaluate((c) => ({ body: c.closest(".ebody").scrollTop, chips: c.scrollTop, end: c.scrollHeight - c.clientHeight }));
const still = (page) => page.evaluate(() => new Promise((ok) => { let n = 0; const f = () => (++n > 10 ? ok() : requestAnimationFrame(f)); requestAnimationFrame(f); }));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: the wheel over a short model list scrolls the editor, a long one keeps it at its end`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const open = async (n) => {
      const page = await (await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(6000);
      page.setDefaultNavigationTimeout(60000);
      page.errors = [];
      page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; page.errors.push(e.message); });
      await page.route("**/*", serve(n));
      await page.goto("http://magpie.test/?view=providers&edit=relayco");
      await page.locator(".editor .mchips .mchip").first().waitFor({ timeout: 60000 });
      // Advanced opened: the editor taller than its window, so its body scrolls
      const adv = page.locator(".editor details.advanced");
      if (!(await adv.evaluate((d) => d.open))) await adv.locator(":scope > summary").click();
      await still(page);
      return page;
    };

    // a short list: the chips fit, the wheel goes on to the body
    let page = await open(3);
    let p = await at(page);
    assert(p.fits, "three chips fit");
    assert(p.room > 40, `the editor's body has room to scroll: ${p.room}`);
    await page.mouse.move(p.x, p.y);
    const before = await tops(page);
    for (let i = 0; i < 3; i++) { await page.mouse.wheel(0, 60); await still(page); }
    const after = await tops(page);
    assert(after.body > before.body, `the body scrolled under the chips: ${before.body} → ${after.body}`);
    assert.equal(await page.evaluate(() => scrollY), 0, "the page itself never scrolls");
    assert.deepEqual(page.errors, []);

    // a long list: the chips scroll first, and at their end hold the wheel
    page = await open(200);
    p = await at(page);
    assert(!p.fits, "two hundred chips overflow their lines");
    await page.mouse.move(p.x, p.y);
    for (let i = 0; i < 200; i++) {
      const s = await tops(page);
      if (s.chips >= s.end - 1) break;
      await page.mouse.wheel(0, 200);
      await still(page);
    }
    const end = await tops(page);
    assert(end.chips >= end.end - 1, "the chips scrolled to their end");
    for (let i = 0; i < 3; i++) { await page.mouse.wheel(0, 60); await still(page); }
    assert.equal((await tops(page)).body, end.body, "at their end the chips keep the wheel");
    assert.deepEqual(page.errors, []);
  });
}
