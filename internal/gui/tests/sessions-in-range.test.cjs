// Run with Node's test runner and Playwright on the module path; see README.md.
// The Sessions tab's list is the sessions the range's totals count, not every
// session whose file was last written in the range: a session's file goes on
// being written after its last call — a fork's seed, a setting — so its last
// line can fall inside "7 days" while the call it spent on fell outside. Such
// a session was listed and not counted, the note under the list saying more
// sessions than the KPI above it (35 against 41, 163 against 168 on a real
// history). Stats says which sessions were at work (keys); the list keeps
// those and no others, so the two agree. English, Chinese, Japanese and
// German, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const iso = (d) => [d.getFullYear(), d.getMonth() + 1, d.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const to = new Date(2026, 8, 28);
const back = (i) => { const d = new Date(to); d.setDate(d.getDate() - i); return d; };
const DAYS = 7;
const from = iso(back(DAYS - 1));

// Two sessions, both with a last line inside the range. Only "a" spent
// anything in it: "b"'s last call was three weeks ago, and what the range
// keeps of it is the seed line a fork wrote after it.
const sessions = [
  { agent: "claude", name: "Claude Code", icon: "claude", id: "a", cwd: "/work/alpha", title: "Worked in the range",
    start: back(2).toISOString(), last: back(2).toISOString(),
    input: 30000, output: 4000, cache_read: 0, cache_write: 0, cost: 1.5, priced: true,
    models: [{ model: "claude-opus-4", input: 30000, output: 4000, cost: 1.5, priced: true }], resume: "claude --resume a" },
  { agent: "claude", name: "Claude Code", icon: "claude", id: "b", cwd: "/work/alpha", title: "Touched in the range only",
    start: back(21).toISOString(), last: back(1).toISOString(),
    input: 90000, output: 9000, cache_read: 0, cache_write: 0, cost: 4, priced: true,
    models: [{ model: "claude-opus-4", input: 90000, output: 9000, cost: 4, priced: true }], resume: "claude --resume b" },
];
const days = Array.from({ length: DAYS }, (_, i) => {
  const d = back(DAYS - 1 - i);
  return { date: iso(d), usage: [{ agent: "claude", cwd: "/work/alpha", model: "claude-opus-4", input: 30000, output: 4000, cache_read: 0, cache_write: 0, cost: 1.5, priced: true }], active: [] };
});
// the one session at work in the range, as Stats names it
const keys = ["claude:a"];

// `withKeys` off serves a stats payload without keys, as an older binary's
// would: the list falls back to the old guess from each session's last line,
// and must draw from it rather than drawing nothing.
function serve(lang, withKeys = true) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/t.json", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/sessions/progress") return json({ indexing: false });
    if (url.pathname === "/api/sessions") return json({ sessions, dirs: ["/test/sessions"] });
    if (url.pathname === "/api/sessions/stats") return json({ from, to: iso(to), days, ...(withKeys ? { keys } : {}), agents: { claude: "Claude Code" } });
    if (url.pathname === "/api/sessions/overview") return json({
      count: keys.length, median: 34000, p90: 34000, days: days.map(() => 1),
      top: { tokens: [], cost: [], active: [] }, messages: days.map(() => 30), output: days.map(() => 0),
      shape: {
        messages: { edges: [1, 6, 16, 31, 61, 121], counts: [1], total: 1 },
        minutes: { edges: [1, 6, 16, 31, 61, 121], counts: [1], total: 1 },
        autonomy: { edges: [0, 1, 3, 6, 11, 21], counts: [1], total: 1 },
      },
      tools: { calls: 0, sessions: 0, top: [], categories: [], weeks: [] },
      skills: { calls: 0, count: 0, top: [] },
    });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/usage/quotas") return json([]); // an array, as the Go side writes it
    if (url.pathname === "/api/usage/quotas/history") return json({ days: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    return body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" });
  };
}

// The KPI's label is set in capitals by CSS, so the tile reads "1 / SESSIONS"
// and "1 / SITZUNGEN"; Chinese and Japanese are caseless. The note is one
// session, and its own wording per language ("1 个会话", "1 件のセッション",
// "1 Sitzung" — the plural picks itself).
const want = {
  en: { kpi: /^1\nSESSIONS/, note: /· 1 session ·/ },
  zh: { kpi: /^1\n会话数/, note: /· 1 个会话 ·/ },
  ja: { kpi: /^1\nセッション/, note: /· 1 件のセッション ·/ },
  de: { kpi: /^1\nSITZUNGEN/, note: /· 1 Sitzung ·/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    test(`${engine} ${lang}: the Sessions list is the sessions the range counted`, async (t) => {
      const w = want[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1000, height: 760 }, reducedMotion: "reduce", timezoneId: "Asia/Shanghai" });
      const page = await context.newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(10000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      // the Sessions tab over 7 days, as a reader who picked it is left
      await page.addInitScript(() => { localStorage.setItem("magpie.usageTab", "sessions"); localStorage.setItem("magpie.sessRange", "7d"); });
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=usage");
      await page.locator("#sessGrid:not([hidden])").waitFor();
      const rows = page.locator("#sessList .row.sess");
      await rows.first().waitFor();

      // one session: the one the range counted. The other's last line falls
      // in the range, but it spent nothing in it.
      assert.equal(await rows.count(), 1, "the list holds the sessions at work in the range");
      assert.equal(await rows.first().innerText().then((x) => x.includes("Worked in the range")), true, "the one at work is the one drawn");
      // and the number beside the list is that list's, so it says the same
      // thing the KPI above it does
      assert.match(await page.locator("#sessNote").innerText(), w.note, "the note counts the list");
      assert.match(await page.locator("#sessStats .kpi").first().innerText(), w.kpi, "the KPI counts the same sessions");
      assert.deepEqual(errors, []);
    });

    // a stats payload without keys (an older binary's) keeps the old guess:
    // the list is drawn from the last lines rather than drawn empty
    test(`${engine} ${lang}: without keys the list falls back to the last line`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1000, height: 760 }, reducedMotion: "reduce", timezoneId: "Asia/Shanghai" });
      const page = await context.newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(10000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.addInitScript(() => { localStorage.setItem("magpie.usageTab", "sessions"); localStorage.setItem("magpie.sessRange", "7d"); });
      await page.route("**/*", serve(lang, false));
      await page.goto("http://magpie.test/?view=usage");
      const rows = page.locator("#sessList .row.sess");
      await rows.first().waitFor();
      assert.equal(await rows.count(), 2, "both sessions whose last line is in the range");
      assert.deepEqual(errors, []);
    });
  }
}
