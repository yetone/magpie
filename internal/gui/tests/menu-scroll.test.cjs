// Run with Node's test runner and Playwright on the module path; see README.md.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const date = "2026-09-28";
const usage = Array.from({ length: 40 }, (_, i) => ({
  agent: "fixture", cwd: "/projects/project-" + String(i + 1).padStart(2, "0"),
  model: "model-" + String(i + 1).padStart(2, "0"), input: 40000 - i * 500,
  output: 1000, cache_read: 0, cache_write: 0, cost: 1, priced: true,
}));
const sessions = usage.map((u, i) => ({
  ...u, id: String(i), title: "Session " + (i + 1), name: "Fixture",
  last: date + "T08:00:00Z", start: date + "T07:00:00Z", models: [u], unpriced: 0,
}));
const state = {
  agents: [{ id: "fixture", name: "Fixture", path: "/test/config.toml", fields: [{
    key: "model", label: "model", value: "model-01",
    options: usage.map(u => ({ value: u.model, label: u.model })),
  }] }],
  profiles: [], settings: { lang: "en", theme: "light" },
};

async function serve(route) {
  const url = new URL(route.request().url());
  const json = data => route.fulfill({ json: data });
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: 'window.bootPrefs = {lang:"en",theme:"light",web:true};' });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return json(state);
  if (url.pathname === "/api/sessions/progress") return json({ indexing: false });
  if (url.pathname === "/api/sessions") return json({ sessions, dirs: ["/test/sessions"] });
  if (url.pathname === "/api/sessions/stats") return json({ from: date, to: date, days: [{ date, usage, active: [] }], agents: { fixture: "Fixture" } });
  if (url.pathname === "/api/sessions/overview") return json({ count: sessions.length, days: [sessions.length], top: { tokens: [], cost: [], active: [] } });
  if (url.pathname === "/api/update" || url.pathname === "/api/drift") return json({});
  if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
  if (url.pathname === "/api/groups") return json({ groups: [] });
  if (url.pathname === "/api/gateway/trace") return json({ mine: false, now: date, totals: { requests: 0, rerouted: 0, errors: 0 } });
  assert(!url.pathname.startsWith("/api/"), "Unexpected API: " + url.pathname);
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  await route.fulfill({ body: await fs.readFile(file), contentType });
}

async function scrolled(page, selector) {
  await page.waitForFunction(selector => {
    const box = document.querySelector(selector);
    return !box || box.hidden || box.scrollTop > 0;
  }, selector);
  const box = page.locator(selector);
  assert(await box.isVisible(), "scrolling must leave the dropdown open");
  assert(await box.evaluate(e => e.scrollTop > 0), "the dropdown must actually scroll");
}

async function scrollMenu(page, selector, gesture) {
  const box = page.locator(selector);
  assert(await box.evaluate(e => e.scrollHeight > e.clientHeight), "the fixture must overflow");
  const r = await box.boundingBox();
  if (gesture === "wheel") {
    await page.mouse.move(r.x + r.width / 2, r.y + r.height / 2);
    await page.mouse.wheel(0, 240);
  } else if (gesture === "track") {
    await page.mouse.click(r.x + r.width - 6, r.y + r.height - 25);
  } else {
    await page.mouse.move(r.x + r.width - 6, r.y + 20);
    await page.mouse.down();
    await page.mouse.move(r.x + r.width - 6, r.y + 200, { steps: 10 });
    await page.mouse.up();
  }
  await scrolled(page, selector);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": scrollable dropdowns", async t => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium", ignoreDefaultArgs: ["--hide-scrollbars"] }));
    const context = await browser.newContext({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" });
    const page = await context.newPage();
    page.setDefaultTimeout(5000);
    const errors = [];
    page.on("pageerror", e => errors.push(e.message));
    await page.route("**/*", serve);
    await page.addInitScript(() => localStorage.setItem("magpie.usageTab", "sessions"));
    await context.tracing.start({ screenshots: true, snapshots: true });
    t.after(async () => {
      try {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, engine + ".png") });
          await context.tracing.stop({ path: path.join(process.env.ARTIFACT_DIR, engine + ".zip") });
        } else await context.tracing.stop();
      } finally { await browser.close(); }
    });
    async function reset() {
      await page.goto("http://magpie.test/?view=usage");
      await page.locator("#sessFolder:not([hidden])").waitFor();
      assert.equal(await page.title(), "magpie");
      assert.equal(await page.locator("#sessList .sess-item").count(), 40);
      // Always-visible scrollbars make track/thumb input independent of OS preferences.
      await page.addStyleTag({ content: `
        .sess-menu, #list { animation: none !important; scrollbar-width: auto; scrollbar-gutter: stable; }
        :is(.sess-menu, #list)::-webkit-scrollbar { width: 14px; }
        :is(.sess-menu, #list)::-webkit-scrollbar-thumb { background: #888; }
        :is(.sess-menu, #list)::-webkit-scrollbar-track { background: #eee; }
      ` });
      await page.evaluate(async () => {
        await document.fonts.ready;
        await Promise.all(document.getAnimations().map(a => a.finished.catch(() => {})));
      });
    }

    for (const filter of ["sessFolder", "sessModel"]) {
      for (const gesture of ["wheel", "track", "drag"]) {
        await t.test(filter + ": " + gesture, async () => {
          await reset();
          await page.locator("#" + filter).click();
          await scrollMenu(page, ".sess-menu", gesture);
          await page.keyboard.press("Escape");
          assert.equal(await page.locator(".sess-menu").count(), 0);
          assert(await page.locator("#" + filter).evaluate(e => e === document.activeElement));
        });
      }
      await t.test(filter + ": keyboard scroll and selection", async () => {
        await reset();
        await page.locator("#" + filter).click();
        await page.keyboard.press("ArrowUp");
        await scrolled(page, ".sess-menu");
        assert(await page.locator(".sess-menu .pm-item").last().evaluate(e => e === document.activeElement));
        await page.keyboard.press("Enter");
        assert.equal(await page.locator(".sess-menu").count(), 0);
        assert.match(await page.locator("#" + filter).innerText(), /(?:project|model)-40/);
        assert.equal(await page.locator("#sessList .sess-item").count(), 1);
        assert.equal(await page.locator("#sessList .name").innerText(), "Session 40");
      });
    }
    await t.test("outside click, page scroll, resize and reopen", async () => {
      await reset();
      const anchor = page.locator("#sessFolder");
      for (const dismiss of ["outside", "page scroll", "resize"]) {
        await reset();
        await anchor.click();
        await page.locator(".sess-menu").waitFor();
        if (dismiss === "outside") { const bar = await page.locator("#sessionsPane .sess-tools").boundingBox(); await page.mouse.click(bar.x + bar.width - 6, bar.y + bar.height / 2); }
        else if (dismiss === "page scroll") {
          await page.mouse.move(1000, 650);
          await page.mouse.wheel(0, 200);
        } else await page.setViewportSize({ width: 1101, height: 800 });
        await page.locator(".sess-menu").waitFor({ state: "detached" });
      }
      await anchor.click();
      await scrollMenu(page, ".sess-menu", "wheel");
    });
    await t.test("main model picker keeps scrolling", async () => {
      await reset();
      await page.locator('#nav [data-view="agents"]').click();
      for (const gesture of ["wheel", "track", "drag"]) {
        await page.locator('#agents [data-key="model"]').click();
        await scrollMenu(page, "#list", gesture);
        await page.locator("#q").press("Escape");
        assert(await page.locator("#pop").isHidden());
      }
    });
    await t.test("main model picker closes on a second click", async () => {
      const anchor = page.locator('#agents [data-key="model"]');
      await anchor.click();
      assert(await page.locator("#pop").isVisible());
      await anchor.click();
      assert(await page.locator("#pop").isHidden());
      await anchor.click();
      assert(await page.locator("#pop").isVisible(), "a third click opens it again");
      await page.locator("#q").press("Escape");
    });
    assert.deepEqual(errors, [], "page runtime errors");
  });
}
