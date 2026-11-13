// Run with Node's test runner and Playwright on the module path; see README.md.
// Currency "By provider" (onkashiko on X): each provider's costs show in
// what it sells in — DeepSeek's in yuan, OpenAI's in dollars — and every
// sum across providers in the currency the "Totals in" row picks, a row
// shown only while By provider is chosen. The menu bar's own currency
// (its second ask) is a row folded under Advanced. Every language, at a narrow
// window; no backend, the API is faked here as the Go side answers it
// (fx.yuan only in By provider, costFX).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { openPart } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

const usage = {
  calls: 5, errors: 0, input: 12000, output: 4000, cache_read: 0, cache_write: 0, reasoning: 0,
  unpriced: 0, cost: 3, bucket: "day",
  series: [{ label: "Mon", input: 12000, output: 4000, calls: 5, cost: 3 }],
  agents: [{ name: "Claude Code", calls: 5, cost: 3, input: 12000, output: 4000 }],
  models: [
    { id: "deepseek/deepseek-chat", name: "deepseek-chat", provider: "deepseek", calls: 3, cost: 1, input: 6000, output: 2000 },
    { id: "openai/gpt-5", name: "gpt-5", provider: "openai", calls: 2, cost: 2, input: 6000, output: 2000 },
  ],
};

const fxOf = (s) => ({ rate: 7.2, at: new Date().toISOString(), stale: false, ...(s.currency === "provider" ? { yuan: ["deepseek"] } : {}) });

function settingsPayload(over) {
  const s = {
    theme: "light", lang: "en", tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    ...over,
  };
  return { ...s, fx: fxOf(s) };
}

function server(lang, saved) {
  let cur = settingsPayload({ lang });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", currency: cur.currency, totalCurrency: cur.totalCurrency }, fx: cur.fx });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        saved.push(body);
        cur = settingsPayload({ ...cur, ...body });
      }
      return json(cur);
    }
    if (url.pathname === "/api/usage") return json(usage);
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const LANGS = [
  ["en", "By provider", "Totals in", "Advanced", "Menu bar currency", "Same as totals"],
  ["zh", "按供应商", "合计币种", "高级", "菜单栏币种", "同合计"],
  ["zh-TW", "按供應商", "合計幣種", "進階", "選單列幣種", "同合計"],
  ["ja", "プロバイダー別", "合計の通貨", "詳細設定", "メニューバーの通貨", "合計と同じ"],
  ["de", "Je Anbieter", "Summen in", "Erweitert", "Währung in der Menüleiste", "Wie Summen"],
];

const modelCost = (page, name) => page.locator("#usageModels .row", { hasText: name }).locator(".cost").textContent().then((s) => s.trim());
const totalCost = (page) => page.locator("#usageCost b").textContent().then((s) => s.trim());

async function toUsage(page) {
  await page.locator('[data-view="usage"]').click();
  await page.locator("#usageModels .row").first().waitFor();
  await page.waitForTimeout(200);
}

async function toSettings(page) {
  await page.locator("#prefs").click();
  await openPart(page, "usage");
  await page.locator("#currencySegs .opt").first().waitFor();
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": By provider shows each provider in its own currency, totals in the chosen one", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const pages = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-currency-provider-${i}.png`) }).catch(() => {});
      }
      await browser.close();
    });

    for (const [lang, byProvider, totalsIn, advanced, menuBar, sameAs] of LANGS) {
      for (const width of [900, 420]) {
        await t.test(`${lang} at ${width}px`, async () => {
          const errors = [], saved = [];
          const context = await browser.newContext({ viewport: { width, height: 640 }, reducedMotion: "reduce" });
          const page = await context.newPage();
          pages.push(page);
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang, saved));

          // dollars everywhere by default
          await page.goto("http://magpie.test/?view=usage");
          await page.locator("#usageModels .row").first().waitFor();
          assert.equal(await totalCost(page), "≈$3.00");
          assert.equal(await modelCost(page, "deepseek-chat"), "≈$1.00");
          assert.equal(await modelCost(page, "gpt-5"), "≈$2.00");

          // Settings: the third choice, and no "Totals in" row until it is picked
          await toSettings(page);
          const segs = page.locator("#currencySegs .opt");
          assert.equal(await segs.count(), 3);
          assert.equal((await segs.nth(2).textContent()).trim(), byProvider);
          assert.equal(await page.locator("#totalCurrencyRow").isVisible(), false, "Totals in shows only for By provider");
          await segs.nth(2).click();
          await page.locator("#currencySegs .opt.on", { hasText: byProvider }).waitFor();
          await page.locator("#totalCurrencyRow").waitFor({ state: "visible" });
          assert.equal((await page.locator("#totalCurrencyRow .name").textContent()).trim(), totalsIn);
          assert.equal(saved.at(-1).currency, "provider");
          const totals = page.locator("#totalCurrencySegs .opt");
          assert.equal(await totals.count(), 2, "totals are $ or ¥, never By provider");
          assert.equal(await totals.nth(0).evaluate((b) => b.classList.contains("on")), true, "totals start in $");
          // nothing of either row spills out of a narrow window
          for (const sel of ["#currencySegs", "#totalCurrencySegs"]) {
            const box = await page.locator(sel).boundingBox();
            assert(box.x >= 0 && box.x + box.width <= width + 0.5, `${sel} spills out at ${width}px: ${JSON.stringify(box)}`);
          }

          // Usage: DeepSeek in yuan (1 × 7.2), OpenAI and the total in dollars
          await toUsage(page);
          assert.equal(await modelCost(page, "deepseek-chat"), "≈¥7.20");
          assert.equal(await modelCost(page, "gpt-5"), "≈$2.00");
          assert.equal(await totalCost(page), "≈$3.00");

          // totals in yuan: the sum goes ¥, each provider keeps its own
          await toSettings(page);
          await page.locator("#totalCurrencySegs .opt").nth(1).click();
          await page.locator("#totalCurrencySegs .opt.on").nth(0).waitFor();
          assert.equal(await page.locator("#totalCurrencySegs .opt").nth(1).evaluate((b) => b.classList.contains("on")), true);
          assert.equal(saved.at(-1).totalCurrency, "cny");
          assert.equal(saved.at(-1).currency, "provider", "picking the totals' currency keeps By provider");
          await toUsage(page);
          assert.equal(await totalCost(page), "≈¥21.60");
          assert.equal(await modelCost(page, "deepseek-chat"), "≈¥7.20");
          assert.equal(await modelCost(page, "gpt-5"), "≈$2.00");

          // the menu bar's currency, set apart from the totals' (onkashiko's
          // second ask): folded under Advanced, closed while unset
          await toSettings(page);
          const more = page.locator("#usageMore");
          assert.equal((await more.locator("summary").textContent()).trim(), advanced);
          assert.equal(await more.evaluate((d) => d.open), false, "Advanced starts folded");
          assert.equal(await page.locator("#trayCurrencyRow").isVisible(), false);
          await more.locator("summary").click();
          await page.locator("#trayCurrencyRow").waitFor({ state: "visible" });
          assert.equal((await page.locator("#trayCurrencyRow .name").textContent()).trim(), menuBar);
          const bar = page.locator("#trayCurrencySegs .opt");
          assert.equal(await bar.count(), 3, "same as totals, $ and ¥");
          assert.equal((await bar.nth(0).textContent()).trim(), sameAs);
          assert.equal(await bar.nth(0).evaluate((b) => b.classList.contains("on")), true, "follows the totals until picked");
          const box = await page.locator("#trayCurrencySegs").boundingBox();
          assert(box.x >= 0 && box.x + box.width <= width + 0.5, `#trayCurrencySegs spills out at ${width}px: ${JSON.stringify(box)}`);
          await bar.nth(1).click();
          await page.waitForFunction(() => document.querySelectorAll("#trayCurrencySegs .opt")[1]?.classList.contains("on"));
          assert.equal(saved.at(-1).trayCurrency, "usd");
          assert.equal(saved.at(-1).totalCurrency, "cny", "the menu bar's pick leaves the totals'");
          assert.equal(saved.at(-1).currency, "provider");
          await toUsage(page);
          assert.equal(await totalCost(page), "≈¥21.60", "the menu bar's pick leaves the Usage page's totals");

          // after a restart, straight onto Usage: the same
          await page.reload();
          await page.locator("#usageModels .row").first().waitFor();
          await page.waitForTimeout(200);
          assert.equal(await totalCost(page), "≈¥21.60");
          assert.equal(await modelCost(page, "deepseek-chat"), "≈¥7.20");
          assert.equal(await modelCost(page, "gpt-5"), "≈$2.00");

          // a set menu bar currency opens Advanced, so it is seen
          await toSettings(page);
          assert.equal(await page.locator("#usageMore").evaluate((d) => d.open), true, "Advanced open while the menu bar's is set");
          assert.equal(await page.locator("#trayCurrencySegs .opt").nth(1).evaluate((b) => b.classList.contains("on")), true, "$ kept");

          // back to $ USD: the Totals in row goes, everything is in dollars
          await toSettings(page);
          await page.locator("#currencySegs .opt").nth(0).click();
          await page.locator("#totalCurrencyRow").waitFor({ state: "hidden" });
          await toUsage(page);
          assert.equal(await modelCost(page, "deepseek-chat"), "≈$1.00");
          assert.equal(await totalCost(page), "≈$3.00");

          assert.deepEqual(errors, []);
          await context.close();
        });
      }
    }
  });
}
