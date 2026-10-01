// Run with Node's test runner and Playwright on the module path; see README.md.
// The Sessions overview's bars, a name, a bar and figures a row (John on
// Discord: widening the window grew the bars and left the names cut short
// with "…"): the names were held to 9em, 10em and 11em whatever the width.
// Now a card's names take the width the longest needs and the bars what is
// left, every row's bar starting at the same place; narrow, the names are
// shortened and a bar is never under 5em.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const iso = (d) => [d.getFullYear(), d.getMonth() + 1, d.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const to = new Date(2026, 8, 28);
const back = (i) => { const d = new Date(to); d.setDate(d.getDate() - i); return d; };
const LONG_DIR = "/work/a-rather-long-project-folder-name";
const LONG_MODEL = "claude-sonnet-4-5-20250929-extended";
const LONG_TOOL = "mcp__slack__send_message_draft";
const LONG_SKILL = "document-skills:frontend-design";
const allDays = Array.from({ length: 20 }, (_, i) => back(19 - i)).map((d, i) => ({
  date: iso(d),
  usage: [
    { agent: "claude", cwd: LONG_DIR, model: LONG_MODEL, input: 30000 * (i + 1), output: 4000, cache_read: 0, cache_write: 0, cost: 1.5, priced: true },
    { agent: "codex", cwd: "/work/b", model: "gpt-5", input: 10000, output: 2000, cache_read: 0, cache_write: 0, cost: 0.4, priced: true },
  ],
  active: [],
}));

function serve(lang) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/settings.json", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/sessions/progress") return json({ indexing: false });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: ["/test/sessions"] });
    if (url.pathname === "/api/sessions/stats") return json({ from: allDays[0].date, to: iso(to), days: allDays, agents: { claude: "Claude Code", codex: "Codex" } });
    if (url.pathname === "/api/sessions/overview") {
      return json({ count: 4, median: 1000, p90: 2000, days: allDays.map(() => 1), top: { tokens: [], cost: [], active: [] },
        messages: allDays.map(() => 3), output: allDays.map(() => 0),
        shape: { messages: { edges: [1], counts: [4], total: 4 }, minutes: { edges: [1], counts: [4], total: 4 }, autonomy: { edges: [0], counts: [4], total: 4 } },
        tools: {
          calls: 100, sessions: 4,
          top: [{ name: LONG_TOOL, category: "Tool", calls: 60, sessions: 3 }, { name: "Bash", category: "Bash", calls: 40, sessions: 4 }],
          categories: [{ name: "Tool", calls: 60, sessions: 3 }, { name: "Bash", calls: 40, sessions: 4 }],
          weeks: [],
        },
        skills: {
          calls: 30, count: 2,
          top: [{ name: LONG_SKILL, calls: 20, sessions: 3, agents: { claude: 20 }, projects: [] }, { name: "pdf", calls: 10, sessions: 1, agents: { claude: 10 }, projects: [] }],
        } });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

// each bar row of the four cards: its name's full and shown widths, its bar's
const rows = (page) => page.evaluate(() => {
  const out = [];
  const pick = (sel, name, track) => document.querySelectorAll(sel).forEach((r) => {
    const n = r.querySelector(name), b = r.querySelector(track).getBoundingClientRect();
    out.push({ sel, text: n.textContent, full: n.scrollWidth, shown: n.clientWidth, bar: b.width, left: b.left, card: r.closest(".sess-card").id });
  });
  pick("#sessProjects .sess-bar", ".n", ".track");
  pick("#sessModels .sess-bar", ".n", ".track");
  pick("#sessTools .sess-bar.tool", ".n span", ".track");
  pick("#sessSkills .sess-skill .line", ".n", ".track");
  return out;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: the sessions bars' names take the width they need`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1600, height: 900 }, reducedMotion: "reduce", timezoneId: "Asia/Shanghai" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.addInitScript(() => { localStorage.setItem("magpie.usageTab", "sessions"); localStorage.setItem("magpie.sessRange", "all"); });
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-sess-bars.png`), fullPage: true });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/?view=usage");
      await page.locator("#sessGrid:not([hidden])").waitFor();
      await page.waitForFunction(() => document.querySelector("#sessSkills .sess-skill") && document.querySelector("#sessTools .sess-bar.tool") && document.querySelectorAll("#sessModels .sess-bar").length === 2);

      await t.test("wide: every name whole, the bars still there and lined up", async () => {
        const rs = await rows(page);
        assert.equal(rs.length, 8, JSON.stringify(rs));
        for (const r of rs) {
          assert.ok(r.full <= r.shown + 0.5, `${r.sel} "${r.text}" cut short: ${r.full} > ${r.shown}`);
          assert.ok(r.bar >= 60, `${r.sel} "${r.text}" bar ${r.bar}`);
        }
        for (const card of new Set(rs.map((r) => r.card))) {
          const lefts = rs.filter((r) => r.card === card).map((r) => Math.round(r.left));
          assert.equal(new Set(lefts).size, 1, `${card} bars start at ${lefts}`);
        }
        assert.ok(rs.some((r) => r.text === LONG_DIR.split("/").pop()) && rs.some((r) => r.text === LONG_MODEL));
      });

      await t.test("narrow: the long names shortened, no bar under 5em", async () => {
        await page.setViewportSize({ width: 420, height: 900 });
        await page.waitForTimeout(100);
        const rs = await rows(page);
        for (const r of rs) assert.ok(r.bar >= 59, `${r.sel} "${r.text}" bar ${r.bar}`);
        assert.ok(rs.some((r) => r.full > r.shown), "a long name should be shortened at 420 wide");
        // nothing past its card
        const over = await page.evaluate(() => [...document.querySelectorAll("#sessGrid .sess-card")].filter((c) => c.scrollWidth > c.clientWidth + 1).map((c) => c.id));
        assert.deepEqual(over, []);
      });

      await t.test("wide again", async () => {
        await page.setViewportSize({ width: 1600, height: 900 });
        await page.waitForTimeout(100);
        for (const r of await rows(page)) assert.ok(r.full <= r.shown + 0.5, `${r.sel} "${r.text}" cut short`);
      });
      assert.deepEqual(errors, []);
    });
  }
}
