// Run with Node's test runner and Playwright on the module path; see README.md.
// The Overview chart's days in the reader's language (the owner: 用量图表的
// 日期也按语言格式化): the backend labels a day "Sep 5" in every language,
// and the chart showed that label under its bars and in their titles, in
// Chinese and Japanese too. It now writes the point's time as the page's
// language does: "Sep 5" in English, "9月5日" in Chinese and Japanese, and a
// week's bar "9月5日 起的一周" / "9月5日 の週". An hour's bar keeps "15:00".
// Chromium and WebKit; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

// three points from Sep 5, a day or a week apart, labelled as the backend does
function overview(bucket) {
  const step = bucket === "week" ? 7 : 1;
  const series = [0, 1, 2].map((i) => {
    const d = new Date(2026, 8, 5 + step * i);
    return { label: d.toLocaleDateString("en", { month: "short", day: "numeric" }), time: d.toISOString(), calls: 3, input: 1000, output: 500, cost: 0.1 };
  });
  return { calls: 9, errors: 0, input: 3000, output: 1500, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 0, cost: 0.3, bucket, series, agents: [], models: [] };
}
const hours = { calls: 3, errors: 0, input: 1000, output: 500, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 0, cost: 0.1, bucket: "hour",
  series: [{ label: "15", time: new Date(2026, 8, 5, 15).toISOString(), calls: 3, input: 1000, output: 500, cost: 0.1 }], agents: [], models: [] };

function serve(lang, answer) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", currency: "usd" } });
    if (url.pathname === "/api/usage") return json(answer());
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/gateway/trace" && url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/gateway/trace") return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const WANT = {
  en: { day: ["Sep 5", "Sep 6", "Sep 7"], week: "week of Sep 12", hour: "15:00" },
  zh: { day: ["9月5日", "9月6日", "9月7日"], week: "9月12日 起的一周", hour: "15:00" },
  ja: { day: ["9月5日", "9月6日", "9月7日"], week: "9月12日 の週", hour: "15:00" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Usage chart's days in the page's language", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "ja"]) {
      await t.test(lang, async () => {
        const ctx = await browser.newContext({ viewport: { width: 1000, height: 700 }, reducedMotion: "reduce", locale: "en-US" });
        const p = await ctx.newPage();
        const errors = [];
        p.setDefaultTimeout(5000);
        p.on("pageerror", (e) => errors.push(e.message));
        let answer = () => overview("day");
        await p.route("**/*", serve(lang, () => answer()));
        await p.goto("http://magpie.test/");
        await p.locator('[data-view="usage"]').first().click();
        const labels = () => p.locator("#chart .labels span").allTextContents();
        const title = (i) => p.locator("#chart .bar").nth(i).getAttribute("title");
        await p.waitForFunction(() => document.querySelectorAll("#chart .labels span").length === 3);
        assert.deepEqual((await labels()).map((x) => x.trim()), WANT[lang].day, lang + ": the days under the bars");
        assert((await title(0)).startsWith(WANT[lang].day[0] + " · "), `${lang}: the first bar's title "${await title(0)}"`);

        // a week's bar
        answer = () => overview("week");
        await p.evaluate(() => loadUsage?.());
        await p.waitForFunction((w) => (document.querySelectorAll("#chart .bar")[1]?.title || "").startsWith(w), WANT[lang].week).catch(() => {});
        assert((await title(1)).startsWith(WANT[lang].week + " · "), `${lang}: the week's title "${await title(1)}"`);

        // an hour's bar keeps its hour
        answer = () => hours;
        await p.evaluate(() => loadUsage?.());
        await p.waitForFunction(() => document.querySelectorAll("#chart .bar").length === 1).catch(() => {});
        assert((await title(0)).startsWith(WANT[lang].hour + " · "), `${lang}: the hour's title "${await title(0)}"`);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
