// Run with Node's test runner and Playwright on the module path; see README.md.
// Warm up on the Usage page's card (01huadalang on Discord: many Codex
// accounts, one to start its 5-hour countdown by hand, looked for on the
// card, not in Settings): a Codex or Claude account whose 5-hour window
// isn't running has a Warm up on its line, which sends that account the
// request Settings' Warm up now sends (POST /api/warm-now {agent, user}).
// Not running: nothing used and its reset a whole window away, a reset gone
// by, or a Claude account with no window read. One running, or an account
// magpie isn't signed in to, has none. Once warmed it goes; refused, the
// reason is in the status line and the button comes back. en, zh, zh-TW,
// ja, de at 1100px and 390px; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const iso = (ms) => new Date(ms).toISOString();
const H = 3600e3;
const quotas = [
  // not started: nothing used, its reset five hours away
  { provider: "codex", name: "Codex", icon: "codex-color", plan: "Plus", user: "idle-account-with-a-long-name@example.com", lastServedAt: iso(now), windows: [{ name: "5 hours", used: 0, resetsAt: iso(now + 5 * H) }, { name: "7 days", used: 12, resetsAt: iso(now + 50 * H) }] },
  // running
  { provider: "codex", name: "Codex", icon: "codex-color", plan: "Pro", user: "busy@example.com", windows: [{ name: "5 hours", used: 30, resetsAt: iso(now + 2 * H) }, { name: "7 days", used: 40, resetsAt: iso(now + 50 * H) }] },
  // its reset gone by: started over from nothing
  { provider: "codex", name: "Codex", icon: "codex-color", plan: "Plus", user: "Ran-Out@Example.com", windows: [{ name: "5 hours", used: 100, resetsAt: iso(now - H) }] },
  // unused, but started a while ago: running
  { provider: "codex", name: "Codex", icon: "codex-color", plan: "Plus", user: "zero@example.com", windows: [{ name: "5 hours", used: 0, resetsAt: iso(now + 3 * H) }] },
  // not one magpie is signed in to
  { provider: "codex", name: "Codex", icon: "codex-color", plan: "Plus", user: "elsewhere@example.com", windows: [{ name: "5 hours", used: 0, resetsAt: iso(now + 5 * H) }] },
  // a Claude account never used: no window read
  { provider: "claude", name: "Claude Code", icon: "claude-color", plan: "Max", user: "claude@example.com", windows: [] },
  // a Claude Ultra read with a week and no 5-hour window: it has none to start (shangyuan0818 on X)
  { provider: "claude", name: "Claude Code", icon: "claude-color", plan: "Ultra", user: "ultra@example.com", windows: [{ name: "7 days", used: 12, resetsAt: iso(now + 72 * H) }] },
];
const WHY = "429 Too Many Requests: usage limit reached";

function serve(lang, asked) {
  const settings = { lang, theme: "light", currency: "usd", codexUsers: ["idle-account-with-a-long-name@example.com", "busy@example.com", "ran-out@example.com", "zero@example.com"], claudeUsers: ["claude@example.com", "ultra@example.com"] };
  const state = { agents: [], profiles: [], settings };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data, status = 200) => r.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/warm-now") {
      const b = r.request().postDataJSON();
      asked.push([b.agent, b.user]);
      return b.agent === "claude" ? json({ error: WHY }, 400) : json({ ok: true });
    }
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

const W = {
  en: { go: "Warm up", tip: "The 5-hour window hasn't started", done: "Warmed up idle-account-with-a-long-name@example.com", failed: "Couldn't warm up claude@example.com: " },
  zh: { go: "预热", tip: "5 小时窗口还没开始", done: "已预热 idle-account-with-a-long-name@example.com", failed: "claude@example.com 预热失败：" },
  "zh-TW": { go: "預熱", tip: "5 小時視窗還沒開始", done: "已預熱 idle-account-with-a-long-name@example.com", failed: "claude@example.com 預熱失敗：" },
  ja: { go: "ウォームアップ", tip: "5 時間枠はまだ始まっていません", done: "idle-account-with-a-long-name@example.com をウォームアップしました", failed: "claude@example.com をウォームアップできませんでした: " },
  de: { go: "Vorwärmen", tip: "Das 5-Stunden-Fenster hat noch nicht begonnen", done: "idle-account-with-a-long-name@example.com vorgewärmt", failed: "claude@example.com konnte nicht vorgewärmt werden: " },
};
const launch = (engine) => engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" });
// the account line's Warm up, by the account
const warmOf = (page, user) => page.locator(".subscription-account", { has: page.locator(`[title="${user}"]`) }).locator("button.acct-warm");
// nothing on an account's line runs past its card, nor the page sideways
const sideways = (page) => page.evaluate(() => {
  const out = [];
  const de = document.documentElement;
  if (de.scrollWidth > de.clientWidth) out.push("page");
  for (const b of document.querySelectorAll(".acct-warm")) {
    const c = b.closest(".subscription-card").getBoundingClientRect(), r = b.getBoundingClientRect();
    if (r.right > c.right + 0.5 || r.left < c.left - 0.5) out.push(b.closest(".subscription-account").dataset.card);
  }
  return out;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(W)) {
    const w = W[lang];
    for (const width of [1100, 390]) {
      test(`${engine} ${lang} ${width}px: Warm up on an account's card whose 5 hours haven't started`, async (t) => {
        const browser = await launch(engine);
        let page;
        t.after(async () => {
          if (process.env.ARTIFACT_DIR && page) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `quota-warm-${engine}-${lang}-${width}.png`), fullPage: true });
          }
          await browser.close();
        });
        page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], asked = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, asked));
        await page.goto("http://magpie.test/?view=usage");

        const idle = warmOf(page, "idle-account-with-a-long-name@example.com");
        await idle.waitFor();
        assert.equal((await idle.innerText()).trim(), w.go);
        assert.ok((await idle.getAttribute("title")).startsWith(w.tip), await idle.getAttribute("title"));
        await warmOf(page, "Ran-Out@Example.com").waitFor();
        await warmOf(page, "claude@example.com").waitFor();
        for (const u of ["busy@example.com", "zero@example.com", "elsewhere@example.com", "ultra@example.com"]) {
          assert.equal(await page.locator(".subscription-account", { has: page.locator(`[title="${u}"]`) }).count(), 1, u + "'s line is there");
          assert.equal(await warmOf(page, u).count(), 0, u + " has no Warm up");
        }
        assert.equal(await page.locator(".acct-warm").count(), 3);
        assert.deepEqual(await sideways(page), []);
        assert.deepEqual(asked, [], "drawing it sent nothing");

        // pressed: that account alone, and it goes once warmed
        await idle.click();
        await page.waitForFunction((s) => document.querySelector("#status")?.textContent.includes(s), w.done);
        assert.deepEqual(asked, [["codex", "idle-account-with-a-long-name@example.com"]]);
        await idle.waitFor({ state: "detached" });
        assert.equal(await page.locator(".acct-warm").count(), 2);

        // refused: the vendor's reason in the status line, and it can be pressed again
        const claude = warmOf(page, "claude@example.com");
        await claude.click();
        await page.waitForFunction((s) => document.querySelector("#status")?.textContent.includes(s), w.failed + WHY);
        assert.deepEqual(asked.at(-1), ["claude", "claude@example.com"]);
        await page.waitForFunction(() => { const b = [...document.querySelectorAll(".acct-warm")].find((x) => x.closest(".subscription-account").textContent.includes("claude@example.com")); return b && !b.disabled; });
        assert.equal(asked.length, 2);
        assert.deepEqual(await sideways(page), []);
        assert.equal(await page.locator("select").count(), 0);
        assert.deepEqual(errors, []);
      });
    }
  }
}
