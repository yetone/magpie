// Run with Node's test runner and Playwright on the module path; see README.md.
// ferock4669 and iyou00 on Discord: with many keys, Routing's Accounts and
// keys ran on and on ("key 这里如果有很多 key 就会拉的很长"). Now each card
// keeps its name, one line of what is left and how it fares, the models it
// answered on hover; a long list shows its first eight rows and the rest
// behind "Show N more" at its foot, kept across a reload, the click moving
// nothing. A row that needs the reader is never behind it: a key failing
// with no answer, and an account to sign in again, drawn under its
// provider's fold while the fold is shut, each under its heading. "View
// usage" beside it opens the Usage page's Overview, where each account's
// card says it all, without keeping that tab as the reader's own. Every
// language, at a window's width and at 420px, in both engines.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const pad = (i) => String(i).padStart(2, "0");
const tallies = {};
// twelve providers of one key each, all answering
for (let i = 1; i <= 12; i++) {
  tallies["p" + pad(i) + "-k"] = { id: "p" + pad(i) + "-k", provider: "p" + pad(i), name: "Alpha " + pad(i), who: "key-" + pad(i), kind: "key",
    agent: "claude", model: "m-" + pad(i), tried: 3, ok: 3, fails: {}, last: now - 60000, models: ["m-" + pad(i)] };
}
// a key failing, never answering: last in the list, and shown all the same
tallies["yak-k"] = { id: "yak-k", provider: "yak", name: "Yak", who: "key-yak", kind: "key", agent: "claude", model: "m-yak", tried: 4, ok: 0, fails: { other: 4 }, last: 0 };
// six accounts of one provider, folded; one refused for its sign-in since it last answered
const users = ["a", "b", "c", "d", "e", "late"];
for (const u of users) {
  tallies[JSON.stringify(["zed", u + "@example.com"])] = { id: "zed@" + u, provider: "zed", name: "Zed", who: u + "@example.com", kind: "account", agent: "zed",
    model: "m-zed", tried: 2, ok: 2, fails: u === "late" ? { auth: 2 } : {}, last: now - 600000, authAt: u === "late" ? now - 300000 : 0 };
}
// one request the gateway still keeps, answered by the first key: with
// none, there is no list of accounts to tally
const iso = (ms) => new Date(ms).toISOString();
const { tried, ok, fails, last, models, ...seat } = tallies["p01-k"];
const routes = [{ id: 1, seq: 1, time: iso(now - 61000), agent: "claude", model: "m-01", provider: "p01", done: true, status: 200, ms: 300,
  order: [seat], tries: [{ id: "p01-k", model: "m-01", start: iso(now - 61000), done: true, ms: 250, status: 200 }] }];
const words = {
  en: { more: "Show 4 more", fewer: "Show fewer", usage: "View usage" },
  zh: { more: "显示其余 4 个", fewer: "收起", usage: "查看用量" },
  "zh-TW": { more: "顯示其餘 4 個", fewer: "收起", usage: "檢視用量" },
  ja: { more: "さらに 4 件表示", fewer: "折りたたむ", usage: "使用量を表示" },
  de: { more: "4 weitere anzeigen", fewer: "Weniger anzeigen", usage: "Nutzung anzeigen" },
};

function serve(lang) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light", quotaLeft: false } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 50, rerouted: 0, errors: 6 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [{ day: new Date(now).toISOString().slice(0, 10), requests: 50 }], routes: [], tallies });
    if (url.pathname === "/api/usage/quotas" || url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const alpha = (from, to) => Array.from({ length: to - from + 1 }, (_, i) => "key-" + pad(from + i));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a long Accounts and keys shows its first rows and every one needing the reader`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const width of [1300, 420]) for (const lang of Object.keys(words)) {
      await t.test(`${lang} at ${width}px`, async () => {
        const ctx = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
        try {
          const page = await ctx.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang));
          await page.addInitScript(() => { try { if (!sessionStorage.getItem("seeded")) { localStorage.setItem("magpie.usageTab", "requests"); sessionStorage.setItem("seeded", "1"); } } catch {} });
          await page.goto("http://magpie.test/?view=routing");
          const w = words[lang];
          const acts = page.locator(".rt-acts");
          const more = page.locator(".rt-acts-foot button.rt-acts-more");
          const names = () => acts.locator(".rt-act .nm b").allTextContents();
          const heads = () => acts.locator(".rt-prov b").allTextContents();
          await more.filter({ hasText: w.more }).waitFor();
          // the first eight, the failing key and the account to sign in
          // again, each under its heading; Zed's other five folded
          const folded = (await names()).filter((x) => !x.startsWith("key-") && !x.includes("@"));
          assert.equal(folded.length, 1, "Zed's fold row: " + folded);
          assert.deepEqual((await names()).filter((x) => x !== folded[0]), [...alpha(1, 8), "key-yak", "late@example.com"]);
          assert.deepEqual(await heads(), ["Alpha 01", "Alpha 02", "Alpha 03", "Alpha 04", "Alpha 05", "Alpha 06", "Alpha 07", "Alpha 08", "Yak", "Zed"]);
          const late = acts.locator(".rt-act", { hasText: "late@example.com" });
          assert.equal(await late.locator("button.rt-signin").count(), 1);
          assert.equal(await acts.locator(".rt-act", { hasText: "key-yak" }).locator(".tally .bad").count(), 1);
          // a card: its name, its line, how it fares; the models on hover
          const one = acts.locator(".rt-act", { hasText: "key-01" });
          assert.equal(await one.getAttribute("title"), "p01-k\nm-01");
          assert.equal(await acts.locator(".mdls, .wins, code").count(), 0);
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth <= 1), true, "nothing past the window");
          // Show 4 more: all of them, and the button clicked stays where it
          // is on the screen (the rows come in above it)
          // wheeled to, as a reader would (the page holds still for a script's scroll)
          await page.mouse.move(width / 2, 300);
          for (let i = 0; i < 40 && (await more.boundingBox()).y > 600; i++) {
            await page.mouse.wheel(0, 120);
            await page.waitForTimeout(100);
          }
          await page.waitForTimeout(300);
          const box = await more.boundingBox();
          assert.ok(box.y > 0 && box.y + box.height < 760, "on the screen: " + JSON.stringify(box));
          assert.ok(box.x >= 0 && box.x + box.width <= width, JSON.stringify(box));
          await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2);
          await more.filter({ hasText: w.fewer }).waitFor();
          const after = (await more.boundingBox()).y; // scrollTop is whole pixels
          assert.ok(Math.abs(after - box.y) <= 1, `the click moved the button: ${box.y} → ${after}`);
          assert.deepEqual((await names()).filter((x) => x !== folded[0]), [...alpha(1, 12), "key-yak", "late@example.com"]);
          assert.equal(await more.getAttribute("aria-expanded"), "true");
          // kept across a reload, then shown fewer again
          await page.reload();
          await more.filter({ hasText: w.fewer }).waitFor();
          assert.equal((await names()).filter((x) => x.startsWith("key-")).length, 13);
          await more.click();
          await more.filter({ hasText: w.more }).waitFor();
          assert.equal((await names()).filter((x) => x.startsWith("key-")).length, 9);
          // View usage: the Overview, each account's card in full; the
          // reader's own tab (Requests) stays theirs
          const usage = page.locator(".rt-acts-foot button.rt-acts-usage");
          assert.equal(await usage.textContent(), w.usage);
          await usage.click();
          await page.locator("#view-usage").waitFor({ state: "visible" });
          await page.locator("#usagePane").waitFor({ state: "visible" });
          assert.equal(await page.locator("#ledgerPane").isVisible(), false);
          assert.equal(await page.evaluate(() => localStorage.getItem("magpie.usageTab")), "requests");
          assert.deepEqual(errors, []);
        } finally {
          await ctx.close();
        }
      });
    }
  });
}
