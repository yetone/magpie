// The saved time step changes the overview's range and labels; its bars
// fit both wide and narrow windows. API fixtures never touch user data.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const assets = path.resolve(__dirname, "../assets");

function server(lang) {
  let cur = {
    lang, theme: "light", tray: "panel", currency: "usd", usageBucket: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    visionModels: [], imageGenModels: [], proxyNow: "none", proxySource: "none",
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: cur });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") cur = { ...cur, ...req.postDataJSON() };
      return json(cur);
    }
    if (url.pathname === "/api/usage") {
      const step = cur.usageBucket === "10m" ? 600e3 : 3600e3;
      const from = Date.parse("2026-10-01T00:00:00Z");
      const count = cur.usageBucket === "10m" ? 120 : cur.usageBucket === "hour" ? 60 : 30;
      const series = Array.from({ length: count }, (_, i) => ({
        label: cur.usageBucket ? new Date(from + i * step).toISOString().slice(11, 16) : "Sep " + (i + 1),
        time: new Date(from + i * step).toISOString(), input: i === 0 ? 120 : 0, output: 0, calls: i === 0 ? 1 : 0, cost: 0, unpriced: 1,
      }));
      const emptyToday = url.searchParams.get("period") === "today";
      return json({ calls: emptyToday ? 0 : 1, input: emptyToday ? 0 : 120, output: 0, cache_read: 0, reasoning: 0,
        cost: 0, unpriced: 1, bucket: cur.usageBucket || "day", series, agents: [], models: [],
        ...(cur.usageBucket ? { chartFrom: new Date(from).toISOString(), chartTo: new Date(from + count * step).toISOString() } : {}) });
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": saved chart interval and responsive bars", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium", executablePath: process.env.PLAYWRIGHT_EXECUTABLE_PATH }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const context = await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce", timezoneId: "Asia/Singapore" });
        const page = await context.newPage();
        t.after(() => context.close());
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        t.after(() => assert.deepEqual(errors, [], "browser errors"));
        page.setDefaultTimeout(5000);
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/");
        await page.locator('[data-view="usage"]').click();
        await page.waitForFunction(() => document.querySelectorAll("#chart .bar").length === 30).catch(async (e) => {
          throw new Error(`${e.message}; errors: ${errors}; page: ${await page.locator("body").innerText()}`);
        });
        const dailyWidth = (await page.locator("#chart .bar").first().boundingBox()).width;
        const select = async (i) => {
          await page.locator("#prefs").click();
          await page.locator("#usageBucketSegs .opt").nth(i).click();
          await page.waitForFunction((i) => document.querySelectorAll("#usageBucketSegs .opt")[i]?.classList.contains("on"), i);
          await page.locator('[data-view="usage"]').click();
          await page.waitForFunction((count) => document.querySelectorAll("#chart .bar").length === count, i === 1 ? 60 : 120);
        };
        await select(1);
        assert.match(await page.locator("#chart .usage-chart-head").textContent(), lang === "zh" ? /最近 60 小时/ : /last 60 hours/);
        const hourWidth = (await page.locator("#chart .bar").first().boundingBox()).width;
        assert(hourWidth < dailyWidth, "60 bars shrink to fit the space previously used by 30");
        await select(2);
        assert.match(await page.locator("#chart .usage-chart-head").textContent(), lang === "zh" ? /最近 120 个时间窗（20 小时）/ : /last 120 intervals \(20 hours\)/);
        assert.equal((await page.locator("#chart .labels span").allTextContents()).filter(Boolean)[1], "12:00", "labels use the client's timezone, like the range heading");
        assert.match(await page.locator("#chart .bar").first().getAttribute("title"), /120/);
        await page.setViewportSize({ width: 480, height: 700 });
        const narrowWidth = (await page.locator("#chart .bar").first().boundingBox()).width;
        assert(narrowWidth < hourWidth && narrowWidth > 0, "bar widths follow the available space");
        assert(await page.locator("#chart").evaluate((chart) => chart.scrollWidth <= chart.clientWidth), "labels and range fit a narrow window");
        // Today may be empty while yesterday's calls remain in the rolling chart.
        await page.locator("#period .opt").first().click();
        await page.locator("#stats.empty").waitFor();
        assert(await page.locator("#chart").isVisible());
        await page.reload();
        await page.locator("#prefs").click();
        await page.waitForFunction(() => document.querySelectorAll("#usageBucketSegs .opt")[2]?.classList.contains("on"));
        // Saving another preference must carry the chosen interval along.
        await page.locator("#themeSegs .opt").last().click();
        await page.waitForFunction(() => document.querySelectorAll("#themeSegs .opt")[2]?.classList.contains("on"));
        await page.reload();
        await page.locator("#prefs").click();
        await page.waitForFunction(() => document.querySelectorAll("#usageBucketSegs .opt")[2]?.classList.contains("on"));
        if (process.env.ARTIFACT_DIR) {
          await page.locator('[data-view="usage"]').click();
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-usage-chart-${lang}.png`) });
        }
        assert.deepEqual(errors, []);
      });
    }
  });
}
