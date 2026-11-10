// Run with Node's test runner and Playwright on the module path; see README.md.
// The Usage page's cards packed into columns (huoranxuanyuan, #860): a tall
// Codex card beside Antigravity and a DeepSeek balance left the short two
// mostly empty, each stretched to Codex's height. Now each card is as tall
// as itself, and the next in the order goes to the column that ends
// highest: Antigravity and DeepSeek stacked beside Codex, at the reporter's
// 897px (1569px at 175%) and 1018px windows and a wide one. Cards of a
// height keep one row in their order; a narrow window has one column. The
// order on the page is still the user's (dragging and Alt+arrows are
// usage-arrange's), no card overlaps another, and a card opened to its
// accounts in full pushes the one under it down rather than over it.
// Every card is as tall as what it holds, the last in a column too
// (jen_celeus on X: "用量卡片…充分的占满整个高度留出很大的空白"): an OpenCode
// Zen card with a plan and no windows beside Claude Code is a head's
// height, not Claude's, and Claude Code, Codex and Antigravity side by side
// (ikunbots on X) each end where their own content does.
// Chromium and WebKit, English, Chinese, Japanese and German; no backend,
// the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const H = 3600e3;
const iso = (ms) => new Date(ms).toISOString();

// as the reporter's: Codex's one account with its week, credits and a
// curve; Antigravity by family, 5 hours and 7 days; DeepSeek a balance
function fixtures(now) {
  const rw = now + 4 * 24 * H;
  const week = [];
  for (let h = 72; h >= 0; h -= 4) week.push({ at: iso(now - h * H), left: 19 + h, start: iso(rw - 168 * H), resetsAt: iso(rw) });
  const ag = (name, family, used, h) => ({ name, family, used, resetsAt: iso(now + h * H) });
  const points = [[48, 30], [36, 25], [24, 22], [12, 20], [0, 17.41]].map(([h, v]) => ({ at: iso(now - h * H), amount: v }));
  const quotas = [
    { provider: "codex", name: "Codex", icon: "codex-color", user: "me@example.com", plan: "Prolite", until: iso(now + 13 * 24 * H),
      lastServedAt: iso(now), windows: [{ name: "7 days", used: 81, resetsAt: iso(rw) }], resets: { count: 3, until: iso(now + 13 * 24 * H) } },
    { provider: "antigravity", name: "Antigravity", icon: "antigravity-color", user: "ag@example.com", plan: "Google AI Pro", windows: [
      ag("Gemini 3.1 Pro (High)", "Gemini", 100, 13), ag("Gemini 3.1 Pro (Low)", "Gemini", 100, 13),
      ag("Claude Sonnet 4.6", "Claude", 0, 5), ag("GPT-OSS 120B (Medium)", "GPT-OSS", 0, 5),
    ] },
    { provider: "deepseek", name: "DeepSeek", icon: "deepseek", windows: [], balance: "¥17.41", readAt: iso(now),
      balanceTrend: { points, fitFrom: iso(now - 48 * H), fitStart: 30, fitNow: 17.41, perDay: 6, runsOut: iso(now + 70 * H) } },
  ];
  const history = [{ provider: "codex", user: "me@example.com", lines: [{ name: "7 days", points: week }] }];
  return { quotas, history };
}
// ikunbots on X: Claude Code, Codex and Antigravity side by side in one
// row, each as tall as what it holds, their bottoms ragged
const ikun = (now) => {
  const w = (name, used, h) => ({ name, used, resetsAt: iso(now + h * H) });
  const ag = (name, family, used, h) => ({ name, family, used, resetsAt: iso(now + h * H) });
  return [
    { provider: "claude", name: "Claude Code", icon: "claude-color", user: "me@example.com", plan: "Max 5x", lastServedAt: iso(now),
      windows: [w("5 hours", 23, 3), w("7 days", 41, 90)] },
    { provider: "codex", name: "Codex", icon: "codex-color", user: "me@example.com", plan: "Plus", windows: [w("5 hours", 8, 2)] },
    { provider: "antigravity", name: "Antigravity", icon: "antigravity-color", user: "ag@example.com", plan: "Google AI Pro", windows: [
      ag("Gemini 3.1 Pro (High)", "Gemini", 100, 13), ag("Gemini 3 Flash", "Gemini", 20, 13),
      ag("Claude Sonnet 4.6", "Claude", 0, 5), ag("GPT-OSS 120B (Medium)", "GPT-OSS", 0, 5),
      ag("Gemini 2.5 Flash Image", "Image", 40, 5), ag("Tab completion", "Tab", 0, 5),
    ] },
  ];
};
// jen_celeus on X: OpenCode Zen's free plan has no windows, its card a head
const zen = (now) => [
  { provider: "claude", name: "Claude Code", icon: "claude-color", user: "me@example.com", plan: "Max 5x", lastServedAt: iso(now),
    windows: [{ name: "5 hours", used: 23, resetsAt: iso(now + 3 * H) }, { name: "7 days", used: 41, resetsAt: iso(now + 90 * H) }] },
  { provider: "opencode-zen-free", name: "OpenCode Zen", icon: "opencode", plan: "OpenCode Zen Free", windows: [] },
  { provider: "codex", name: "Codex", icon: "codex-color", user: "me@example.com", plan: "Plus", windows: [{ name: "5 hours", used: 8, resetsAt: iso(now + 2 * H) }] },
];
// cards of one height: one row, in their order
const even = (now) => ["kimi", "zai", "minimax", "moonshot"].map((p) => ({ provider: p, name: p, icon: p, windows: [{ name: "5 hours", used: 30, resetsAt: iso(now + 2 * H) }] }));
// several accounts, the second in brief until opened
const several = (now) => [
  { provider: "codex", name: "Codex", icon: "codex-color", user: "a@example.com", plan: "Plus", lastServedAt: iso(now), windows: [{ name: "5 hours", used: 20, resetsAt: iso(now + 2 * H) }, { name: "7 days", used: 40, resetsAt: iso(now + 90 * H) }] },
  { provider: "codex", name: "Codex", icon: "codex-color", user: "b@example.com", plan: "Plus", windows: [{ name: "5 hours", used: 50, resetsAt: iso(now + 2 * H) }, { name: "7 days", used: 60, resetsAt: iso(now + 90 * H) }] },
  { provider: "kimi", name: "Kimi", icon: "kimi", windows: [{ name: "Weekly", used: 10, resetsAt: iso(now + 90 * H) }] },
  { provider: "zai", name: "Z.ai", icon: "zai", windows: [{ name: "5 hours", used: 5, resetsAt: iso(now + 2 * H) }] },
  { provider: "deepseek", name: "DeepSeek", icon: "deepseek", windows: [], balance: "¥3.00", readAt: iso(now) },
];

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

// each card's box, the bottom of what is in it, and its key, in page order
const boxes = (page) => page.$$eval("#subscriptionUsage > .subscription-card", (cs) => cs.map((c) => {
  const r = c.getBoundingClientRect();
  const inner = Math.max(...[...c.children].filter((e) => e.offsetParent).map((e) => e.getBoundingClientRect().bottom));
  return { key: c.dataset.key, x: Math.round(r.left), y: Math.round(r.top), w: Math.round(r.width), h: Math.round(r.height), bottom: r.bottom, inner };
}));
const settle = (page) => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(() => requestAnimationFrame(r)))));

function sound(cards, what) {
  const end = Math.max(...cards.map((c) => c.bottom));
  for (const c of cards) {
    // as tall as what is in it, its padding and border, the last in its
    // column too: none reaches down to the tallest's end (jen_celeus on X)
    assert.ok(c.inner <= c.bottom, `${what}: ${c.key} holds what is in it: ${JSON.stringify(c)}`);
    assert.ok(c.bottom - c.inner < 16, `${what}: ${c.key} is as tall as itself (the tallest ends at ${end}): ${JSON.stringify(c)}`);
  }
  for (const a of cards) for (const b of cards) {
    if (a === b) continue;
    const apart = a.x + a.w <= b.x + 1 || b.x + b.w <= a.x + 1 || a.y + a.h <= b.y + 1 || b.y + b.h <= a.y + 1;
    assert.ok(apart, `${what}: ${a.key} and ${b.key} overlap: ${JSON.stringify([a, b])}`);
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    test(`${engine} ${lang}: the Usage page's short cards stack beside a tall one`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const pages = [];
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          for (const [name, p] of pages) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-usage-pack-${name}.png`), fullPage: true });
        }
        await browser.close();
      });
      const errors = [];
      const open = async (name, data, width) => {
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        pages.push([name, page]);
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, data));
        await page.goto("http://magpie.test/?view=usage");
        await page.locator("#subscriptionUsage > .subscription-card[data-key]").first().waitFor();
        await settle(page);
        return page;
      };
      const now = Date.now();

      // jen_celeus' case: Claude Code beside an OpenCode Zen with a plan and
      // no windows, and Codex; the Zen card a head's height, not Claude's
      for (const width of [1400, 897, 560]) {
        const page = await open(`zen-${width}`, { quotas: zen(now) }, width);
        const cards = await boxes(page);
        assert.deepEqual(cards.map((c) => c.key), ["claude", "opencode-zen-free", "codex"], "the order on the page is the user's");
        sound(cards, `zen ${width}px`);
        const [claude, z] = cards;
        assert.ok(z.h < 60, `${width}px: the Zen card is a head's height: ${JSON.stringify(z)}`);
        assert.ok(claude.h > z.h + 40, `${width}px: Claude's card is taller than Zen's: ${JSON.stringify(cards)}`);
        if (z.x !== claude.x) assert.ok(z.bottom < claude.bottom - 40, `${width}px: Zen beside Claude ends at its own content: ${JSON.stringify(cards)}`);
      }

      // ikunbots' row of three of different heights: each as tall as what
      // it holds, the bottoms ragged
      for (const width of [1400, 1018]) {
        const page = await open(`ikun-${width}`, { quotas: ikun(now) }, width);
        const cards = await boxes(page);
        assert.deepEqual(cards.map((c) => c.key), ["claude", "codex", "antigravity"], "the order on the page is the user's");
        assert.ok(cards.every((c) => c.y === cards[0].y), `${width}px: one row: ${JSON.stringify(cards)}`);
        assert.ok(Math.max(...cards.map((c) => c.bottom)) - Math.min(...cards.map((c) => c.bottom)) > 20, `${width}px: none stretched to the tallest: ${JSON.stringify(cards)}`);
        assert.ok(Math.max(...cards.map((c) => c.inner)) - Math.min(...cards.map((c) => c.inner)) > 20, `${width}px: the fixture's cards hold different heights: ${JSON.stringify(cards)}`);
        sound(cards, `ikun ${width}px`);
        // a narrow window: one column, each card as tall as itself
        await page.setViewportSize({ width: 480, height: 900 });
        await settle(page);
        const narrow = await boxes(page);
        assert.ok(narrow.every((c) => c.x === narrow[0].x), `one column: ${JSON.stringify(narrow)}`);
        for (const c of narrow) assert.ok(c.bottom - c.inner < 16, `narrow: ${c.key} is as tall as itself: ${JSON.stringify(c)}`);
        // and wide again: one row again, each as tall as itself
        await page.setViewportSize({ width, height: 900 });
        await settle(page);
        const back = await boxes(page);
        assert.ok(back.every((c) => c.y === back[0].y), `${width}px again: one row: ${JSON.stringify(back)}`);
        sound(back, `ikun ${width}px again`);
      }

      // the reporter's three, at their window and wider
      for (const width of [897, 1018, 1400]) {
        const page = await open(`reporter-${width}`, fixtures(now), width);
        const cards = await boxes(page);
        assert.deepEqual(cards.map((c) => c.key), ["codex", "antigravity", "deepseek"], "the order on the page is the user's");
        sound(cards, `${width}px`);
        const [codex, ag, ds] = cards;
        assert.equal(ag.x, ds.x, `${width}px: DeepSeek under Antigravity: ${JSON.stringify(cards)}`);
        assert.ok(ag.x > codex.x && ag.y === codex.y, `${width}px: Antigravity beside Codex, at its top`);
        assert.ok(Math.abs(ds.y - (ag.y + ag.h) - 7) <= 1, `${width}px: the cards' own gap between them: ${JSON.stringify([ag, ds])}`);
        // still stacked, not stretched: Antigravity and DeepSeek, last in
        // the column, each as tall as itself
        assert.ok(ag.bottom - ag.inner < 16, `${width}px: Antigravity as tall as itself: ${JSON.stringify(ag)}`);
        assert.ok(ds.bottom - ds.inner < 16, `${width}px: DeepSeek as tall as itself: ${JSON.stringify(ds)}`);
      }

      // cards of a height: one row, in their order; a narrow window one column
      // as wide as 1300 beside the sidebar
      const row = await open("even", { quotas: even(now) }, 1512);
      let cards = await boxes(row);
      assert.deepEqual(cards.map((c) => c.key), ["kimi", "zai", "minimax", "moonshot"]);
      assert.ok(cards.every((c) => c.y === cards[0].y), `one row: ${JSON.stringify(cards)}`);
      assert.deepEqual(cards.map((c) => c.x), [...cards.map((c) => c.x)].sort((a, b) => a - b), "left to right in their order");
      sound(cards, "even");
      await row.setViewportSize({ width: 480, height: 900 });
      await settle(row);
      cards = await boxes(row);
      assert.ok(cards.every((c) => c.x === cards[0].x), `one column: ${JSON.stringify(cards)}`);
      assert.deepEqual(cards.map((c) => c.y), [...cards.map((c) => c.y)].sort((a, b) => a - b), "top to bottom in their order");
      sound(cards, "narrow");

      // an account opened in full: the card grows, the one under it moves
      // down, nothing moves the page
      const fold = await open("fold", { quotas: several(now) }, 897);
      cards = await boxes(fold);
      sound(cards, "folded");
      const scrollY = () => fold.evaluate(() => [document.scrollingElement.scrollTop, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join());
      const before = await scrollY(), codex0 = cards.find((c) => c.key === "codex");
      await fold.locator('#subscriptionUsage > [data-key="codex"] .subscription-account.brief .quota-acct-fold').click();
      await settle(fold);
      cards = await boxes(fold);
      const codex1 = cards.find((c) => c.key === "codex");
      // what it holds grew, and the card with it
      assert.ok(codex1.inner > codex0.inner && codex1.h > codex0.h, `the card grew: ${JSON.stringify([codex0, codex1])}`);
      assert.equal(codex1.x, codex0.x, "the card clicked stays where it was");
      assert.equal(codex1.y, codex0.y, "the card clicked stays where it was");
      sound(cards, "opened");
      assert.equal(await scrollY(), before, "no click moves the page");
      assert.deepEqual(errors, []);
    });
  }
}
