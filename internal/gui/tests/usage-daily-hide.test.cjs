// Run with Node's test runner and Playwright on the module path; see README.md.
// A quota row hidden from Usage → Overview takes its credits a day with it
// (Will on Discord: Kiro's Credits (us-east-1) and Overage (us-east-1)
// hidden, and their days still drawn under the card). Hidden by the same
// key, provider and row group, and with every row hidden the card shows
// them all again, its days too. Every language, a wide window and magpie
// web on a phone; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const dayKey = (back) => {
  const d = new Date();
  d.setHours(12, 0, 0, 0);
  d.setDate(d.getDate() - back);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
};
const days = (window, used) => ({ since: dayKey(3), window, days: [{ day: dayKey(1), used }, { day: dayKey(0), used: used / 2 }] });
const month = (name, amount, limit, aside) => ({ name, used: (100 * amount) / limit, display: `${amount} / ${limit}`, amount, limit, unit: "credits", span: 30 * 24 * 3600, ...(aside ? { aside: true } : {}) });
// as the Kiro plugin draws an Identity Center account with overage on,
// one region enabled
const KIRO = {
  provider: "kiro", name: "Kiro", icon: "kiro-color", plan: "Power", user: "will@example.com",
  windows: [
    month("Credits", 10000, 10000, true),
    month("Overage", 2589.24, 37500),
    month("Credits (us-east-1)", 10000, 10000, true),
    month("Overage (us-east-1)", 2589.24, 37500, true),
    { name: "Credits (eu-central-1)", display: "not enabled", aside: true },
  ],
  daily: days("Credits", 400),
  moreDaily: [days("Overage", 90), days("Credits (us-east-1)", 400), days("Overage (us-east-1)", 90)],
};
const REGIONS = ["kiro|Credits (us-east-1)", "kiro|Overage (us-east-1)", "kiro|Credits (eu-central-1)"];
const EVERY = ["kiro|Credits", "kiro|Overage", ...REGIONS];

function server(lang, web, hidden) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    const settings = { lang, theme: "light", usageWindowsHidden: hidden };
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], clients: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/usage/quotas") return json([KIRO]);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: false } });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const SIZES = [{ width: 1200, web: false }, { width: 390, web: true }];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a hidden quota row's credits a day are hidden with it", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of LANGS) {
      for (const { width, web } of SIZES) {
        await t.test(`${lang} ${width}${web ? " web" : ""}`, async () => {
          const errors = [];
          const ctx = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce", hasTouch: web, isMobile: web && engine === "chromium" });
          // the rows and the pools' heads the card draws, with hidden as saved
          const seen = async (hidden) => {
            const p = await ctx.newPage();
            p.setDefaultTimeout(5000);
            p.setDefaultNavigationTimeout(60000);
            p.on("pageerror", (e) => errors.push(e.message));
            await p.route("**/*", server(lang, web, hidden));
            await p.goto("http://magpie.test/?view=usage");
            const card = p.locator('#subscriptionUsage > [data-provider="kiro"]');
            await card.locator(".credit-days").first().waitFor({ timeout: 20000 });
            const rows = await card.locator(".quota-name").allTextContents();
            const pools = await card.locator(".credit-days .cd-head span").allTextContents();
            await p.close();
            return { rows, pools };
          };
          const region = (list) => list.filter((s) => /us-east-1|eu-central-1/.test(s)).length;

          // nothing hidden: four pools, two of them a region's
          let s = await seen([]);
          assert.equal(s.pools.length, 4, s.pools.join(" | "));
          assert.equal(region(s.pools), 2);

          // the regions' rows hidden: their pools go with them
          s = await seen(REGIONS);
          assert.equal(region(s.rows), 0, s.rows.join(" | "));
          assert.equal(s.pools.length, 2, s.pools.join(" | "));
          assert.equal(region(s.pools), 0, s.pools.join(" | "));

          // one pool left is drawn without a name, as a card of one pool is
          s = await seen(["kiro|Overage", ...REGIONS]);
          assert.equal(s.pools.length, 1, s.pools.join(" | "));
          assert.ok(!s.pools[0].includes(" · "), s.pools[0]);

          // every row hidden: the card shows them all again, and every pool
          s = await seen(EVERY);
          assert.equal(region(s.rows), 3, s.rows.join(" | "));
          assert.equal(s.pools.length, 4, s.pools.join(" | "));

          await ctx.close();
          assert.deepEqual(errors, []);
        });
      }
    }
  });
}
