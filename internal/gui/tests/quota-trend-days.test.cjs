// Run with Node's test runner and Playwright on the module path; see README.md.
// The allowances' trend range takes any number of days back, not only the
// presets (shangyuan0818 on X): "Other number of days…" in the app's menu
// opens a small field in the head, 1 to 45 (what magpie keeps), Enter saves
// it, the head and every card name it, and it is remembered across a
// reload; a number out of range says so and keeps the range. Hovering a
// curve shows the exact reading nearest the pointer, every line's, with its
// time: the quota's left to two decimals, a balance as its card writes it.
// No click moves the page. Every language, both engines; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = process.env.MAGPIE_QUOTA_ASSETS || path.resolve(__dirname, "../assets");
const H = 3600e3;
const iso = (ms) => new Date(ms).toISOString();
// the pointer on (x, y) until the tip shows: a card drawn again under a
// still pointer (the history read in) has no hover until it moves
async function hoverTip(page, tip, x, y) {
  await page.mouse.move(x, y);
  for (let i = 0; i < 40 && !(await tip.isVisible()); i++) await page.mouse.move(x + ((i + 1) % 2), y);
  await tip.waitFor();
}

function fixtures(now) {
  const rw = now + 3 * 24 * H;
  const quotas = [
    { provider: "codex", name: "Codex", icon: "openai", plan: "Plus", user: "a@x.com", windows: [{ name: "Weekly", used: 30, resetsAt: iso(rw) }] },
    { provider: "deepseek", name: "DeepSeek", icon: "deepseek", windows: [], balance: "¥30.50", readAt: iso(now),
      balanceTrend: { points: [[48, 50], [24, 40.125], [0, 30.5]].map(([h, v]) => ({ at: iso(now - h * H), amount: v })) } },
  ];
  const week = [];
  for (let h = 20 * 24; h >= 0; h -= 6) week.push({ at: iso(now - h * H), left: +(70.456 + h / 9).toFixed(3), start: iso(rw - 168 * H), resetsAt: iso(rw) });
  return { quotas, history: [{ provider: "codex", user: "a@x.com", lines: [{ name: "Weekly", points: week }] }] };
}

function serve(lang, data) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (body) => route.fulfill({ json: body });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(data.quotas);
    if (url.pathname === "/api/usage/quotas/history/holds") return json([]);
    if (url.pathname === "/api/usage/quotas/history") return json(data.history);
    if (url.pathname === "/api/usage") return json({ calls: 0, input: 0, output: 0, cost: 0, agents: [], accounts: [], models: [], series: [], days: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { trend: "Trends: ", other: "Other number of days…", days: (n) => n + " days", bad: "A number of days from 1 to 45", week: "Weekly", balance: "Balance" },
  zh: { trend: "走势：", other: "其他天数…", days: (n) => n + " 天", bad: "天数为 1 到 45 之间的整数", week: "每周", balance: "余额" },
  "zh-TW": { trend: "走勢：", other: "其他天數…", days: (n) => n + " 天", bad: "天數為 1 到 45 之間的整數", week: "每週", balance: "餘額" },
  ja: { trend: "推移：", other: "日数を指定…", days: (n) => n + " 日", bad: "日数は 1〜45 の整数です", week: "週", balance: "残高" },
  de: { trend: "Verlauf: ", other: "Andere Anzahl Tage…", days: (n) => n + " Tage", bad: "Eine Anzahl Tage von 1 bis 45", week: "Wöchentlich", balance: "Guthaben" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, w] of Object.entries(words)) {
    test(`${engine} ${lang}: the quota trend takes any number of days, and a hover reads the exact numbers`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const context = await browser.newContext({ viewport: { width: 440, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const data = fixtures(Date.now());
      await page.route("**/*", serve(lang, data));
      await page.goto("http://magpie.test/?view=usage");
      const curve = page.locator('.subscription-card[data-key="codex"] .quota-curve').first();
      await curve.waitFor();
      const pick = page.locator("#quotaHead #quotaTrend");
      const scroll = () => page.locator("#view-usage").evaluate((v) => v.scrollTop);
      const before = await scroll();

      // the presets, then any number
      await pick.click();
      const items = page.locator(".sess-menu .pm-item");
      const names = await items.allTextContents();
      assert.ok(names.some((n) => n.startsWith(w.days(7))) && names.some((n) => n.startsWith(w.days(30))), "7 and 30 days are offered: " + names);
      await items.filter({ hasText: w.other }).click();
      const field = page.locator("#quotaHead input.qt-days");
      await field.waitFor();
      assert.ok(await pick.isHidden(), "the field stands where the button was");
      await field.fill("12");
      await field.press("Enter");
      await page.waitForFunction((s) => document.querySelector("#quotaTrend")?.textContent === s, w.trend + w.days(12));
      assert.equal(await field.count(), 0);
      assert.equal(await page.evaluate(() => localStorage.getItem("magpie.quotaRange")), "12d");
      assert.equal(await curve.locator(".qc-range").textContent(), w.days(12), "the card names the range too");
      const axis12 = await curve.locator(".qc-axis span").first().textContent();

      // the custom number is listed and checked; a bad number keeps it
      await pick.click();
      const custom = items.filter({ hasText: w.days(12) });
      await items.first().waitFor();
      assert.equal(await custom.count(), 1, "the custom range is in the menu: " + await items.allTextContents());
      await items.filter({ hasText: w.other }).click();
      await field.waitFor();
      assert.equal(await field.inputValue(), "12", "the field starts at the current number");
      await field.fill("99");
      await field.press("Enter");
      await page.waitForFunction((s) => document.querySelector("#status")?.textContent === s, w.bad);
      assert.equal(await page.evaluate(() => localStorage.getItem("magpie.quotaRange")), "12d", "a number out of range changes nothing");
      // Escape leaves it as it was
      await pick.click();
      await items.filter({ hasText: w.other }).click();
      await field.fill("3");
      await field.press("Escape");
      assert.equal(await page.evaluate(() => localStorage.getItem("magpie.quotaRange")), "12d");
      assert.equal(await pick.textContent(), w.trend + w.days(12));

      // a preset's span differs from 12 days'
      await pick.click();
      await items.filter({ hasText: w.days(7) }).first().click();
      await page.waitForFunction((s) => document.querySelector("#quotaTrend")?.textContent === s, w.trend + w.days(7));
      assert.notEqual(await curve.locator(".qc-axis span").first().textContent(), axis12, "12 days reaches further back than 7");
      // and back to 12, kept across a reload
      await pick.click();
      await items.filter({ hasText: w.other }).click();
      await field.fill("12");
      await field.press("Enter");
      await page.waitForFunction((s) => document.querySelector("#quotaTrend")?.textContent === s, w.trend + w.days(12));
      assert.equal(await scroll(), before, "no click moved the page");
      await page.reload();
      await curve.waitFor();
      assert.equal(await pick.textContent(), w.trend + w.days(12), "remembered across a reload");

      // the hover: the reading nearest the pointer, exactly
      const plot = curve.locator(".qc-plot");
      const box = await plot.boundingBox();
      const tip = curve.locator(".qc-tip");
      await hoverTip(page, tip, box.x + box.width - 2, box.y + box.height / 2);
      const rows = await tip.locator("div").allTextContents();
      assert.deepEqual(rows, [w.week + "70.46%"], "the exact left at now, two decimals");
      assert.ok((await tip.locator("b").textContent()).trim(), "with its time");
      assert.equal(await curve.getAttribute("title"), null, "the curve's own tooltip waits while the tip shows");
      await hoverTip(page, tip, box.x + box.width / 2, box.y + box.height / 2);
      const mid = await tip.locator("em").textContent();
      assert.match(mid, /^\d+(\.\d{1,2})?%$/);
      assert.notEqual(mid, "70.46%", "the reading follows the pointer");
      await page.mouse.move(0, 0);
      await tip.waitFor({ state: "hidden" });
      assert.ok(await curve.getAttribute("title"), "and comes back after");

      const bal = page.locator(".subscription-card", { hasText: "DeepSeek" }).locator(".balance-curve");
      // wheeled into view, as a person would: the page puts back a scroll no one asked for
      const vb = await page.locator("#view-usage").boundingBox();
      await page.mouse.move(vb.x + vb.width / 2, vb.y + 40);
      for (let i = 0; i < 20 && (await bal.locator(".qc-plot").boundingBox()).y + 60 > vb.y + vb.height; i++) await page.mouse.wheel(0, 120);
      // Chromium's wheel glides on: read the plot once it has stopped
      let bbox = null;
      for (let i = 0; i < 50; i++) {
        const b = await bal.locator(".qc-plot").boundingBox();
        if (bbox && b.y === bbox.y) break;
        bbox = b;
        await page.waitForTimeout(100);
      }
      const btip = bal.locator(".qc-tip");
      await hoverTip(page, btip, bbox.x + 2, bbox.y + bbox.height / 2);
      assert.equal(await btip.locator("span").textContent(), w.balance);
      assert.match(await btip.locator("em").textContent(), /50(\.00)?/, "the oldest balance at the left end");
      assert.deepEqual(errors, []);
    });
  }
}
