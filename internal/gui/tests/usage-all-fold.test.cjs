// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider's accounts folded under "All N accounts" (justfreefall on X:
// 既然已经有汇总信息了，能不能出个按钮把下面两个折叠？). Open by default. A
// click on the strip, or its chevron (the accounts' own ⌄, a button with
// aria-expanded), folds that provider's accounts and their "Show …" button
// away, the strip staying where it was on screen; another provider's card
// stays as it is. Remembered by provider across a reload, for the tray
// panel too, which folds and unfolds the same way. The chevron works from
// the keyboard and keeps its focus. A menu bar cell asking for a folded
// account opens them. At 440px (360px in the panel) nothing in the strip
// spills. English, Chinese (both), Japanese and German, Chromium and
// WebKit; no backend, the API is faked here. SHOTS=<dir> keeps screenshots.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const iso = (ms) => new Date(ms).toISOString();
const codex = (user, five, week) => ({ provider: "codex", name: "Codex", kind: "subscription", icon: "openai", plan: "Plus", user, readAt: iso(now),
  windows: [{ name: "5 hours", used: five, resetsAt: iso(now + 2 * 3600e3) }, { name: "7 days", used: week, resetsAt: iso(now + 4 * 24 * 3600e3) }] });
const claude = (user, five) => ({ provider: "claude", name: "Claude", kind: "subscription", icon: "claudecode-color", plan: "Max", user, readAt: iso(now),
  windows: [{ name: "5 hours", used: five, resetsAt: iso(now + 3600e3) }] });
const quotas = () => [codex("a@example.com", 30, 12), codex("b@example.com", 6, 10), claude("m1@example.com", 30), claude("m2@example.com", 70),
  ...["kimi", "glm", "minimax"].map((id) => ({ provider: id, name: id, kind: "subscription", icon: "generic", windows: [{ name: "Weekly", used: 10 }] }))];

function serve(lang, tray) {
  const settings = { theme: "light", lang, tray, quotaLeft: false, currency: "usd", usageOrder: ["kimi", "glm", "minimax", "codex", "claude"] };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/usage/quotas") return json(quotas());
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: iso(Date.now()), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// the chevron's tooltip, in each language: the words the app has already
const words = {
  en: { fold: "Fold", unfold: "Unfold" },
  zh: { fold: "收起", unfold: "展开" },
  "zh-TW": {}, ja: {}, de: {},
};

const fits = (page, sel) => page.evaluate((sel) => {
  const bad = [];
  if (document.documentElement.scrollWidth > innerWidth) bad.push("page scrolls sideways");
  for (const box of document.querySelectorAll(sel)) {
    const b = box.getBoundingClientRect();
    if (box.scrollWidth > box.clientWidth + 0.5) bad.push("strip spills");
    for (const e of box.querySelectorAll("*")) {
      const r = e.getBoundingClientRect();
      if (!r.width) continue;
      if (r.left < b.left - 0.5 || r.right > b.right + 0.5) bad.push(`${e.className?.baseVal ?? e.className ?? e.tagName} past the strip`);
    }
  }
  return bad;
}, sel);

const shot = async (page, name) => {
  if (!process.env.SHOTS) return;
  await fs.mkdir(process.env.SHOTS, { recursive: true });
  await page.screenshot({ path: path.join(process.env.SHOTS, name + ".png") });
};

async function open(browser, lang, tray, url, width, height) {
  const context = await browser.newContext({ viewport: { width, height }, reducedMotion: "reduce" });
  const page = await context.newPage();
  page.setDefaultTimeout(8000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, tray));
  await page.addInitScript(() => { try { if (!sessionStorage.getItem("set")) { localStorage.clear(); localStorage.setItem("magpie.quotaRange", "off"); sessionStorage.setItem("set", "1"); } } catch {} });
  await page.goto(url);
  return { page, errors };
}

// where the strip is on screen, and the view's scroll
const at = (page, strip, view) => page.evaluate(([s, v]) => ({ top: Math.round(document.querySelector(s).getBoundingClientRect().top), scroll: document.querySelector(v).scrollTop }), [strip, view]);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, w] of Object.entries(words)) {
    test(`${engine} ${lang}: a provider's accounts fold under All N accounts, in the window and the panel`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());

      // the Usage page: Codex's card after three, before Claude's
      const { page, errors } = await open(browser, lang, "window", "http://magpie.test/?view=usage", 440, 520);
      const card = '#subscriptionUsage > [data-key="codex"]';
      const strip = `${card} .quota-all`;
      const chev = page.locator(`${strip} .quota-all-fold`);
      await page.waitForSelector(strip);
      await page.waitForFunction((c) => document.querySelectorAll(c + " .subscription-account").length === 2, card);
      // open by default, as before
      assert.equal(await chev.getAttribute("aria-expanded"), "true");
      if (w.fold) assert.equal(await chev.getAttribute("title"), w.fold);
      assert.equal(await page.locator(`${card} .quota-more`).isVisible(), true);
      assert.deepEqual(await fits(page, ".quota-all"), []);
      // the reader scrolls the strip up near the top, Claude's card below
      await page.mouse.move(220, 300);
      for (let i = 0; i < 30 && (await page.locator(strip).boundingBox()).y > 160; i++) {
        await page.mouse.wheel(0, 60);
        await page.waitForTimeout(120);
      }
      await page.waitForTimeout(300);
      const before = await at(page, strip, "#view-usage");
      assert.ok(before.scroll > 0, "the view is scrolled to the card");
      await shot(page, `fold-usage-open-${engine}-${lang}`);

      // a click on the strip's head folds them; the strip stays put
      await page.locator(`${strip} .quota-all-head`).click();
      await page.waitForFunction((c) => !document.querySelector(c + " .subscription-account"), card);
      assert.equal(await chev.getAttribute("aria-expanded"), "false");
      if (w.unfold) assert.equal(await chev.getAttribute("title"), w.unfold);
      assert.equal(await page.locator(`${card} .quota-windows`).count(), 0, "no account's meters");
      assert.equal(await page.locator(`${card} .quota-more`).isVisible(), false, "nor its Show … button");
      assert.equal(await page.locator(`${strip} .quota-all-w`).count(), 2, "the strip as it was");
      const after = await at(page, strip, "#view-usage");
      assert.equal(after.top, before.top, "the strip didn't move");
      // another provider's card as it was
      assert.equal(await page.locator('#subscriptionUsage > [data-key="claude"] .subscription-account').count(), 2);
      assert.equal(await page.locator('#subscriptionUsage > [data-key="claude"] .quota-all-fold').getAttribute("aria-expanded"), "true");
      assert.deepEqual(await fits(page, ".quota-all"), []);
      await shot(page, `fold-usage-folded-${engine}-${lang}`);

      // remembered across a reload
      await page.reload();
      await page.waitForSelector(strip);
      assert.equal(await chev.getAttribute("aria-expanded"), "false");
      assert.equal(await page.locator(`${card} .subscription-account`).count(), 0);
      assert.deepEqual(await page.evaluate(() => JSON.parse(localStorage.getItem("magpie.usageAllFolded"))), ["codex"]);

      // the chevron unfolds; from the keyboard it folds again and keeps focus
      await chev.click();
      await page.waitForFunction((c) => document.querySelectorAll(c + " .subscription-account").length === 2, card);
      assert.equal(await chev.getAttribute("aria-expanded"), "true");
      await chev.focus();
      await page.keyboard.press("Enter");
      await page.waitForFunction((c) => !document.querySelector(c + " .subscription-account"), card);
      assert.ok(await page.evaluate((s) => document.activeElement === document.querySelector(s + " .quota-all-fold"), strip), "focus stays on the chevron");
      await page.keyboard.press(" ");
      await page.waitForFunction((c) => document.querySelectorAll(c + " .subscription-account").length === 2, card);
      assert.deepEqual(await page.evaluate(() => JSON.parse(localStorage.getItem("magpie.usageAllFolded"))), []);
      assert.deepEqual(errors, []);

      // the tray panel: folded the same way, and a menu bar cell opens them
      const panel = await open(browser, lang, "panel", "http://magpie.test/?mode=panel", 360, 760);
      const p = panel.page;
      await p.locator('#ptabs [data-ptab="usage"]').click();
      const pstrip = '#panelQuota .quota-all[data-fold="codex"]';
      await p.waitForSelector(pstrip);
      const group = (sel) => p.evaluate(([s, sel]) => document.querySelector(s).closest(".pq-group").querySelectorAll(sel).length, [pstrip, sel]);
      assert.ok(await group(".pq-card") >= 1);
      assert.equal(await group(".pq-more"), 1);
      await p.locator(`${pstrip} .quota-all-fold`).click();
      await p.waitForFunction((s) => !document.querySelector(s).closest(".pq-group").querySelector(".pq-card"), pstrip);
      assert.equal(await group(".pq-more"), 0);
      assert.equal(await p.locator(`${pstrip} .quota-all-fold`).getAttribute("aria-expanded"), "false");
      assert.ok(await p.evaluate(() => document.querySelector('#panelQuota .quota-all:not([data-fold="codex"])').closest(".pq-group").querySelector(".pq-card")), "Claude's cards as they were");
      assert.deepEqual(await fits(p, ".pq-all"), []);
      await shot(p, `fold-panel-${engine}-${lang}`);
      await p.evaluate(() => panelQuotaFocus("codex|b@example.com"));
      await p.waitForSelector('#panelQuota .pq-card[data-card="codex|b@example.com"]');
      assert.equal(await p.locator(`${pstrip} .quota-all-fold`).getAttribute("aria-expanded"), "true");
      assert.deepEqual(panel.errors, []);
    });
  }
}
