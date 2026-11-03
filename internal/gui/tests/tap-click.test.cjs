// Run with Node's test runner and Playwright on the module path; see README.md.
// A trackpad's tap-to-click (refeccd, Discord #feedback): the press and the lift
// come within a few tens of milliseconds and the pointer can slide a few pixels
// between them, with no hover before. A row's logo handle began a drag after 4px
// and swallowed the click that followed, so the tap opened nothing and the
// reader tapped again. A press let go quickly and close to where it came down
// is a click; a real drag still moves the row. MAGPIE_ASSETS selects an older
// asset directory for a failing comparison. The real trackpad can't be driven
// from a test. Chromium and WebKit, English and Chinese; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets");

function fixture(lang) {
  const state = { agents: ["alpha", "beta", "gamma"].map((id) => ({ id, name: id, icon: "generic", path: "/fixture/" + id, fields: [] })), profiles: [], settings: { lang, theme: "light", guideHidden: true } }; // the Get started card put away: these rows are the page, the test reads where they sit
  const posted = [];
  const route = async (route) => {
    const request = route.request(), url = new URL(request.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/agents/arrange") {
      const body = request.postDataJSON();
      posted.push(body);
      Object.assign(state.settings, { agentOrder: body.order, agentsHidden: body.hidden, agentsShown: body.shown });
      return json(state.settings);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
  return { state, posted, route };
}


for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a tap that slides a few pixels still clicks an agent row's handle", { timeout: 120000 }, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => { if (errors.length) console.log(errors); await browser.close(); });
    for (const lang of ["en", "zh"]) for (const mode of ["window", "panel"]) for (const slide of [0, 5, 9]) {
      await t.test(`${lang}, ${mode}, slid ${slide}px`, async () => {
        const ctx = await browser.newContext({ viewport: mode === "panel" ? { width: 380, height: 560 } : { width: 1000, height: 640 }, reducedMotion: "reduce" });
        const page = await ctx.newPage(), f = fixture(lang);
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", f.route);
        await page.goto(`http://magpie.test/?view=agents&mode=${mode}`);
        await page.locator("#agents > .row.agent").nth(2).waitFor();
        await page.mouse.move(1, 1);
        const box = await page.locator("#agents > .row.agent").nth(1).locator(".ag-handle").boundingBox();
        const x = box.x + box.width / 2, y = box.y + box.height / 2 - slide / 2;
        // no hover before: the pointer comes, presses, slides, lets go at once
        await page.mouse.move(x, y);
        await page.mouse.down();
        if (slide) await page.mouse.move(x, y + slide);
        await page.mouse.up();
        await page.getByRole("menuitem").first().waitFor();
        assert.deepEqual(f.posted, [], "a tap moved a row");
        assert.equal(await page.locator("#agents > .row.dragging").count(), 0);
        await page.keyboard.press("Escape");

        // a drag the length of a row is still a drag
        const before = await page.locator("#agents > .row.agent").evaluateAll((rs) => rs.map((r) => r.dataset.id));
        await page.mouse.move(x, y);
        await page.mouse.down();
        await page.waitForTimeout(350);
        await page.mouse.move(x, y + box.height * 2.2, { steps: 12 });
        await page.mouse.up();
        await page.waitForFunction(() => document.querySelector("#agents > .row.dragging") === null);
        assert.equal(f.posted.length, 1, "a drag past the next row moved nothing");
        assert.notDeepEqual(f.posted[0].order.slice(0, 3), before);
      });
    }
  });
}
