// Run with Node's test runner and Playwright on the module path; see README.md.
// #309 (novus77): Usage › Sessions › By day, today's bar hovered showed no
// usage. The page reads the sessions again every fifteen seconds, and while
// an agent is at work today's numbers have changed each time — so the whole
// chart was drawn anew, the bar under the pointer taken away with its
// tooltip. The same days by the same metric now keep their bars, filled in
// again where they are: today's bar stays under the pointer, hovered, its
// title today's numbers as they are now. A metric picked after that draws
// the numbers now in, not those of the first draw. The API is faked here,
// with today's usage growing at each read; the fifteen seconds are the page
// clock's, run forward.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const TZ = "Asia/Shanghai";
// today where the page is, as the Go side dates it
const isoIn = (d) => new Intl.DateTimeFormat("en-CA", { timeZone: TZ, year: "numeric", month: "2-digit", day: "2-digit" }).format(d);
const today = isoIn(new Date());
const back = (i) => { const [y, m, d] = today.split("-").map(Number); return isoIn(new Date(Date.UTC(y, m - 1, d - i, 12))); };

function serve(lang, reads) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/settings.json", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  const use = (input, output) => [{ agent: "claude", cwd: "/work/alpha", model: "claude-opus-4", input, output, cache_read: 0, cache_write: 0, cost: 0, priced: true }];
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/sessions/progress") return json({ indexing: false });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: ["/test/sessions"] });
    // 30 days up to today; today's tokens grow a million each read
    const days = Array.from({ length: 30 }, (_, i) => back(29 - i)).map((date, i) => ({
      date, usage: i === 29 ? use(1_000_000 * reads.n, 100_000 * reads.n) : use(2_000_000, 200_000), active: [],
    }));
    if (url.pathname === "/api/sessions/stats") {
      reads.n++;
      days[29].usage = use(1_000_000 * reads.n, 100_000 * reads.n);
      return json({ from: days[0].date, to: today, days, agents: { claude: "Claude Code" } });
    }
    if (url.pathname === "/api/sessions/overview") {
      return json({ count: 3, median: 1000, p90: 2000, days: days.map(() => 1), messages: days.map(() => 10), output: days.map(() => 0), top: { tokens: [], cost: [], active: [] } });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    // opening the page asks for the quotas too (an array, as the Go side writes it)
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

const want = {
  en: { day: new Date(today + "T12:00:00Z").toLocaleDateString("en", { month: "short", day: "numeric", timeZone: "UTC" }), tokens: (n) => `${(1.1 * n).toFixed(1).replace(/\.0$/, "")}M tokens`, output: "Output tokens" },
  zh: { day: new Date(today + "T12:00:00Z").toLocaleDateString("zh-CN", { month: "short", day: "numeric", timeZone: "UTC" }), tokens: (n) => `${Math.round(110 * n)} 万 token`, output: "输出 Token" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: today's bar keeps its tooltip`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1100, height: 760 }, reducedMotion: "reduce", timezoneId: TZ });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      const errors = [], reads = { n: 0 };
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, reads));
      await page.clock.install();
      await page.addInitScript(() => { localStorage.setItem("magpie.usageTab", "sessions"); localStorage.setItem("magpie.sessRange", "30d"); });
      t.after(() => browser.close());
      await page.goto("http://magpie.test/?view=usage");
      const bars = page.locator("#sessChart .bars > .bar");
      await page.waitForFunction(() => document.querySelectorAll("#sessChart .bars > .bar").length === 30);
      const last = bars.last();
      const title = () => last.getAttribute("title");
      const w = want[lang];

      await t.test("today is the last bar, its usage in its title", async () => {
        const n = reads.n;
        assert(n >= 1);
        const tt = await title();
        assert(tt.startsWith(w.day + " · "), tt);
        assert(tt.includes(w.tokens(n)), tt);
      });

      await t.test("read again while hovered, the bar stays, its title today's now", async () => {
        const top = await page.evaluate(() => document.scrollingElement.scrollTop);
        await last.hover();
        await page.evaluate(() => { window.__today = document.querySelector("#sessChart .bars > .bar:last-child"); });
        const was = reads.n, before = await title();
        await page.clock.runFor(16000);
        await page.waitForFunction((b) => document.querySelector("#sessChart .bars > .bar:last-child")?.title !== b, before);
        assert(reads.n > was, "the sessions were not read again");
        const now = await title();
        assert(now.includes(w.tokens(reads.n)), now);
        assert.equal(await page.evaluate(() => window.__today.isConnected), true, "today's bar was drawn anew under the pointer");
        assert.equal(await page.evaluate(() => window.__today.matches(":hover")), true);
        assert.equal(await bars.count(), 30);
        const tt = await title();
        assert(tt.startsWith(w.day + " · "), tt);
        const h = await last.locator("i.in").evaluate((e) => parseFloat(e.style.height));
        assert(h > 0, "today's bar is empty");
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), top);
      });

      await t.test("a metric picked after draws the numbers now in", async () => {
        const n = reads.n;
        await page.locator("#sessChart .segs .opt", { hasText: w.output }).click();
        await page.waitForFunction(() => document.querySelector("#sessChart .bars > .bar:last-child i.in") === null);
        const tt = await title();
        assert(tt.includes(w.tokens(n)), tt);
      });

      assert.deepEqual(errors, []);
    });
  }
}
