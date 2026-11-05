// Run with Node's test runner and Playwright on the module path; see README.md.
// The tray panel's Allowances tab folds what has nothing left (runking on
// Discord): an account whose every window is used up, a balance of 0 or
// less, behind "{n} at 0" in the foot, a click away and remembered. A failed
// or old reading, a window used part way, Codex's credits and a vendor with
// one account still going stay in sight; a menu bar cell still opens a folded
// card. APIs are mocked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ZERO_ASSETS || path.resolve(__dirname, "../assets"));
const quotas = [
  // at 0: used up, a balance of nothing, a balance below nothing
  { provider: "claude", name: "Claude Code", icon: "claude-color", user: "a@b.c", windows: [{ name: "Weekly", used: 100 }, { name: "5-hour", used: 100 }] },
  { provider: "deepseek", name: "DeepSeek", icon: "deepseek-color", windows: [], balance: "¥0.00" },
  { provider: "openrouter", name: "OpenRouter", icon: "openrouter", windows: [], balance: "-$1.20" },
  // not at 0
  { provider: "moonshot", name: "Moonshot", icon: "moonshot", windows: [], balance: "$5.00" },
  { provider: "kimi", name: "Kimi Code", icon: "kimi-color", windows: [{ name: "Weekly", used: 100 }], asOf: "2026-11-04T10:00:00Z" },
  { provider: "glm", name: "GLM Coding Plan", icon: "zhipu-color", windows: [], error: "401 token expired" },
  { provider: "codex", name: "Codex", icon: "codex-color", user: "x@y.z", windows: [{ name: "5-hour", used: 100 }, { name: "Weekly", used: 100 }] },
  { provider: "minimax", name: "MiniMax", icon: "minimax-color", windows: [{ name: "5-hour", used: 100 }, { name: "Weekly", used: 30 }] },
  { provider: "gemini", name: "Gemini", icon: "gemini-color", user: "g1@b.c", windows: [{ name: "Daily", used: 100 }] },
  { provider: "gemini", name: "Gemini", icon: "gemini-color", user: "g2@b.c", windows: [{ name: "Daily", used: 40 }] },
];
const folded = ["claude|a@b.c", "deepseek", "openrouter"];
const always = ["moonshot", "kimi", "glm", "codex|x@y.z", "minimax"];

function serve(lang) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", usageOrder: [], panelUsageHidden: [] };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
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
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: the panel's Allowances tab folds what has nothing left`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(async () => { await browser.close(); });
      const errors = [];
      const context = await browser.newContext({ viewport: { width: 440, height: 300 }, reducedMotion: "reduce" });
      await context.addInitScript(() => {
        try { if (!sessionStorage.getItem("set")) { localStorage.setItem("magpie.panelTab", "usage"); sessionStorage.setItem("set", "1"); } } catch {}
        window.__scrolled = 0;
        const scroll = Element.prototype.scrollIntoView;
        Element.prototype.scrollIntoView = function (o) { window.__scrolled++; scroll.call(this, o); };
      });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      const open = async () => {
        await page.goto("http://magpie.test/?mode=panel");
        await page.waitForFunction(() => document.querySelectorAll("#panelQuota .pq-card").length >= 3 && document.querySelector("#panelQuota .pq-foot"));
      };
      await open();
      const tr = (s, v) => page.evaluate(([s, v]) => t(s, v), [s, v]);
      const cards = () => page.evaluate(() => [...document.querySelectorAll("#panelQuota [data-card]")].map((c) => c.dataset.card));
      const zero = page.locator("#panelQuota .pq-foot .pq-zero");
      const view = page.locator("#view-agents");
      const noSideways = async () => assert(await page.evaluate(() => {
        const v = document.querySelector("#view-agents");
        return v.scrollWidth <= v.clientWidth && document.scrollingElement.scrollWidth <= innerWidth
          && [...document.querySelectorAll("#panelQuota .pq-foot, #panelQuota .pq-foot > *")].every((r) => r.getBoundingClientRect().right <= innerWidth);
      }), "nothing runs off sideways at 440px");

      // folded: the three with nothing left, and the foot says how many
      let shown = await cards();
      for (const id of folded) assert(!shown.includes(id), `${id} is folded`);
      for (const id of always) assert(shown.includes(id), `${id} stays in sight`);
      assert(shown.includes("gemini|g1@b.c") || shown.includes("gemini|g2@b.c") || shown.includes("gemini"), "a vendor with one account still going stays");
      assert.equal(await zero.textContent(), await tr("{n} at 0", { n: 3 }));
      assert.equal(await zero.getAttribute("aria-expanded"), "false");
      assert.equal(await zero.getAttribute("title"), await tr("Nothing left here: every window used up, or a balance of 0"));
      assert.equal(await zero.evaluate((b) => getComputedStyle(b).borderLeftWidth === getComputedStyle(b).borderRightWidth && getComputedStyle(b).borderLeftColor === getComputedStyle(b).borderRightColor), true, "no stripe down the button's left");
      assert.equal(await page.locator("#panelQuota .pq-foot .pq-hidden").count(), 0, "folded is not counted as hidden");
      await noSideways();

      // a click shows them, without moving the page, the keyboard on the button
      await page.mouse.move(220, 150);
      // the reader scrolls to the foot by the wheel, a turn at a time
      for (let i = 0; i < 20; i++) {
        const [left, was] = await view.evaluate((v) => [v.scrollHeight - v.clientHeight - v.scrollTop, v.scrollTop]);
        if (left <= 1) break;
        await page.mouse.wheel(0, 200);
        await page.waitForFunction((was) => document.querySelector("#view-agents").scrollTop !== was, was, { timeout: 1000 }).catch(() => {});
      }
      assert(await view.evaluate((v) => v.scrollTop > 0), "the reader scrolled down");
      const keep = await view.evaluate((v) => v.scrollTop);
      await zero.click();
      await page.waitForFunction(() => document.querySelector('#panelQuota [data-card="deepseek"]'));
      shown = await cards();
      for (const id of [...folded, ...always]) assert(shown.includes(id), `${id} is shown once opened`);
      assert.equal(await zero.textContent(), await tr("Fold the ones at 0"));
      assert.equal(await zero.getAttribute("aria-expanded"), "true");
      assert(await page.evaluate(() => document.activeElement?.matches("#panelQuota .pq-zero")), "the keyboard stays on the button");
      const after = await view.evaluate((v) => [v.scrollTop, v.scrollHeight - v.clientHeight]);
      assert.equal(after[0], Math.min(keep, after[1]), "the click keeps the reader where they were");
      assert.equal(await page.evaluate(() => window.__scrolled), 0, "nothing scrolled itself");
      await noSideways();

      // the choice is kept: open after a reload, folded again after one more
      await open();
      assert((await cards()).includes("deepseek"), "open after a reload");
      await zero.click();
      await page.waitForFunction(() => !document.querySelector('#panelQuota [data-card="deepseek"]'));
      await open();
      assert(!(await cards()).includes("deepseek"), "folded after a reload");
      assert.equal(await zero.textContent(), await tr("{n} at 0", { n: 3 }));

      // a menu bar cell for a folded card opens it all the same
      await page.evaluate(() => panelQuotaFocus("claude|a@b.c"));
      await page.waitForFunction(() => document.querySelector('#panelQuota [data-card="claude|a@b.c"]'));
      assert.equal(await zero.textContent(), await tr("{n} at 0", { n: 2 }), "the one asked for isn't counted as folded");

      assert.deepEqual(errors, []);
    });
  }
}
