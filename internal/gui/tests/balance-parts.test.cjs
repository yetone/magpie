// Run with Node's test runner and Playwright on the module path; see README.md.
// A custom provider's balance card (#420): a balance field the user wrote
// with several amounts shows each on a line of its own, its label quiet and
// the first the larger, "Balance" only where the user gave no label; a
// percent is a meter, amber from 90%. Each card says when it was read —
// "Updated 3 minutes ago" — or, standing in for a reading that failed just
// now, "As of … — couldn't be read just now", windows' cards too. The menu
// bar panel's Balances show the same amounts and the "As of". A balance of
// one amount reads as it did. No left-border accent. English and Chinese,
// Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const ago = (min) => new Date(Date.now() - min * 6e4).toISOString();
const quotas = () => [
  { provider: "relay", name: "My Relay", icon: "generic", windows: [], balance: "$2.00 · Today $2.50 · Used 95%", readAt: ago(3),
    balanceParts: [{ text: "$2.00" }, { label: "Today", text: "$2.50" }, { label: "Used", text: "95%", percent: 95 }] },
  { provider: "other", name: "Other Relay", icon: "generic", windows: [], balance: "$4.20", readAt: ago(120), asOf: ago(120) },
  { provider: "plain", name: "Plain", icon: "generic", windows: [], balance: "¥9.00", readAt: ago(0) },
  { provider: "kimi", name: "Kimi", icon: "kimi-color", asOf: ago(45), windows: [{ name: "Weekly", used: 30 }] },
];

function serve(lang, panel) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${!panel}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(quotas());
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { balance: "Balance", updated: "Updated 3 minutes ago", stale: /^As of .+ — couldn't be read just now$/, asOf: /^As of / },
  zh: { balance: "余额", updated: "3分钟前更新", stale: /^截至 .+，暂时无法获取最新用量$/, asOf: /^截至 / },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a custom balance's amounts apart, a percent a meter, when it was read`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const pages = [];
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          for (const [name, p] of pages) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-balance-parts-${name}.png`) });
        }
        await browser.close();
      });
      const errors = [];
      const open = async (name, url, viewport) => {
        const page = await (await browser.newContext({ viewport, reducedMotion: "reduce" })).newPage();
        pages.push([name, page]);
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, url.includes("mode=panel")));
        await page.goto(url);
        return page;
      };
      const width = (loc) => loc.evaluate((e) => Math.round(parseFloat(e.style.width)));

      // the Usage page: a line an amount, the user's labels, Balance only
      // where there is none
      const page = await open("usage", "http://magpie.test/?view=usage", { width: 900, height: 700 });
      const relay = page.locator(".subscription-card", { hasText: "My Relay" });
      await relay.locator(".bal-part").first().waitFor();
      assert.deepEqual(await relay.locator(".bal-part > span").allTextContents(), [w.balance, "Today", "Used"]);
      assert.deepEqual(await relay.locator(".bal-part > b").allTextContents(), ["$2.00", "$2.50", "95%"]);
      assert.equal(await relay.locator(".bal-part.lead").count(), 1, "the first is the one that counts");
      const [lead, rest] = await relay.locator(".bal-part > b").evaluateAll((es) => es.slice(0, 2).map((e) => parseFloat(getComputedStyle(e).fontSize)));
      assert(lead > rest, `the first larger: ${lead} ${rest}`);
      // the percent a meter, amber from 90%; the amounts without one
      assert.equal(await relay.locator(".bal-part .quota-track").count(), 1);
      assert.equal(await width(relay.locator(".bal-part .quota-track.full i")), 95);
      const amber = await relay.locator(".bal-part .quota-track i").evaluate((e) => getComputedStyle(e).backgroundColor);
      const accent = await page.evaluate(() => { const i = document.createElement("i"); i.style.color = "var(--amber)"; document.body.append(i); const c = getComputedStyle(i).color; i.remove(); return c; });
      assert.equal(amber, accent, "the meter at 95% is amber");
      // when it was read, and kept current
      assert.equal(await relay.locator(".quota-read").textContent(), w.updated);
      assert.equal(await relay.locator(".quota-read.stale").count(), 0);
      // one amount reads as it did, and says when it was read
      const plain = page.locator(".subscription-card", { hasText: "Plain" });
      assert.deepEqual(await plain.locator(".quota-balance > span, .quota-balance > b").allTextContents(), [w.balance, "¥9.00"]);
      assert.equal(await plain.locator(".bal-part, .quota-track").count(), 0);
      assert.equal(await plain.locator(".quota-read").count(), 1);
      // standing in for one that couldn't be read just now: as of when
      const other = page.locator(".subscription-card", { hasText: "Other Relay" });
      assert.match(await other.locator(".quota-read.stale").textContent(), w.stale);
      const kimi = page.locator(".subscription-card", { hasText: "Kimi" });
      assert.match(await kimi.locator(".quota-read.stale").textContent(), w.stale, "a windows card stale too");
      const border = await page.evaluate(() => [...document.querySelectorAll(".subscription-card, .subscription-card *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no left-border accent");
      const over = await page.evaluate(() => [...document.querySelectorAll(".subscription-card")].filter((c) => c.scrollWidth > c.clientWidth + 1).length);
      assert.equal(over, 0, "nothing runs out of a card");

      // the menu bar panel's Balances
      const panel = await open("panel", "http://magpie.test/?mode=panel", { width: 440, height: 640 });
      await panel.locator('#ptabs [data-ptab="usage"]').click();
      const card = panel.locator(".pq-card.bal", { hasText: "My Relay" });
      await card.locator(".pq-amt").waitFor();
      assert.equal(await card.locator(".pq-amt").textContent(), "$2.00");
      assert.deepEqual(await card.locator(".pq-bp > span").allTextContents(), ["Today", "Used"]);
      assert.deepEqual(await card.locator(".pq-bp > b").allTextContents(), ["$2.50", "95%"]);
      assert.equal(await width(card.locator(".quota-track.full i")), 95);
      const stale = panel.locator(".pq-card.bal", { hasText: "Other Relay" });
      assert(await stale.evaluate((e) => e.classList.contains("stale")));
      assert.match(await stale.locator(".pq-asof").textContent(), w.asOf);
      assert.match((await stale.getAttribute("title")).split("\n")[1], w.stale);
      assert.equal(await panel.locator(".pq-card.bal", { hasText: "Plain" }).locator(".pq-amt").textContent(), "¥9.00");
      const pover = await panel.evaluate(() => [...document.querySelectorAll(".pq-card.bal")].filter((c) => c.scrollWidth > c.clientWidth + 1).length);
      assert.equal(pover, 0, "nothing runs out of a balance's card");

      const missing = await page.evaluate(() => ["As of {when}", "Updated {when}", "As of {when} — couldn't be read just now"].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
