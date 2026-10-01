// Run with Node's test runner and Playwright on the module path; see README.md.
// The Sessions tab's activity calendar fills its card (John on Discord: 这个活跃度
// 怎么没有铺满全部宽度呢？): 118 days are 17 weeks, and their cells took the left
// part of a wide card and left the rest empty. The weeks before the range now
// come in as empty cells, GitHub's way, without tooltips; the range's own days
// keep theirs; the cells stay square, the month names sit over their weeks, and
// the calendar follows the window as it is made wider or narrower without
// running past the card.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const iso = (d) => [d.getFullYear(), d.getMonth() + 1, d.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const to = new Date(2026, 8, 28);
const N = 118;
const allDays = Array.from({ length: N }, (_, i) => { const d = new Date(to); d.setDate(d.getDate() - (N - 1 - i)); return d; }).map((d, i) => {
  const date = iso(d);
  if (i % 2) return { date, usage: [], active: [] };
  const n = 1 + (i % 5);
  return {
    date,
    usage: [{ agent: "claude", cwd: "/work/alpha", model: "claude-opus-4", input: 30000 * n, output: 4000 * n, cache_read: 0, cache_write: 0, cost: 1.5 * n, priced: true }],
    active: [{ agent: "claude", cwd: "/work/alpha", seconds: 600 * n, hours: Object.assign(new Array(24).fill(0), { 9: 600 * n }) }],
  };
});

function serve(lang) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/settings.json", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/sessions/progress") return json({ indexing: false });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: ["/test/sessions"] });
    if (url.pathname === "/api/sessions/stats") return json({ from: allDays[0].date, to: iso(to), days: allDays, agents: { claude: "Claude Code" } });
    if (url.pathname === "/api/sessions/overview") {
      return json({ count: 40, median: 120000, p90: 880000, days: allDays.map((d) => d.usage.length ? 2 : 0), top: { tokens: [], cost: [], active: [] },
        messages: allDays.map((d) => d.usage.length ? 30 : 0), output: allDays.map(() => 0) });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

// how the calendar sits in its card
const measure = () => {
  const chart = document.querySelector("#sessChart"), wrap = chart.querySelector(".sess-cal-wrap");
  const cal = wrap.querySelector(".sess-cal"), side = wrap.querySelector(".sess-cal-side");
  const w = wrap.getBoundingClientRect(), c = cal.getBoundingClientRect(), s = side.getBoundingClientRect();
  const days = [...cal.querySelectorAll(":scope > i")], pads = [...cal.querySelectorAll(":scope > s")];
  const cell = days[0].getBoundingClientRect();
  // each month's name starts where its week's cells do
  const cols = new Map([...days, ...pads].map((e) => [e.style.gridColumnStart || e.style.gridArea.split("/")[1].trim(), e.getBoundingClientRect().left]));
  const off = [...cal.querySelectorAll(".mo")].map((m) => Math.abs(m.getBoundingClientRect().left - cols.get(m.style.gridArea.split("/")[1].trim())));
  return {
    // the part of the card the calendar and its facts take, side by side or one over the other
    used: (Math.max(c.right, s.right) - w.left) / w.width, row: s.top < c.bottom - 4,
    days: days.length, titled: days.filter((e) => e.title).length, pads: pads.length, padTitles: pads.filter((e) => e.title).length,
    square: Math.abs(cell.width - cell.height) < 1, cell: cell.width, months: off.length, monthOff: Math.max(0, ...off),
    overflow: document.documentElement.scrollWidth - document.documentElement.clientWidth, past: Math.max(c.right, s.right) - w.right,
  };
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: the activity calendar fills its card`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1400, height: 820 }, reducedMotion: "reduce", timezoneId: "Asia/Shanghai" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.addInitScript(() => { localStorage.setItem("magpie.usageTab", "sessions"); localStorage.setItem("magpie.sessRange", "all"); });
      const shot = async (name) => {
        if (!process.env.ARTIFACT_DIR) return;
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.locator("#sessChart").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-calendar-${name}.png`) });
      };
      t.after(() => browser.close());
      await page.goto("http://magpie.test/?view=usage");
      await page.locator("#sessChart .sess-cal i").first().waitFor();

      await t.test("a wide window: the weeks before fill the card", async () => {
        await shot("wide");
        const m = await page.evaluate(measure);
        assert(m.row, "the facts sit beside the calendar");
        assert(m.used > 0.9, `the calendar and its facts take ${(100 * m.used).toFixed(0)}% of the card`);
        assert(m.past <= 1, `${m.past}px past the card`);
        assert.equal(m.days, N);
        assert.equal(m.titled, N);
        assert(m.pads > 0);
        assert.equal(m.padTitles, 0, "a week before the range says nothing of it");
        assert(m.square, "the cells are square");
        assert(m.cell >= 12 && m.cell <= 22, `cells ${m.cell}px`);
        assert(m.months >= 6, `${m.months} months named`);
        assert(m.monthOff < 1, `a month's name is ${m.monthOff}px off its week`);
        assert.equal(m.overflow, 0);
      });

      await t.test("narrower: fewer weeks before, still across the card", async () => {
        await page.setViewportSize({ width: 1000, height: 820 });
        await page.waitForFunction(() => document.querySelector("#sessChart .sess-cal-wrap").clientWidth < 1200);
        await page.waitForFunction(() => { const w = document.querySelector("#sessChart .sess-cal-wrap"), c = w.querySelector(".sess-cal"); return c.getBoundingClientRect().right <= w.getBoundingClientRect().right; });
        await shot("medium");
        const m = await page.evaluate(measure);
        assert(m.used > 0.9, `the calendar and its facts take ${(100 * m.used).toFixed(0)}% of the card`);
        assert(m.past <= 1, `${m.past}px past the card`);
        assert.equal(m.days, N);
        assert(m.square);
        assert(m.monthOff < 1);
        assert.equal(m.overflow, 0);
      });

      await t.test("a narrow window: the facts go under, the weeks still across", async () => {
        await page.setViewportSize({ width: 560, height: 820 });
        await page.waitForFunction(() => document.querySelector("#sessChart .sess-cal-wrap").clientWidth < 560);
        await page.waitForFunction(() => { const w = document.querySelector("#sessChart .sess-cal-wrap"), c = w.querySelector(".sess-cal"); return c.getBoundingClientRect().right <= w.getBoundingClientRect().right; });
        await shot("narrow");
        const m = await page.evaluate(measure);
        assert(!m.row, "the facts sit under the calendar");
        assert(m.used > 0.9, `the calendar takes ${(100 * m.used).toFixed(0)}% of the card`);
        assert(m.past <= 1, `${m.past}px past the card`);
        assert.equal(m.days, N);
        assert(m.square);
        assert.equal(m.overflow, 0);
      });

      // narrower than the window may be made (560), so the card is narrower
      // than the range's own weeks: the page around it runs over, the calendar doesn't
      await t.test("narrower than the range: no weeks before, the cells shrink", async () => {
        await page.setViewportSize({ width: 360, height: 820 });
        await page.waitForFunction(() => document.querySelectorAll("#sessChart .sess-cal > s").length === 0);
        await shot("narrowest");
        const m = await page.evaluate(measure);
        assert(m.past <= 1, `${m.past}px past the card`);
        assert.equal(m.days, N);
        assert(m.square);
        assert(m.cell < 14, `cells ${m.cell}px`);
      });

      await t.test("wide again: the weeks before come back", async () => {
        await page.setViewportSize({ width: 1400, height: 820 });
        await page.waitForFunction(() => document.querySelectorAll("#sessChart .sess-cal > s").length > 0);
        const m = await page.evaluate(measure);
        assert(m.used > 0.9, `${(100 * m.used).toFixed(0)}%`);
        assert.deepEqual(errors, []);
      });
    });
  }
}
