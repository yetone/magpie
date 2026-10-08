// Run with Node's test runner and Playwright on the module path; see README.md.
// The tray panel's Allowances tab hides subscriptions and moves them (H20 on
// Discord): Arrange lists them, a pill hides or shows one, the logo moves it
// (Alt+arrows, drag) in the order the Usage page shares; what's hidden stays
// on the Usage page, and a menu bar cell still opens its card. APIs are mocked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const quotas = [
  { provider: "claude", name: "Claude Code", icon: "claude-color", user: "a@b.c", windows: [{ name: "Weekly", used: 17.6 }, { name: "5-hour", used: 42.2 }] },
  { provider: "codex", name: "Codex", icon: "codex-color", user: "x@y.z", windows: [{ name: "5-hour", used: 8 }, { name: "Weekly", used: 100 }] },
  { provider: "codex", name: "Codex", icon: "codex-color", user: "other@例子.test", windows: [{ name: "Weekly", used: 25 }] },
  { provider: "deepseek", name: "DeepSeek", icon: "deepseek-color", windows: [], balance: "¥12.30" },
  { provider: "kimi", name: "Kimi Code 的一个名字很长很长的订阅 with a long name", icon: "kimi-color", windows: [{ name: "Weekly", used: 30 }] },
];

function serve(lang, posts, fail, saved = {}) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", usageOrder: [], panelUsageHidden: ["kimi"], ...saved };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/usage/arrange") {
      const body = route.request().postDataJSON();
      posts.push(body);
      if (fail.next) { fail.next = false; return route.fulfill({ status: 500, body: "disk full" }); }
      if (body.order) settings.usageOrder = body.order;
      if (body.panelHidden) settings.panelUsageHidden = body.panelHidden;
      return json(settings);
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/usage") return json({ days: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    test(`${engine} ${lang}: the panel's Allowances tab hides and moves subscriptions`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(async () => { await browser.close(); });
      const errors = [], posts = [], fail = { next: false };
      const context = await browser.newContext({ viewport: { width: 440, height: 300 }, reducedMotion: "reduce" });
      await context.addInitScript(() => {
        try { localStorage.setItem("magpie.panelTab", "usage"); } catch {}
        window.__scrolled = 0;
        const scroll = Element.prototype.scrollIntoView;
        Element.prototype.scrollIntoView = function (o) { window.__scrolled++; scroll.call(this, o); };
      });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts, fail));
      await page.goto("http://magpie.test/?mode=panel");
      await page.waitForFunction(() => document.querySelectorAll("#panelQuota .pq-card").length >= 3);
      const tr = (s, v) => page.evaluate(([s, v]) => t(s, v), [s, v]);
      const cards = () => page.evaluate(() => [...document.querySelectorAll("#panelQuota .pq-card")].map((c) => c.dataset.card));
      const rows = () => page.evaluate(() => [...document.querySelectorAll("#panelQuota .pq-arow")].map((r) => r.dataset.key));
      const view = page.locator("#view-agents");
      const noSideways = async () => assert(await page.evaluate(() => {
        const v = document.querySelector("#view-agents");
        return v.scrollWidth <= v.clientWidth && document.scrollingElement.scrollWidth <= innerWidth
          && [...document.querySelectorAll("#panelQuota .pq-arow, #panelQuota .pq-foot")].every((r) => r.scrollWidth <= r.clientWidth + 1 && r.getBoundingClientRect().right <= innerWidth);
      }), "nothing runs off sideways at 440px");

      // what the settings hide is left out, and the foot says how many
      assert(!(await cards()).includes("kimi"), "the hidden subscription has no card");
      assert.equal(await page.locator("#panelQuota .pq-foot .pq-hidden").textContent(), await tr("{n} hidden", { n: 1 }));
      assert.equal(await page.locator("#panelQuota .pq-arrange").textContent(), await tr("Arrange"));
      await noSideways();

      // Arrange: a click that doesn't move the page; a row each, in order
      // the reader scrolls to the foot, as a reader does: by the wheel
      await page.mouse.move(220, 150);
      await page.mouse.wheel(0, 1000);
      await page.waitForFunction(() => { const v = document.querySelector("#view-agents"); return v.scrollTop > 0 && v.scrollTop >= v.scrollHeight - v.clientHeight - 1; });
      const keep = await view.evaluate((v) => v.scrollTop);
      await page.locator("#panelQuota .pq-arrange").click();
      await page.waitForSelector("#panelQuota .pq-arow");
      const after = await view.evaluate((v) => [v.scrollTop, v.scrollHeight - v.clientHeight]);
      assert.equal(after[0], Math.min(keep, after[1]), "Arrange keeps the reader where they were");
      assert.deepEqual(await rows(), ["claude", "codex", "deepseek", "kimi"]);
      assert.equal(await page.locator("#panelQuota .pq-anote").textContent(), await tr("Hiding is for this panel only: routing, the Usage page and the menu bar still use what's hidden. The order is the Usage page's too."));
      assert(await page.locator('#panelQuota .pq-arow[data-key="kimi"]').evaluate((r) => r.classList.contains("put-away")));
      assert.equal(await page.locator('#panelQuota .pq-arow[data-key="kimi"] .pq-show').textContent(), await tr("Show"));
      assert.equal(await page.locator('#panelQuota .pq-arow[data-key="claude"] .pq-show').textContent(), await tr("Hide"));
      for (const w of await page.evaluate(() => [...document.querySelectorAll("#panelQuota .pq-arow")].map((r) => getComputedStyle(r).borderLeftWidth))) assert.equal(w, "0px", "no stripe down a row's left");
      await noSideways();

      // the grip by the logo is there on hover only
      await page.mouse.move(5, 5);
      const grip = (key) => page.locator(`#panelQuota .pq-arow[data-key="${key}"] .pq-ahandle`).evaluate((h) => getComputedStyle(h, "::before").opacity);
      await page.waitForFunction(() => getComputedStyle(document.querySelector('#panelQuota .pq-arow[data-key="codex"] .pq-ahandle'), "::before").opacity === "0");
      await page.locator('#panelQuota .pq-arow[data-key="codex"] .pq-aname').hover();
      await page.waitForFunction(() => getComputedStyle(document.querySelector('#panelQuota .pq-arow[data-key="codex"] .pq-ahandle'), "::before").opacity === "1");
      assert.equal(await grip("claude"), "0");

      // hide and show: the pill posts what's hidden, and doesn't scroll
      const top = await view.evaluate((v) => v.scrollTop);
      await page.locator('#panelQuota .pq-arow[data-key="kimi"] .pq-show').click();
      await page.waitForFunction(() => !document.querySelector('#panelQuota .pq-arow[data-key="kimi"]').classList.contains("put-away"));
      await page.locator('#panelQuota .pq-arow[data-key="deepseek"] .pq-show').click();
      await page.waitForFunction(() => document.querySelector('#panelQuota .pq-arow[data-key="deepseek"]').classList.contains("put-away"));
      assert.deepEqual(posts.splice(0), [{ panelHidden: [] }, { panelHidden: ["deepseek"] }]);
      assert.equal(await view.evaluate((v) => v.scrollTop), top, "a pill's click doesn't move the page");
      assert(await page.evaluate(() => document.activeElement?.closest(".pq-arow")?.dataset.key === "deepseek"), "the keyboard stays on the pill");

      // a save that fails is put back
      fail.next = true;
      await page.locator('#panelQuota .pq-arow[data-key="claude"] .pq-show').click();
      await page.waitForFunction(() => document.querySelector("#status")?.textContent.includes("disk full") || document.body.textContent.includes("disk full"));
      assert(!(await page.locator('#panelQuota .pq-arow[data-key="claude"]').evaluate((r) => r.classList.contains("put-away"))), "the failed hide is undone");
      posts.splice(0);

      // Alt+arrow moves a row, the keyboard staying on its logo
      await page.locator('#panelQuota .pq-arow[data-key="claude"] .pq-ahandle').focus();
      await page.keyboard.press("Alt+ArrowDown");
      await page.waitForFunction(() => document.querySelector("#panelQuota .pq-arow")?.dataset.key === "codex");
      assert.deepEqual(await rows(), ["codex", "claude", "deepseek", "kimi"]);
      assert.deepEqual(posts.splice(0), [{ order: ["codex", "claude", "deepseek", "kimi"] }]);
      assert(await page.evaluate(() => document.activeElement?.matches('.pq-arow[data-key="claude"] .pq-ahandle')));

      // a drag by the logo: DeepSeek to the top
      const from = await page.locator('#panelQuota .pq-arow[data-key="deepseek"] .pq-ahandle').boundingBox();
      const to = await page.locator('#panelQuota .pq-arow[data-key="codex"]').boundingBox();
      await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2);
      await page.mouse.down();
      for (let i = 1; i <= 8; i++) await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2 + (to.y - from.y - 4) * i / 8);
      await page.mouse.up();
      await page.waitForFunction(() => document.querySelector("#panelQuota .pq-arow")?.dataset.key === "deepseek");
      assert.deepEqual(posts.splice(0), [{ order: ["deepseek", "codex", "claude", "kimi"] }]);

      // Done: the cards in the new order, DeepSeek left out. The reader brings
      // Done out from under the sticky tabs with the wheel: Playwright's own
      // scroll into view is code's, and the page puts it back
      await page.mouse.move(220, 150);
      for (let i = 0; i < 20; i++) {
        const [by, was] = await page.evaluate(() => {
          const d = document.querySelector("#panelQuota .pq-done").getBoundingClientRect();
          const top = document.querySelector("#ptabs").getBoundingClientRect().bottom, v = document.querySelector("#view-agents");
          const foot = v.getBoundingClientRect().bottom;
          return [d.top < top ? d.top - top - 8 : d.bottom > foot ? d.bottom - foot + 8 : 0, v.scrollTop];
        });
        if (!by) break;
        await page.mouse.wheel(0, by);
        await page.waitForFunction((was) => document.querySelector("#view-agents").scrollTop !== was, was, { timeout: 1000 }).catch(() => {});
      }
      await page.locator("#panelQuota .pq-done").click();
      await page.waitForFunction(() => !document.querySelector("#panelQuota .pq-arow"));
      assert.deepEqual(await cards(), ["codex|x@y.z", "claude|a@b.c", "kimi"]);
      assert.equal(await page.locator("#panelQuota .pq-foot .pq-hidden").textContent(), await tr("{n} hidden", { n: 1 }));
      await noSideways();
      assert.equal(await page.evaluate(() => window.__scrolled), 0, "nothing scrolled itself");

      // a menu bar cell for the hidden one opens its card all the same
      // Reduced motion makes the flash one frame long, which a poll can miss:
      // what lit up is recorded as it is lit
      await page.evaluate(() => {
        window.__flashed = [];
        new MutationObserver((ms) => {
          for (const m of ms) if (m.target.matches?.(".pq-card.flash")) window.__flashed.push(m.target.dataset.card);
        }).observe(document.querySelector("#panelQuota"), { subtree: true, attributes: true, attributeFilter: ["class"] });
        panelQuotaFocus("deepseek");
      });
      await page.waitForFunction(() => window.__flashed.includes("deepseek") && document.querySelector('#panelQuota .pq-card[data-card="deepseek"]'));
      assert.equal(await page.locator("#panelQuota .pq-foot .pq-hidden").count(), 0, "the one asked for isn't counted as hidden");
      // put away, the panel hides it again
      await page.evaluate(() => {
        Object.defineProperty(document, "hidden", { configurable: true, get: () => true });
        document.dispatchEvent(new Event("visibilitychange"));
      });
      await page.waitForFunction(() => !document.querySelector('#panelQuota .pq-card[data-card="deepseek"]'));

      // the Usage page keeps every card, in the shared order. The window reads
      // the settings saved above from magpie, as it does: set into state by hand,
      // its own load could answer after and put the first order back
      const win = await context.newPage();
      win.on("pageerror", (e) => errors.push(e.message));
      await win.route("**/*", serve(lang, [], {}, { usageOrder: ["deepseek", "codex", "claude", "kimi"], panelUsageHidden: ["deepseek"] }));
      await win.goto("http://magpie.test/?view=usage&tab=usage");
      await win.waitForFunction(() => document.querySelectorAll("#subscriptionUsage > [data-key]").length >= 4);
      assert.deepEqual(await win.evaluate(() => [...new Set([...document.querySelectorAll("#subscriptionUsage > [data-key]")].map((c) => c.dataset.key))]), ["deepseek", "codex", "claude", "kimi"]);

      assert.deepEqual(errors, []);
    });
  }
}
