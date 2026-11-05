// Run with Node's test runner and Playwright on the module path; see README.md.
// #1834: Accounts and keys has a refresh beside its heading that reads the
// accounts' allowances again, by hand only. Opening the page reads them in
// the ordinary way; the button asks /api/usage/quotas?asked=1 (as the
// Usage page's Refresh now does), turns while the read is on its way, and
// the account's windows show what came back. Nothing asks again by
// itself. A list with keys only has no button. In every language, at a
// window's width and at 420px, in both engines; the click moves nothing.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const iso = (ms) => new Date(ms).toISOString();
const acct = { id: "claude", provider: "claude", name: "Claude", who: "work@example.com", kind: "account", agent: "claude", model: "claude-opus-5", known: true, used: 40, plan: "Max" };
const key = { id: "deepseek/k1", provider: "deepseek", name: "DeepSeek", who: "main", kind: "key", model: "deepseek-chat" };
const routeOf = (order) => ({
  id: 7, seq: 7, time: iso(now - 2000), agent: "claude", model: order[0].model, provider: order[0].provider, done: true, status: 200, ms: 900, tokens: 1500,
  order, tries: [{ id: order[0].id, model: order[0].model, start: iso(now - 2000), done: true, ms: 800, status: 200 }],
});
const card = (used) => [{ provider: "claude", name: "Claude", kind: "subscription", user: "work@example.com", plan: "Max",
  windows: [{ name: "5 hours", used, resetsAt: iso(now + 130 * 60e3) }] }];

function serve(lang, order, asks, held) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light", quotaLeft: false } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 7, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: [routeOf(order)] });
    }
    if (url.pathname === "/api/usage/quotas") {
      const asked = url.searchParams.get("asked") === "1";
      // the button's read is of every account, a saved Claude one's too (#5380)
      asks.push(asked && url.searchParams.get("all") === "1" ? "all" : asked);
      if (!asked) return json(card(40));
      await held; // the vendor takes its time
      return json(card(70));
    }
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { title: "Read the accounts' allowances again", label: "Refresh now", before: "5 hours 40% used", after: "5 hours 70% used" },
  zh: { title: "重新读取各账号的额度", label: "立即刷新", before: "5 小时 已用 40%", after: "5 小时 已用 70%" },
  "zh-TW": { title: "重新讀取各帳號的額度", label: "立即重新整理", before: "5 小時 已用 40%", after: "5 小時 已用 70%" },
  ja: { title: "アカウントの利用枠を読み直します", label: "今すぐ更新", before: "5 時間 使用済み 40%", after: "5 時間 使用済み 70%" },
  de: { title: "Kontingente der Konten neu lesen", label: "Jetzt aktualisieren", before: "5 Stunden 40% verbraucht", after: "5 Stunden 70% verbraucht" },
};

// the button wholly in sight by the reader's own wheel: the app undoes a
// scroll nobody asked for
async function wheelTo(page, b) {
  const v = await page.locator("#view-routing").boundingBox();
  await page.mouse.move(v.x + v.width / 2, v.y + v.height / 2);
  for (let i = 0; i < 80; i++) {
    const r = await b.boundingBox();
    if (r && r.y >= v.y + 40 && r.y + r.height <= v.y + v.height - 40) return page.waitForTimeout(300);
    await page.mouse.wheel(0, r && r.y < v.y + 40 ? -60 : 60);
    await page.waitForTimeout(40);
  }
  throw new Error("the refresh did not come into sight");
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: Accounts and keys reads the allowances again when asked, and only then`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(words)) {
      for (const width of [1100, 420]) {
        await t.test(`${lang} at ${width}px`, async () => {
          const w = words[lang];
          const ctx = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
          try {
            const page = await ctx.newPage();
            page.setDefaultTimeout(5000);
            page.setDefaultNavigationTimeout(60000);
            const errors = [], asks = [];
            let release;
            const held = new Promise((y) => { release = y; });
            page.on("pageerror", (e) => errors.push(e.message));
            await page.route("**/*", serve(lang, [acct], asks, held));
            await page.goto("http://magpie.test/?view=routing");
            const tip = (want) => page.waitForFunction((want) => [...document.querySelectorAll(".rt-act")].some((e) => e.textContent.includes("work@example.com") && (e.querySelector(".st")?.title || "").includes(want)), want);
            await tip(w.before);
            const b = page.locator("#rtReadAllowances");
            await b.waitFor({ state: "visible" });
            assert.equal(await b.getAttribute("title"), w.title);
            assert.equal(await b.getAttribute("aria-label"), w.label);
            // beside the heading, on its line
            const [head, bb] = await b.evaluate((e) => [e.closest(".row-head").getBoundingClientRect(), e.getBoundingClientRect()].map((r) => ({ y: r.y, height: r.height })));
            assert.ok(bb.y >= head.y - 4 && bb.y + bb.height <= head.y + head.height + 4, JSON.stringify({ head, bb }));
            assert.ok(asks.length >= 1 && asks.every((a) => !a), "opening reads in the ordinary way: " + asks);
            await wheelTo(page, b);
            const top = await page.locator("#view-routing").evaluate((v) => v.scrollTop);
            const box = await b.boundingBox();
            await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2);
            await page.waitForFunction(() => document.querySelector("#rtReadAllowances").classList.contains("busy"));
            assert.deepEqual(asks.filter(Boolean), ["all"], "one asked read, of every account");
            // a second click while it turns asks nothing more
            await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2);
            release();
            await tip(w.after);
            await page.waitForFunction(() => !document.querySelector("#rtReadAllowances").classList.contains("busy"));
            assert.equal(await page.locator("#view-routing").evaluate((v) => v.scrollTop), top, "the click moved the page");
            // and nothing reads again by itself
            await page.waitForTimeout(2500);
            assert.equal(asks.filter(Boolean).length, 1, "asked again by itself: " + asks);
            assert.deepEqual(errors, []);
          } finally {
            await ctx.close();
          }
        });
      }
    }
  });

  test(`${engine}: a list of keys only has no allowances to read`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 1100, height: 760 }, reducedMotion: "reduce" })).newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    await page.route("**/*", serve("en", [key], [], Promise.resolve()));
    await page.goto("http://magpie.test/?view=routing");
    await page.locator(".rt-act", { hasText: "main" }).first().waitFor();
    await page.waitForTimeout(1200);
    assert.equal(await page.locator("#rtReadAllowances").isVisible(), false);
  });
}
