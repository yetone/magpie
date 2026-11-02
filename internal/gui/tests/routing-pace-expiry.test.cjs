// Run with Node's test runner and Playwright on the module path; see README.md.
// Use before expiry (pace): a WorkBuddy account whose check-in credits
// expire before its month renews goes first, and the Routing page's story
// says the credits' expiry set its deadline, not its reset (lcyksp on
// Discord). An account whose month decides is told as before. In every
// language, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const ago = (minutes) => new Date(now.getTime() - minutes * 60e3).toISOString();
const ahead = (hours) => new Date(now.getTime() + hours * 36e5).toISOString();
const base = { provider: "workbuddy", name: "WorkBuddy", kind: "account", agent: "workbuddy", model: "deepseek-v4.1-flash", known: true };
// A: its month untouched, renewing in 28 days, 70.85 of its 500 credits
// expiring in 14 hours: 14% lost then, 1% an hour
const a = (fields) => ({ ...base, id: "workbuddy#a", who: "a@example.com", routing: "pace", used: 0, renews: [ahead(672)], ...fields });
const b = () => ({ ...base, id: "workbuddy#b", who: "b@example.com", routing: "pace", used: 50, renews: [ahead(240)], pace: 50 / 240, due: ahead(240) });
const route = (id, order) => ({
  id, seq: id, time: ago(1), agent: "workbuddy", model: "deepseek-v4.1-flash", provider: "workbuddy", done: true, status: 200, ms: 900, tokens: 1200,
  order, tries: [{ id: order[0].id, model: "deepseek-v4.1-flash", start: ago(1), done: true, status: 200, ms: 900 }],
});
const routes = [
  route(2, [a({ pace: 70.85 / 500 * 100 / 14, due: ahead(14), dueBy: "expiry", lapse: ahead(14) }), b()]),
  route(1, [b(), a({ pace: 100 / 672, due: ahead(672) })]),
];

function serve(lang) {
  return async (request) => {
    const url = new URL(request.request().url());
    const json = (data) => request.fulfill({ json: data });
    if (url.pathname === "/boot.js") return request.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return request.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "workbuddy", name: "WorkBuddy", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: now.toISOString(), seq: 4, totals: { requests: routes.length }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await request.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// expiry: the credits that expire, 14% of A's month, in 14 hours; week:
// the reason an account whose reset decides is told by
const words = {
  en: { expiry: /most credits expiring per hour until they expire — 14\s?% of its allowance expires in/, week: /per hour until reset/ },
  zh: { expiry: /快过期的积分 ÷ 距过期小时数最高，额度的 14\s?% 将在/, week: /÷ 距重置小时数最高/ },
  "zh-TW": { expiry: /快過期的點數 ÷ 距過期小時數最高，額度的 14\s?% 將在/, week: /÷ 距重置小時數最高/ },
  ja: { expiry: /期限切れ間近のクレジットが最も多いです — 枠の 14\s?% が/, week: /リセットまでの 1 時間あたりの残り利用枠/ },
  de: { expiry: /das meiste ablaufende Guthaben – 14\s?% seines Kontingents läuft in/, week: /Restkontingent pro Stunde bis zum Zurücksetzen/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: credits expiring before the month renews are told as the deadline`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await browser.newPage({ viewport: { width, height: 1200 }, reducedMotion: "reduce" });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (error) => errors.push(error.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=routing");
        await page.locator(".rt-req").nth(routes.length - 1).waitFor();
        const w = words[lang];
        const why = () => page.locator(".rt-steps li.why").first().textContent();
        const open = async (n, who, want) => {
          await page.locator(".rt-req").nth(n).click();
          await page.waitForFunction(([who, src]) => {
            const li = document.querySelector(".rt-steps li.why");
            return li && li.textContent.includes(who) && new RegExp(src).test(li.textContent);
          }, [who, want.source]);
        };
        await open(0, "a@example.com", w.expiry);
        assert.doesNotMatch(await why(), w.week);
        // the month decides: as before
        await open(1, "b@example.com", w.week);
        assert.doesNotMatch(await why(), w.expiry);
        const wide = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
        assert.ok(wide <= 0, `the page scrolls sideways by ${wide}px`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
