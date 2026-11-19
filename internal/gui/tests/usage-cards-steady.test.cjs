// Run with Node's test runner and Playwright on the module path; see README.md.
// The Usage page's allowance cards come in at different times (liuzhengdong99
// on X: 用量界面每张卡片取数的时间不一致，导致卡片渲染时高度一直跳动——滑到
// 上面看某张卡片，视图会被拽回下面). The reader wheels up to a card while the
// page is still reading: the vendors' answers land a few at a time (the page
// asks again while X-Magpie-Reading says a read is under way) and the curves'
// history after them. The card being read (the highest starting in sight)
// stays where it is on the screen through every frame. Opened again, with
// the cards' heights known from before, no card in sight moves at all: a
// card waiting on its read or its curve keeps the room it had.
// MAGPIE_ASSETS can point at older assets.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets"));
const H = 3600e3, iso = (ms) => new Date(ms).toISOString();

// eight accounts' cards; the n-th read has the first 3 + 2n read, the rest
// still failing as a read cut short does, and the curves' history comes last
const NAMES = ["Claude Code", "Codex", "Kimi", "GLM", "MiniMax", "Copilot", "Gemini", "Qwen"];
function cards(now, read) {
  return NAMES.map((name, i) => {
    const q = { provider: "p" + i, name, icon: "openai", plan: "Pro", readAt: iso(now - 60e3) };
    if (i >= read) return { ...q, windows: [], error: "Get \"https://api.example.com/usage\": context deadline exceeded" };
    const windows = [{ name: "5 hours", used: 10 + i * 7, resetsAt: iso(now + 2 * H) }, { name: "Weekly", used: 5 + i * 3, resetsAt: iso(now + 50 * H) }];
    if (i % 3 === 0) windows.push({ name: "Monthly", used: 30, resetsAt: iso(now + 400 * H) });
    return { ...q, windows };
  });
}
function history(now) {
  return NAMES.map((_, i) => ({
    provider: "p" + i, user: "",
    lines: [{ name: "5 hours", points: Array.from({ length: 8 }, (_, k) => ({ at: iso(now - (8 - k) * 20 * 60e3), left: 100 - k * 5 - i, start: iso(now - 3 * H), resetsAt: iso(now + 2 * H) })) }],
  }));
}

function serve(lang, st) {
  const settings = { theme: "light", lang, quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (body, headers) => route.fulfill({ json: body, headers });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") {
      // a read held till the test lets the next answer go
      const n = st.reads++;
      if (n > 0) await st.gate(n);
      const read = Math.min(NAMES.length, 3 + 2 * n);
      return json(cards(st.now, read), read < NAMES.length ? { "X-Magpie-Reading": "1" } : {});
    }
    if (url.pathname === "/api/usage/quotas/history") { await st.gate("history"); return json(history(st.now)); }
    if (url.pathname === "/api/usage") {
      // the local figures under the cards, so the page scrolls
      const series = Array.from({ length: 7 }, (_, i) => ({ label: "d" + i, time: iso(st.now - (6 - i) * 24 * H), calls: 10 + i, input: 100, output: 200, cache_read: 0, cache_write: 0, cost: 0.1 }));
      const agents = Array.from({ length: 12 }, (_, i) => ({ id: "a" + i, name: "Agent " + i, calls: 10 + i, input: 100, output: 200, cost: 0.1 }));
      const models = Array.from({ length: 12 }, (_, i) => ({ id: "m" + i, calls: 10 + i, input: 100, output: 200, cost: 0.1 }));
      return json({ calls: 100, errors: 0, input: 1000, output: 2000, reasoning: 0, unpriced: 0, cost: 1, bucket: "day", series, agents, models, keys: [], path: "" });
    }
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// where each card is on the screen, every frame, until stopWatch()
const WATCH = () => {
  const seen = [];
  let on = true;
  const v = document.querySelector("#view-usage");
  const tick = () => {
    const top = v.getBoundingClientRect().top, at = {};
    for (const c of document.querySelectorAll("#subscriptionUsage > [data-key]")) at[c.dataset.key] = Math.round((c.getBoundingClientRect().top - top) * 2) / 2;
    seen.push(at);
    if (on) requestAnimationFrame(tick);
  };
  requestAnimationFrame(tick);
  window.stopWatch = () => { on = false; return seen; };
};
// the cards in sight, by where they are from the view's top
const IN_SIGHT = () => {
  const v = document.querySelector("#view-usage"), b = v.getBoundingClientRect(), at = {};
  for (const c of document.querySelectorAll("#subscriptionUsage > [data-key]")) {
    const r = c.getBoundingClientRect();
    if (r.top >= b.top && r.top < b.top + v.clientHeight) at[c.dataset.key] = r.top - b.top;
  }
  return at;
};

const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: a card read while the others come in stays where it is`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await browser.newPage({ viewport: { width, height: 700 }, reducedMotion: "no-preference" });
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const held = new Map();
        const st = {
          now: Date.now(), reads: 0,
          open: new Set(),
          gate: (k) => st.open.has(k) ? null : new Promise((r) => held.set(k, [...held.get(k) || [], r])),
        };
        const release = async (k) => {
          for (let i = 0; i < 100 && !held.has(k); i++) await page.waitForTimeout(30);
          assert.ok(held.has(k), "the page asked for " + k);
          st.open.add(k);
          for (const r of held.get(k)) r();
          held.delete(k);
        };
        t.after(() => { for (const rs of held.values()) for (const r of rs) r(); });
        await page.addInitScript(() => { localStorage.setItem("magpie.navLayout", "top"); localStorage.setItem("magpie.usageTab", "usage"); });
        await page.route("**/*", serve(lang, st));
        const view = page.locator("#view-usage");
        const key = width > 600 ? "p5" : "p4";
        const top = () => page.evaluate((k) => {
          const v = document.querySelector("#view-usage"), c = document.querySelector(`#subscriptionUsage > [data-key="${k}"]`);
          return c.getBoundingClientRect().top - v.getBoundingClientRect().top;
        }, key);

        // opened, read a few at a time while the reader goes down the page
        // and back up to a card in the middle; then the rest come in
        const visit = async () => {
          st.reads = 0; st.open.clear();
          await page.goto("http://magpie.test/?view=usage");
          await page.locator('#subscriptionUsage > [data-key="p7"]').waitFor();
          await page.locator("#usageModels .row").first().waitFor();
          const box = await view.boundingBox();
          await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
          for (let i = 0; i < 30 && await page.evaluate(() => { const v = document.querySelector("#view-usage"); return v.scrollTop + v.clientHeight < v.scrollHeight - 2; }); i++) {
            await page.mouse.wheel(0, 200);
            await page.waitForTimeout(40);
          }
          for (let i = 0; i < 60 && await top() < 60; i++) {
            await page.mouse.wheel(0, -60);
            await page.waitForTimeout(40);
          }
          await page.waitForTimeout(500);
          const sight = await page.evaluate(IN_SIGHT);
          assert.ok(Object.keys(sight).length, "a card is in sight");
          await page.evaluate(WATCH);
          for (const k of [1, 2, 3, "history"]) {
            await release(k);
            await page.waitForTimeout(400);
          }
          await page.locator(`#subscriptionUsage > [data-key="p7"] .quota-curve`).waitFor();
          await page.waitForTimeout(400);
          const seen = await page.evaluate(() => window.stopWatch());
          // each card in sight: where it went, frame by frame
          return Object.fromEntries(Object.entries(sight).map(([k, at]) => [k, [at, [...new Set(seen.map((s) => s[k]).filter((y) => y == null || Math.abs(y - at) > 1))]]]));
        };

        // the first time: the card the reader is on, the highest starting
        // in sight, stays
        const first = await visit();
        const [read] = Object.entries(first).sort((a, b) => a[1][0] - b[1][0]);
        assert.deepEqual(read[1][1], [], `${read[0]} was at ${read[1][0]}px from the view's top; it went to ${read[1][1].join(", ")}`);

        // opened again, every card's height known: none in sight moves
        await page.waitForTimeout(1200); // the heights kept (a second after)
        const again = await visit();
        for (const [k, [at, moved]] of Object.entries(again)) assert.deepEqual(moved, [], `opened again, ${k} was at ${at}px from the view's top; it went to ${moved.join(", ")}`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
