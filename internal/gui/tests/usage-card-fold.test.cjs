// Run with Node's test runner and Playwright on the module path; see README.md.
// PILIHU2023 on X (Usage > Overview): 「用量的概览页卡片，添加折叠按钮，随后
// 点击后仅显示用量，同时控制每个卡片高度宽度一致」. A card of one account or
// key has a fold in its head; folded, it shows its windows' figures and bars
// alone (no curve, resets line or "Updated"), and stays so across a reload.
// Folded cards are one width and one height, in rows that line up: a
// provider's accounts folded under "All N accounts" are a row too, and
// accounts each in brief (no All N accounts: their plans differ) take
// whole rows. A card left open is as tall as what it holds, never cut and
// never stretched (jen_celeus on X). Unfolding gives the card back as it was.
// Chromium and WebKit, at 1400, 900 and 400px, in every language; no
// backend, the API is faked here.
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
  const rw = now + 4 * 24 * H;
  const week = [];
  for (let h = 72; h >= 0; h -= 4) week.push({ at: iso(now - h * H), left: 19 + h, start: iso(rw - 168 * H), resetsAt: iso(rw) });
  const points = [[48, 30], [36, 25], [24, 22], [12, 20], [0, 17.41]].map(([h, v]) => ({ at: iso(now - h * H), amount: v }));
  const quotas = [
    { provider: "claude", name: "Claude Code", icon: "claude-color", user: "me@example.com", plan: "Max 5x", lastServedAt: iso(now), readAt: iso(now),
      windows: [w("5 hours", 23, 3), w("7 days", 41, 90)] },
    { provider: "codex", name: "Codex", icon: "codex-color", user: "me@example.com", plan: "Pro", lastServedAt: iso(now), readAt: iso(now),
      windows: [{ name: "7 days", used: 81, resetsAt: iso(rw) }], resets: { count: 3, until: iso(now + 13 * 24 * H) } },
    { provider: "antigravity", name: "Antigravity", icon: "antigravity-color", user: "ag@example.com", plan: "Google AI Pro", readAt: iso(now), windows: [
      ag("Gemini 3.1 Pro (High)", "Gemini", 100, 13), ag("Gemini 3 Flash", "Gemini", 20, 13),
      ag("Claude Sonnet 4.6", "Claude", 0, 5), ag("GPT-OSS 120B (Medium)", "GPT-OSS", 0, 5),
    ] },
    { provider: "kimi", name: "Kimi", icon: "kimi", windows: [w("Weekly", 10, 90)] },
    { provider: "deepseek", name: "DeepSeek", icon: "deepseek", windows: [], balance: "¥17.41", readAt: iso(now),
      balanceTrend: { points, fitFrom: iso(now - 48 * H), fitStart: 30, fitNow: 17.41, perDay: 6, runsOut: iso(now + 70 * H) } },
    { provider: "zai", name: "Z.ai", icon: "zai", user: "a@example.com", plan: "Pro", lastServedAt: iso(now), windows: [w("5 hours", 20, 2), w("7 days", 40, 90)] },
    { provider: "zai", name: "Z.ai", icon: "zai", user: "b@example.com", plan: "Pro", windows: [w("5 hours", 50, 2), w("7 days", 60, 90)] },
    { provider: "minimax", name: "MiniMax", icon: "minimax", user: "c@example.com", plan: "Plus", lastServedAt: iso(now), windows: [w("5 hours", 30, 2)] },
    { provider: "minimax", name: "MiniMax", icon: "minimax", user: "d@example.com", plan: "Max", windows: [w("5 hours", 70, 2)] },
  ];
  const history = [{ provider: "codex", user: "me@example.com", lines: [{ name: "7 days", points: week }] }];
  return { quotas, history };
}

function serve(lang, data) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: true, currency: "cny" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (body) => route.fulfill({ json: body });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(data.quotas);
    if (url.pathname === "/api/usage/quotas/history") return json(data.history || []);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const boxes = (page) => page.$$eval("#subscriptionUsage > .subscription-card", (cs) => cs.map((c) => {
  const r = c.getBoundingClientRect();
  const inner = Math.max(...[...c.children].filter((e) => e.offsetParent).map((e) => e.getBoundingClientRect().bottom));
  return { key: c.dataset.key, folded: c.dataset.folded || "", x: Math.round(r.left), y: Math.round(r.top), w: Math.round(r.width), h: Math.round(r.height), bottom: r.bottom, inner };
}));
const settle = (page) => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(() => requestAnimationFrame(r)))));
const fold = (page, key) => page.locator(`#subscriptionUsage > [data-key="${key}"] > .subscription-head > .quota-acct-fold`);

function apart(cards, what) {
  for (const a of cards) {
    assert.ok(a.inner <= a.bottom + 0.5, `${what}: ${a.key} holds what is in it: ${JSON.stringify(a)}`);
    for (const b of cards) {
      if (a === b) continue;
      const ok = a.x + a.w <= b.x + 1 || b.x + b.w <= a.x + 1 || a.y + a.h <= b.y + 1 || b.y + b.h <= a.y + 1;
      assert.ok(ok, `${what}: ${a.key} and ${b.key} overlap: ${JSON.stringify([a, b])}`);
    }
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of (process.env.LANGS ? process.env.LANGS.split(",") : ["en", "zh", "zh-TW", "ja", "de"])) {
    test(`${engine} ${lang}: a Usage card folds to its figures, folded cards one size`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      let page;
      t.after(async () => {
        if (process.env.ARTIFACT_DIR && page) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-usage-card-fold.png`), fullPage: true }).catch(() => {});
        }
        await browser.close();
      });
      const errors = [];
      const context = await browser.newContext({ viewport: { width: 1400, height: 900 }, reducedMotion: "reduce" });
      page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, fixtures(Date.now())));
      await page.goto("http://magpie.test/?view=usage");
      await page.locator("#subscriptionUsage > .subscription-card[data-key]").first().waitFor();
      await settle(page);

      // every card of one account or key has its fold, open; a provider of
      // several keeps its accounts' folds and "All N accounts"
      for (const key of ["claude", "codex", "antigravity", "kimi", "deepseek"]) {
        assert.equal(await fold(page, key).getAttribute("aria-expanded"), "true", `${key}: a fold in its head, open`);
      }
      for (const key of ["zai", "minimax"]) assert.equal(await fold(page, key).count(), 0, `${key}: several accounts, their own folds, none in the head`);
      assert.equal(await page.locator('#subscriptionUsage > [data-key="minimax"] .quota-all').count(), 0, "MiniMax's plans differ: no All 2 accounts");
      const codex = page.locator('#subscriptionUsage > [data-key="codex"]');
      assert.ok(await codex.locator(".quota-resets").count() > 0, "open: Codex's resets line");
      const open = await boxes(page);
      assert.ok(open.every((c) => !c.folded), "nothing folded at first");

      // folding Codex: its figures and bars alone, the fold turned
      await fold(page, "codex").click();
      await page.waitForSelector('#subscriptionUsage > [data-key="codex"][data-folded]');
      assert.equal(await fold(page, "codex").getAttribute("aria-expanded"), "false");
      assert.equal(await codex.locator(".quota-resets, .quota-curve, .quota-read, .quota-history").count(), 0, "folded: no resets, curve or read line");
      assert.equal(await codex.locator(".quota-windows.brief").count(), 1, "folded: the windows in brief");
      assert.ok(await codex.locator(".quota-windows .quota-track").count() > 0, "folded: the bars are there");

      // every card folded: the provider of several folds its accounts under
      // All N accounts, the rest from their heads
      for (const key of ["claude", "antigravity", "kimi", "deepseek"]) await fold(page, key).click();
      await page.locator('#subscriptionUsage > [data-key="zai"] .quota-all-fold').click();
      await page.locator('#subscriptionUsage > [data-key="minimax"] .subscription-account:not(.brief) .quota-acct-fold').click();
      await page.waitForSelector('#subscriptionUsage > [data-key="minimax"][data-folded="n"]');
      await settle(page);

      const sameSize = async (what) => {
        const cards = await boxes(page);
        apart(cards, what);
        assert.ok(cards.every((c) => c.folded), `${what}: every card folded: ${JSON.stringify(cards)}`);
        const ones = cards.filter((c) => c.folded === "1");
        // Z.ai's accounts under All 2 accounts are a card of one row too
        assert.equal(ones.length, 6, what);
        assert.equal(cards.find((c) => c.key === "minimax").folded, "n", what);
        for (const c of cards) assert.ok(Math.abs(c.w - cards[0].w) <= 1, `${what}: one width: ${JSON.stringify(cards)}`);
        for (const c of ones) assert.ok(Math.abs(c.h - ones[0].h) <= 1, `${what}: one height: ${JSON.stringify(ones)}`);
        // whole rows: a card of several is n rows of a lone one's height
        const row = ones[0].h + 7;
        for (const c of cards) {
          const n = (c.h + 7) / row;
          assert.ok(Math.abs(n - Math.round(n)) * row <= 2, `${what}: ${c.key} is whole rows of ${row}px: ${JSON.stringify(c)}`);
        }
        // the rows line up: every top is the first's plus whole rows
        const top = Math.min(...cards.map((c) => c.y));
        for (const c of cards) {
          const n = (c.y - top) / row;
          assert.ok(Math.abs(n - Math.round(n)) * row <= 2, `${what}: ${c.key} starts on a row: ${JSON.stringify(cards)}`);
        }
        return cards;
      };
      const wide = await sameSize("1400px");
      if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-usage-card-folded.png`) });
      assert.ok(new Set(wide.map((c) => c.x)).size > 1, "1400px: side by side");

      // kept across a reload (a restart)
      await page.reload();
      await page.locator("#subscriptionUsage > .subscription-card[data-key]").first().waitFor();
      await settle(page);
      await sameSize("1400px after a reload");
      assert.equal(await fold(page, "codex").getAttribute("aria-expanded"), "false", "folded after a reload");

      for (const width of [900, 400]) {
        await page.setViewportSize({ width, height: 900 });
        await settle(page);
        const cards = await sameSize(`${width}px`);
        if (width === 400) assert.ok(cards.every((c) => c.x === cards[0].x), `400px: one column: ${JSON.stringify(cards)}`);
        const sideways = await page.evaluate(() => document.scrollingElement.scrollWidth - innerWidth);
        assert.ok(sideways <= 0, `${width}px: no sideways scroll (${sideways})`);
      }
      await page.setViewportSize({ width: 1400, height: 900 });
      await settle(page);

      // Antigravity unfolded: as tall as what it holds, cut nowhere, the
      // folded ones still one height
      await fold(page, "antigravity").click();
      await page.waitForSelector('#subscriptionUsage > [data-key="antigravity"]:not([data-folded])');
      await settle(page);
      const mixed = await boxes(page);
      apart(mixed, "Antigravity open");
      const ag = mixed.find((c) => c.key === "antigravity");
      assert.ok(ag.bottom - ag.inner < 16, `the open card is as tall as itself: ${JSON.stringify(ag)}`);
      const ones = mixed.filter((c) => c.folded === "1");
      for (const c of ones) assert.ok(Math.abs(c.h - ones[0].h) <= 1, `the folded ones one height: ${JSON.stringify(ones)}`);

      // Codex unfolded again: its resets line back, as tall as itself
      await fold(page, "codex").click();
      await page.waitForSelector('#subscriptionUsage > [data-key="codex"]:not([data-folded])');
      await settle(page);
      assert.ok(await codex.locator(".quota-resets").count() > 0, "unfolded: Codex's resets line again");
      const back = (await boxes(page)).find((c) => c.key === "codex");
      assert.ok(back.bottom - back.inner < 16, `unfolded Codex is as tall as itself: ${JSON.stringify(back)}`);

      assert.deepEqual(errors, []);
    });
  }
}
