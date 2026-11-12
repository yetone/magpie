// Run with Node's test runner and Playwright on the module path; see README.md.
// A card's quota rows on Usage → Overview can be hidden one group at a time
// (wangshaowei39 on X: Antigravity's Claude & GPT rows unwanted). A
// right-click on a row (a long press on a touch screen) offers Hide {group}
// from Overview; the card's menu button offers each group. Kept by provider
// and group (POST /api/usage/arrange, windowsHidden), still hidden when the
// page is opened again, listed under the cards with the hidden cards, each
// with Unhide. A card is never left with no row. Every language, a wide
// window, a narrow one and magpie web on a phone; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { isDeepStrictEqual } = require("node:util");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const win = (name, used) => ({ name, used, resetSecs: 3 * 3600 });
const pw = (pool, name, used) => ({ name, pool, used, resetSecs: 3 * 3600 });
const QUOTAS = [
  { provider: "antigravity", name: "Antigravity", icon: "antigravity-color", plan: "Pro", windows: [pw("Gemini", "5 hours", 20), pw("Gemini", "7 days", 30), pw("Claude & GPT", "5 hours", 40), pw("Claude & GPT", "7 days", 50)] },
  { provider: "claude", name: "Claude", icon: "claude-color", plan: "max", windows: [win("5h", 70)] },
  { provider: "stepfun", name: "StepFun", icon: "stepfun-color", plan: "Step Plan", windows: [win("5h", 30)] },
  { provider: "stepfun", name: "StepFun", icon: "stepfun-color", windows: [], balance: "¥42.00" },
  // a second StepFun provider with a balance alone: a card of its own
  { provider: "stepfun-2", name: "StepFun Work", icon: "stepfun-color", windows: [], balance: "¥7.50" },
];

function server(lang, web, saved, posts) {
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    const settings = () => ({ lang, theme: "light", usageWindowsHidden: saved.hidden });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], clients: [], profiles: [], settings: settings(), fx: { rate: 7.2, at: new Date().toISOString() } });
    if (url.pathname === "/api/usage/arrange") {
      const body = JSON.parse(req.postData() || "{}");
      posts.push(body);
      if (body.windowsHidden) saved.hidden = body.windowsHidden;
      return json(settings());
    }
    if (url.pathname === "/api/settings") return json(settings());
    if (url.pathname === "/api/usage/quotas") return json(QUOTAS);
    if (url.pathname === "/api/usage") return json({ calls: 0, errors: 0, input: 0, output: 0, reasoning: 0, cache_read: 0, cache_write: 0, unpriced: 0, cost: 0, bucket: "day", series: [], path: "~/.config/magpie/usage.jsonl", agents: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: false } });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

const L = {
  en: { hide: (g) => `Hide ${g} from Overview`, card: "Hide from Overview", n: (n) => `${n} hidden`, unhide: "Unhide", more: (n) => `Show ${n} Hidden` },
  zh: { hide: (g) => `从概览中隐藏 ${g}`, card: "从概览中隐藏", n: (n) => `已隐藏 ${n} 个`, unhide: "取消隐藏", more: (n) => `显示已隐藏的 ${n} 个` },
  "zh-TW": { hide: (g) => `從概覽中隱藏 ${g}`, card: "從概覽中隱藏", n: (n) => `已隱藏 ${n} 個`, unhide: "取消隱藏", more: (n) => `顯示已隱藏的 ${n} 個` },
  ja: { hide: (g) => `${g} を概要から隠す`, card: "概要から隠す", n: (n) => `${n} 件非表示`, unhide: "再表示", more: (n) => `非表示の ${n} 件を表示` },
  de: { hide: (g) => `${g} in der Übersicht ausblenden`, card: "In der Übersicht ausblenden", n: (n) => `${n} ausgeblendet`, unhide: "Einblenden", more: (n) => `${n} ausgeblendete anzeigen` },
};
const SIZES = [{ width: 1200, web: false }, { width: 560, web: false }, { width: 390, web: true }];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a card's quota rows are hidden from the Overview, and brought back", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) {
      for (const { width, web } of SIZES) {
        const w = L[lang];
        await t.test(`${lang} ${width}${web ? " web" : ""}`, async () => {
          const errors = [];
          const saved = { hidden: [] }, posts = [];
          const ctx = await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce", hasTouch: web, isMobile: web && engine === "chromium" });
          const open = async () => {
            const p = await ctx.newPage();
            p.setDefaultTimeout(5000);
            p.setDefaultNavigationTimeout(60000);
            p.on("pageerror", (e) => errors.push(e.message));
            await p.route("**/*", server(lang, web, saved, posts));
            await p.goto("http://magpie.test/?view=usage");
            await p.locator("#subscriptionUsage .subscription-card").first().waitFor({ timeout: 20000 });
            return p;
          };
          const groups = (p) => p.locator('#subscriptionUsage > [data-provider="antigravity"] .quota-name').evaluateAll((qs) => qs.map((q) => q.textContent.split(" · ")[0]));
          const items = (p) => p.locator(".row-menu .rm-item").allTextContents();
          const savedAs = async (want) => {
            for (const end = Date.now() + 5000; !isDeepStrictEqual(saved.hidden, want) && Date.now() < end;) await new Promise((ok) => setTimeout(ok, 20));
            assert.deepEqual(saved.hidden, want);
          };
          const longPress = (loc) => loc.evaluate((c) => new Promise((done) => {
            const box = c.getBoundingClientRect();
            const at = { pointerType: "touch", isPrimary: true, pointerId: 7, bubbles: true, clientX: box.left + box.width / 2, clientY: box.top + box.height / 2 };
            c.dispatchEvent(new PointerEvent("pointerdown", at));
            setTimeout(() => { c.dispatchEvent(new PointerEvent("pointerup", at)); done(); }, 700);
          }));
          const menuOn = async (p, loc) => {
            await reader.inView(p, loc);
            if (web) await longPress(loc); else await loc.click({ button: "right" });
            await p.locator(".row-menu").waitFor();
          };

          let p = await open();
          assert.deepEqual(await groups(p), ["Gemini", "Gemini", "Claude & GPT", "Claude & GPT"]);
          assert.equal(await p.locator("#quotaHid").isHidden(), true);
          // a card with one row offers no hiding of it
          await menuOn(p, p.locator('#subscriptionUsage > [data-provider="claude"] .quota'));
          assert.deepEqual(await items(p), [w.card]);
          await p.keyboard.press("Escape");

          // a row's own menu: its group alone
          await menuOn(p, p.locator('#subscriptionUsage > [data-provider="antigravity"] .quota', { hasText: "Claude & GPT" }).first());
          assert.deepEqual(await items(p), [w.card, w.hide("Claude & GPT")]);
          await p.locator(".row-menu .rm-item", { hasText: w.hide("Claude & GPT") }).click();
          await p.waitForFunction(() => document.querySelectorAll('[data-provider="antigravity"] .quota').length === 2);
          assert.deepEqual(await groups(p), ["Gemini", "Gemini"]);
          await savedAs(["antigravity|Claude & GPT"]);
          assert.deepEqual(posts.at(-1), { windowsHidden: ["antigravity|Claude & GPT"] });
          const line = p.locator("#quotaHid .us-hidline");
          assert.equal((await line.textContent()).trim(), w.n(1));

          // the last group can't be hidden
          await menuOn(p, p.locator('#subscriptionUsage > [data-provider="antigravity"] .quota').first());
          assert.deepEqual(await items(p), [w.card, w.more(1)]);
          await p.keyboard.press("Escape");

          // still hidden when the page is opened again
          await p.close();
          p = await open();
          assert.deepEqual(await groups(p), ["Gemini", "Gemini"]);
          await reader.click(p, p.locator("#quotaHid .us-hidline"));
          assert.equal((await p.locator("#quotaHid .row.stat .sub").textContent()).trim(), "Claude & GPT");
          const back = p.locator("#quotaHid .us-unhide");
          assert.equal((await back.textContent()).trim(), w.unhide);
          await reader.inView(p, back);
          await back.click();
          await p.waitForFunction(() => document.querySelectorAll('[data-provider="antigravity"] .quota').length === 4);
          await savedAs([]);
          assert.equal(await p.locator("#quotaHid").isHidden(), true);

          // the card's own button lists each group
          const more = p.locator('#subscriptionUsage > [data-provider="antigravity"] .us-more');
          await reader.inView(p, more);
          await more.click({ force: true });
          await p.locator(".row-menu").waitFor();
          assert.deepEqual(await items(p), [w.card, w.hide("Gemini"), w.hide("Claude & GPT")]);
          assert.deepEqual(errors, []);
          await ctx.close();
        });
      }
    }
  });
}
