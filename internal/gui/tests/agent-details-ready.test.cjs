// First Details must measure the settings it will show, including the lazy
// Long conversations row. Hold each read rather than racing a wall-clock
// delay; sample the sheet's last animation frame and its natural height.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { agentRow } = require("./reader.cjs");

const assets = process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets");
const languages = ["en", "zh", "zh-TW", "ja", "de"];
const row = (id) => `.row.agent[data-id="${id}"]`;
const link = (page, id) => page.locator(row(id) + " .ag-link");
function held() {
  let release;
  const promise = new Promise((r) => { release = r; });
  return { promise, release };
}

async function openPage(t, engine, lang = "en", width = 1180, reducedMotion = "no-preference") {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await browser.newPage({ viewport: { width, height: 1000 }, reducedMotion });
  page.setDefaultTimeout(8000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], settingsAsked = held(), settings = held(), compactAsked = held(), compact = held();
  t.after(async () => { settings.release(); compact.release(); await browser.close(); });
  let reads = 0, fail = false;
  page.on("pageerror", (e) => errors.push(e.message));
  await page.addInitScript(() => {
    const animate = Element.prototype.animate;
    window.detailSlides = [];
    Element.prototype.animate = function (...args) {
      const run = animate.apply(this, args);
      if (this.classList.contains("ag-exp")) {
        run.pause();
        window.detailSlides.push(run);
      }
      return run;
    };
  });
  await page.route("**/*", async (route) => {
    const url = new URL(route.request().url()), json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs={lang:${JSON.stringify(lang)},theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [agentRow("codex", "Codex"), agentRow("claude", "Claude Code"), agentRow("grok", "Grok Build"), agentRow("goose", "Goose")], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/settings") {
      reads++;
      settingsAsked.release();
      await settings.promise;
      if (fail) return route.fulfill({ status: 500, body: "settings unavailable" });
      return json({ lang, theme: "light", tray: "panel", compactAt: 200000, codexAgentsV1: true, visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [], titleModels: [], redactWords: [] });
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.pathname === "/agentcompact.js") { compactAsked.release(); await compact.promise; }
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); }
    catch { await route.fulfill({ status: 404, body: "" }); }
  });
  await page.goto("http://magpie.test/?view=agents");
  await link(page, "codex").waitFor();
  return { page, errors, settingsAsked, settings, compactAsked, compact, reads: () => reads, fail: (v) => { fail = v; } };
}

async function slideEndsWhole(page, id) {
  await page.waitForFunction(() => detailSlides.length > 0);
  const heights = await page.evaluate(async (selector) => {
    const run = detailSlides.at(-1), box = document.querySelector(selector);
    await run.ready;
    run.currentTime = run.effect.getTiming().duration - 1;
    const before = box.getBoundingClientRect().height;
    run.finish();
    await run.finished;
    await new Promise(requestAnimationFrame);
    return [before, box.getBoundingClientRect().height];
  }, row(id));
  assert(Math.abs(heights[1] - heights[0]) < 1, `Details must end without a height jump: ${heights.join(" -> ")}`);
  return heights[1];
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of languages) for (const width of [1180, 560]) {
    test(`${engine} ${lang} ${width}: first Details waits for settings and ends at its full height`, async (t) => {
      const f = await openPage(t, engine, lang, width), { page } = f;
      const shut = await page.locator(row("codex")).boundingBox();
      await link(page, "codex").click();
      await f.settingsAsked.promise;
      assert.equal(await page.locator(row("codex") + " .ag-exp").count(), 0, "no partial Details while settings are unread");
      assert.equal(await link(page, "codex").getAttribute("aria-busy"), "true", "the read has immediate feedback");
      f.settings.release();
      await f.compactAsked.promise;
      assert.equal(await page.locator(row("codex") + " .ag-exp").count(), 0, "no partial Details while its last settings row loads");
      f.compact.release();
      await page.locator(row("codex") + " .ag-prefs .agent-compact").waitFor();
      const full = await slideEndsWhole(page, "codex");
      assert(full > shut.height + 100, "the whole settings list is present");
      assert.equal(await page.locator("#view-agents").evaluate((v) => v.scrollTop), 0, "opening never scrolls the page");
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "no horizontal overflow");
      await link(page, "codex").click();
      await page.evaluate(async () => { const run = detailSlides.at(-1); run.finish(); await run.finished; });
      await page.locator(row("codex") + " .ag-exp").waitFor({ state: "detached" });
      await link(page, "codex").click();
      assert.equal(await slideEndsWhole(page, "codex"), full, "warm Details opens to the same full height");
      assert.equal(f.reads(), 1, "warm opening reuses the settings");
      assert.deepEqual(f.errors, []);
      if (process.env.ARTIFACT_DIR && lang === "zh" && width === 1180) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.locator(row("codex") + " .ag-exp").evaluate((b) => b.getAnimations({ subtree: true }).forEach((a) => a.finish()));
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `details-${engine}.png`) });
      }
    });
  }

  for (const id of ["claude", "grok"]) test(`${engine}: ${id} first Details prepares its settings too`, async (t) => {
    const f = await openPage(t, engine), { page } = f;
    await link(page, id).click();
    await f.settingsAsked.promise;
    assert.equal(await page.locator(row(id) + " .ag-exp").count(), 0);
    f.settings.release(); f.compact.release();
    await page.locator(row(id) + " .agent-compact").waitFor();
    await slideEndsWhole(page, id);
    assert.deepEqual(f.errors, []);
  });

  test(`${engine}: the first animation ends at the natural height after both reads answer`, async (t) => {
    const f = await openPage(t, engine), { page } = f;
    await link(page, "codex").click();
    await f.settingsAsked.promise;
    f.settings.release(); f.compact.release();
    await page.locator(row("codex") + " .ag-prefs .agent-compact").waitFor();
    await slideEndsWhole(page, "codex");
    assert.deepEqual(f.errors, []);
  });

  test(`${engine}: settings already read still waits for the first controls import`, async (t) => {
    const f = await openPage(t, engine), { page } = f;
    f.settings.release();
    await page.locator("#prefs").click();
    await page.locator("#view-settings").waitFor();
    await page.waitForFunction(() => document.querySelector("#fullContextSegs .opt"));
    await page.locator('#nav button[data-view="agents"]').click();
    await link(page, "codex").click();
    await f.compactAsked.promise;
    assert.equal(await page.locator(row("codex") + " .ag-exp").count(), 0, "a cold controls module still delays measurement");
    f.compact.release();
    await page.locator(row("codex") + " .agent-compact").waitFor();
    await slideEndsWhole(page, "codex");
    assert.equal(f.reads(), 1);
    assert.deepEqual(f.errors, []);
  });

  test(`${engine}: a failed settings read leaves Details closed and retryable`, async (t) => {
    const f = await openPage(t, engine), { page } = f;
    f.fail(true); f.compact.release();
    await link(page, "codex").click();
    await f.settingsAsked.promise;
    f.settings.release();
    await page.waitForFunction(() => !document.querySelector('.row.agent[data-id="codex"] .ag-link').disabled);
    assert.equal(await page.locator(row("codex") + " .ag-exp").count(), 0, "failure never shows guessed defaults");
    assert.match(await page.locator("#status").textContent(), /settings unavailable/);
    f.fail(false);
    await link(page, "codex").click();
    await page.locator(row("codex") + " .agent-compact").waitFor();
    await slideEndsWhole(page, "codex");
    assert.equal(f.reads(), 2);
    assert.deepEqual(f.errors, []);
  });

  test(`${engine}: a late settings read cannot replace the agent clicked next`, async (t) => {
    const f = await openPage(t, engine), { page } = f;
    await link(page, "codex").click();
    await f.settingsAsked.promise;
    await link(page, "goose").click();
    await slideEndsWhole(page, "goose");
    const answered = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/settings");
    f.settings.release(); f.compact.release();
    await answered;
    await page.waitForFunction(() => !document.querySelector('.row.agent[data-id="codex"] .ag-link').hasAttribute("aria-busy"));
    assert.equal(await page.locator(row("codex") + " .ag-exp").count(), 0);
    assert.equal(await link(page, "goose").getAttribute("aria-expanded"), "true");
    assert.deepEqual(f.errors, []);
  });

  test(`${engine}: reduced motion waits for real settings, then opens at once`, async (t) => {
    const f = await openPage(t, engine, "en", 1180, "reduce"), { page } = f;
    await link(page, "codex").click();
    await f.settingsAsked.promise;
    assert.equal(await page.locator(row("codex") + " .ag-exp").count(), 0);
    f.settings.release(); f.compact.release();
    await page.locator(row("codex") + " .agent-compact").waitFor();
    assert.equal(await page.evaluate(() => detailSlides.length), 0, "no sheet animation with reduced motion");
    assert.deepEqual(f.errors, []);
  });

  test(`${engine}: leaving Agents while settings load does not open a hidden row`, async (t) => {
    const f = await openPage(t, engine), { page } = f;
    await link(page, "codex").click();
    await f.settingsAsked.promise;
    await page.locator('#nav button[data-view="providers"]').click();
    await page.locator("#view-providers").waitFor();
    f.settings.release(); f.compact.release();
    await page.waitForFunction(() => !document.querySelector('.row.agent[data-id="codex"] .ag-link').hasAttribute("aria-busy"));
    assert.equal(await page.locator(row("codex") + " .ag-exp").count(), 0);
    assert.deepEqual(f.errors, []);
  });
}
