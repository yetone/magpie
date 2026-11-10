// Run with Node's test runner and Playwright on the module path; see README.md.
// Dragging a Usage card among cards of different heights (jen_celeus on X:
// "第一次拖拽会出现整个卡片界面都跟着拖动…拖到某个下面后没有明确的反馈，松手后
// 结果是插到别的卡片后"). With the page scrolled down, a press on a first-row
// card sat in the scroll edge and the first drag scrolled the whole page;
// the others were moved to slots of other cards' sizes, overlapping, with
// nothing saying where the card would go; and the drop packed the cards
// anew, into another column count, so it landed elsewhere. Now only the
// card moves, a dashed slot shows where it lands, the others stand where
// the packing puts them in that order, and letting go leaves every card
// where it was shown. Chromium and WebKit, English and Chinese; no backend,
// the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const H = 3600e3;
const iso = (ms) => new Date(ms).toISOString();
function fixtures(now) {
  const w = (name, used, h) => ({ name, used, resetsAt: iso(now + h * H) });
  const ag = (name, family, used, h) => ({ name, family, used, resetsAt: iso(now + h * H) });
  return [
    { provider: "claude", name: "Claude Code", icon: "claude-color", user: "me@example.com", plan: "Max 5x", windows: [w("5 hours", 23, 3), w("7 days", 41, 90)] },
    { provider: "opencode-zen-free", name: "OpenCode Zen", icon: "opencode", plan: "OpenCode Zen Free", windows: [] },
    { provider: "codex", name: "Codex", icon: "codex-color", user: "me@example.com", plan: "Plus", windows: [w("5 hours", 8, 2)] },
    { provider: "antigravity", name: "Antigravity", icon: "antigravity-color", user: "ag@example.com", plan: "Google AI Pro", windows: [
      ag("Gemini 3.1 Pro (High)", "Gemini", 100, 13), ag("Gemini 3 Flash", "Gemini", 20, 13), ag("Claude Sonnet 4.6", "Claude", 0, 5),
      ag("GPT-OSS 120B (Medium)", "GPT-OSS", 0, 5), ag("Gemini 2.5 Flash Image", "Image", 40, 5), ag("Tab completion", "Tab", 0, 5)] },
    { provider: "kimi", name: "Kimi", icon: "kimi", windows: [w("Weekly", 55, 90)] },
    { provider: "zai", name: "Z.ai", icon: "zai", windows: [w("5 hours", 5, 2), w("7 days", 15, 50), w("MCP", 2, 50)] },
  ];
}

function serve(lang, settings, saved, quotas) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/usage/arrange") {
      const { order } = route.request().postDataJSON();
      saved.push(order);
      settings.usageOrder = order;
      return json(settings);
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a Usage card dragged among cards of other heights lands where its slot showed`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const settings = { theme: "light", lang, tray: "panel", currency: "usd" };
      const saved = [], errors = [];
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 480 } })).newPage();
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, settings, saved, fixtures(Date.now())));
      await page.goto("http://magpie.test/?view=usage");
      const handle = (key) => page.locator(`#subscriptionUsage > [data-key="${key}"] .us-handle`);
      await handle("zai").waitFor();
      await page.waitForTimeout(500);
      // the page scrolled down until the first row's logos sit under the
      // scroll edge, 6px below the view's top
      const dy = await page.evaluate(() => {
        const v = document.querySelector("#subscriptionUsage").closest(".view");
        return document.querySelector("#subscriptionUsage > .subscription-card").getBoundingClientRect().top - v.getBoundingClientRect().top - 6;
      });
      await page.mouse.move(600, 400);
      await page.mouse.wheel(0, Math.round(dy));
      await page.waitForTimeout(400);
      const scrollTop = () => page.evaluate(() => document.querySelector("#subscriptionUsage").closest(".view").scrollTop);
      const top0 = await scrollTop();
      assert.ok(top0 > 0, "the page scrolls");
      const view = await page.evaluate(() => document.querySelector("#subscriptionUsage").closest(".view").getBoundingClientRect().top);
      const box = async (sel) => page.locator(sel).evaluate((e) => { const r = e.getBoundingClientRect(); return [r.x, r.y, r.width, r.height].map(Math.round); });
      const shown = () => page.evaluate(() => Object.fromEntries([...document.querySelectorAll("#subscriptionUsage > .subscription-card")]
        .map((c) => { const r = c.getBoundingClientRect(); return [c.dataset.key, [r.x, r.y, r.width, r.height].map(Math.round)]; })));
      const overlap = (a, b) => a[0] < b[0] + b[2] - 1 && b[0] < a[0] + a[2] - 1 && a[1] < b[1] + b[3] - 1 && b[1] < a[1] + a[3] - 1;
      const near = (a, b, msg) => a.forEach((v, i) => assert.ok(Math.abs(v - b[i]) <= 2, `${msg}: ${a} vs ${b}`));

      const drag = async (key, target, check) => {
        const h = await handle(key).boundingBox(), to = await box(`#subscriptionUsage > [data-key="${target}"]`);
        await page.mouse.move(h.x + h.width / 2, h.y + h.height / 2);
        await page.mouse.down();
        const ex = to[0] + to[2] / 2, ey = to[1] + 20;
        for (let i = 1; i <= 12; i++) await page.mouse.move(h.x + (ex - h.x) * i / 12, h.y + (ey - h.y) * i / 12);
        await page.waitForTimeout(400);
        await check?.();
        assert.equal(await page.locator("#subscriptionUsage > .drag-slot").count(), 1, "a slot shows where the card lands");
        const slot = await box("#subscriptionUsage > .drag-slot");
        const before = await shown();
        delete before[key];
        const keys = Object.keys(before);
        for (const a of keys) for (const b of keys) if (a < b) assert.ok(!overlap(before[a], before[b]), `${a} and ${b} overlap while dragged`);
        for (const a of keys) assert.ok(!overlap(before[a], slot), `${a} stands over the slot`);
        await page.mouse.up();
        await page.waitForTimeout(400);
        assert.equal(await page.locator("#subscriptionUsage > .drag-slot").count(), 0);
        const after = await shown();
        near(after[key], slot, `${key} lands in its slot`);
        for (const a of keys) near(after[a], before[a], `${a} stays where it was shown`);
        return saved.at(-1);
      };

      // a first-row card dragged across: only the card moves, not the page
      const press = await handle("claude").boundingBox();
      assert.ok(press.y + press.height / 2 < view + 36, `the press is in the scroll edge: ${press.y} ${view}`);
      const order = await drag("claude", "codex", async () => {
        assert.equal(await scrollTop(), top0, "the page doesn't scroll with the first drag");
      });
      assert.notEqual(order[0], "claude", "the order is saved");
      assert.equal(await scrollTop(), top0);
      // a card dragged up its column onto the first: in that order two
      // columns would leave less empty, but the drop keeps the three it
      // was shown in
      const n = saved.length;
      await drag("zai", "claude");
      assert.equal(saved.length, n + 1, "the second drag moves the card too");
      assert.deepEqual(errors, []);
    });
  }
}
