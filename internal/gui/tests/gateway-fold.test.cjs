// Run with Node's test runner and Playwright on the module path; see README.md.
// The Gateway page's Connect folds away under its heading, the base URL left
// beside it to copy; the fold is remembered, and a click on it with the page
// scrolled leaves the heading where it is.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const calls = Array.from({ length: 20 }, (_, i) => ({
  time: new Date(now - (i + 1) * 60e3).toISOString(), agent: "fixture", model: "fixture/model-a", status: 200, ms: 900,
}));
const providers = {
  providers: [{ id: "fixture", name: "Fixture", icon: "generic", models: [{ id: "model-a", name: "Model A", on: true }], agents: [] }],
  gateway: { running: true, window: true, mine: true, url: "http://127.0.0.1:3999", calls, groups: [] },
};
const state = { agents: [], profiles: [], settings: { lang: "en", theme: "light" } };

async function serve(route) {
  const url = new URL(route.request().url());
  const json = (data) => route.fulfill({ json: data });
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: 'window.bootPrefs = {lang:"en",theme:"light",web:true};' });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return json(state);
  if (url.pathname === "/api/providers") return json(providers);
  if (url.pathname === "/api/groups") return json({ groups: [] });
  if (url.pathname === "/api/gateway/trace") {
    if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
    return json({ mine: true, now: new Date(now).toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
  }
  if (url.pathname.startsWith("/api/")) return json({});
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  await route.fulfill({ body: await fs.readFile(file), contentType });
}

const view = "#view-gateway";
const top = (page, sel) => page.locator(sel).evaluate((e) => e.getBoundingClientRect().top);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Connect folds away and stays folded", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const context = await browser.newContext({ viewport: { width: 900, height: 560 }, reducedMotion: "reduce" });
    const page = await context.newPage();
    page.setDefaultTimeout(5000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve);
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, engine + "-gateway-fold.png") });
      }
      await browser.close();
    });

    await page.goto("http://magpie.test/?view=gateway");
    await page.locator("#connect .val code").first().waitFor();
    const fold = page.locator("#foldConnect");
    assert.equal(await fold.getAttribute("aria-expanded"), "true", "Connect starts open");
    assert.equal(await page.locator("#connectNote code").count(), 0, "open, the head has no URL of its own");

    // scrolled so the Connect head is well down the view, the fold leaves it there
    await page.locator(view).evaluate((v) => { v.scrollTop = 40; });
    await page.waitForTimeout(300);
    const was = await top(page, "#foldConnect");
    await fold.click();
    await page.waitForTimeout(600);
    assert.equal(await fold.getAttribute("aria-expanded"), "false");
    assert(await page.locator("#connect").evaluate((c) => c.hidden), "folded, Connect's fields are hidden");
    const is = await top(page, "#foldConnect");
    assert(Math.abs(is - was) <= 1, `folding moved the head ${Math.round(is - was)}px`);
    assert.equal((await page.locator("#connectNote code").textContent()).trim(), "http://127.0.0.1:3999/v1", "folded, the head shows the base URL");
    assert.equal(await page.locator("#connectNote .copy").count(), 1, "with a copy button");

    // remembered across a reload
    await page.reload();
    await page.locator("#activity .call").first().waitFor();
    // Reduced-motion animations can still be on their first frame when
    // fast fixtures return. Compare the settled heading position.
    await page.locator(view).evaluate(async (v) => {
      await Promise.all(v.getAnimations().map((a) => a.finished.catch(() => {})));
    });
    assert.equal(await page.locator("#foldConnect").getAttribute("aria-expanded"), "false", "the fold is remembered");
    assert(await page.locator("#connect").evaluate((c) => c.hidden));

    // and opens again, the head where it was
    const before = await top(page, "#foldConnect");
    await page.locator("#foldConnect").click();
    await page.waitForTimeout(600);
    assert.equal(await page.locator("#foldConnect").getAttribute("aria-expanded"), "true");
    assert(await page.locator("#connect").evaluate((c) => !c.hidden));
    assert.equal(await page.locator("#connectNote code").count(), 0);
    const after = await top(page, "#foldConnect");
    assert(Math.abs(after - before) <= 1, `unfolding moved the head ${Math.round(after - before)}px`);
    assert.deepEqual(errors, []);
  });
}
