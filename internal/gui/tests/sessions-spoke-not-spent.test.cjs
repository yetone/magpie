// Run with Node's test runner and Playwright on the module path; see README.md.
// The list is where a session is found and its resume command copied, so a
// session the page lists has to be one the range counted. Some sessions say
// something but spent nothing: a Claude Code chat whose first request never
// came back, and every Cursor chat — its messages keep no tokens, and a chat
// worked on with pauses longer than five minutes keeps no active time. Those
// are listed, yet counting only what was spent leaves them out of keys and so
// out of every range, the whole of time included. Stats counts a day the
// session spoke on as a day it was at work, and the KPI, the note under the
// list and the rows drawn are the one list's length. English, Chinese,
// Japanese and German, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const iso = (d) => [d.getFullYear(), d.getMonth() + 1, d.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const to = new Date(2026, 8, 28);
const back = (i) => { const d = new Date(to); d.setDate(d.getDate() - i); return d; };

// A spent session, a single-line Claude Code session whose first request
// died, and a Cursor chat: the two silent ones carry no tokens and no active
// time, yet each is there to be resumed.
const spend = (input, output) => [{ agent: "claude", cwd: "/work/alpha", model: "claude-opus-4", input, output, cache_read: 0, cache_write: 0, cost: input * 4e-6 + output * 20e-6, priced: true }];
const sessions = [
  { agent: "claude", name: "Claude Code", icon: "claude", id: "a", cwd: "/work/alpha", title: "Worked in the range",
    start: back(2).toISOString(), last: back(2).toISOString(),
    input: 30000, output: 4000, cache_read: 0, cache_write: 0, cost: 0.2, priced: true,
    models: [{ model: "claude-opus-4", input: 30000, output: 4000, cost: 0.2, priced: true }], resume: "claude --resume a" },
  { agent: "claude", name: "Claude Code", icon: "claude", id: "solo", cwd: "/work/alpha", title: "Asked once, nothing came back",
    start: back(1).toISOString(), last: back(1).toISOString(),
    input: 0, output: 0, cache_read: 0, cache_write: 0, cost: 0, priced: true,
    models: [], resume: "claude --resume solo" },
  { agent: "cursor", name: "Cursor", icon: "cursor", id: "c1", cwd: "/work/alpha", title: "Cursor chat, no tokens kept",
    start: back(1).toISOString(), last: back(1).toISOString(),
    input: 0, output: 0, cache_read: 0, cache_write: 0, cost: 0, priced: true,
    models: [], resume: "cursor-agent --resume c1" },
];
const days = [{ date: iso(back(2)), usage: spend(30000, 4000), active: [] }];
// all three were at work in the range: the silent two spoke, so they count
const keys = ["claude:a", "claude:solo", "cursor:c1"];

function serve(lang) {
  const state = { agents: [
    { id: "claude", name: "Claude Code", path: "/t.json", fields: [] },
    { id: "cursor", name: "Cursor", path: "/t2.json", fields: [] },
  ], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/sessions/progress") return json({ indexing: false });
    if (url.pathname === "/api/sessions") return json({ sessions, dirs: ["/test/sessions"] });
    if (url.pathname === "/api/sessions/stats") return json({ from: days[0].date, to: iso(to), days, keys, agents: { claude: "Claude Code", cursor: "Cursor" } });
    if (url.pathname === "/api/sessions/overview") return json({
      count: keys.length, median: 34000, p90: 34000, days: [1],
      top: { tokens: [], cost: [], active: [] }, messages: [30], output: [0],
      shape: {
        messages: { edges: [1, 6, 16, 31, 61, 121], counts: [1], total: 3 },
        minutes: { edges: [1, 6, 16, 31, 61, 121], counts: [0], total: 0 },
        autonomy: { edges: [0, 1, 3, 6, 11, 21], counts: [1], total: 3 },
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

// three sessions: the KPI label, the note beside the list (the plural picks
// itself: German "3 Sitzungen", the others' plurals as in sessions-in-range)
const want = {
  en: { kpi: /^3\nSESSIONS/, note: /· 3 sessions ·/ },
  zh: { kpi: /^3\n会话数/, note: /· 3 个会话 ·/ },
  ja: { kpi: /^3\nセッション/, note: /· 3 件のセッション ·/ },
  de: { kpi: /^3\nSITZUNGEN/, note: /· 3 Sitzungen ·/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    test(`${engine} ${lang}: a session that spent nothing but said something stays in the list`, async (t) => {
      const w = want[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1000, height: 760 }, reducedMotion: "reduce", timezoneId: "Asia/Shanghai" });
      const page = await context.newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(10000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      // the whole of time, where the list is also the index of sessions
      await page.addInitScript(() => { localStorage.setItem("magpie.usageTab", "sessions"); localStorage.setItem("magpie.sessRange", "all"); });
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=usage");
      await page.locator("#sessGrid:not([hidden])").waitFor();
      const rows = page.locator("#sessList .row.sess");
      await rows.first().waitFor();

      // all three, the two that spent nothing among them
      assert.equal(await rows.count(), 3, "a session that spent nothing but spoke is not dropped from the list");
      const titles = (await rows.allInnerTexts()).join("\n");
      assert.match(titles, /Asked once, nothing came back/, "the single-line session is still there to be resumed");
      assert.match(titles, /Cursor chat, no tokens kept/, "the Cursor chat is still there to be resumed");
      // the KPI, the note under the list and the rows say the same number
      assert.match(await page.locator("#sessNote").innerText(), w.note, "the note counts the list");
      assert.match(await page.locator("#sessStats .kpi").first().innerText(), w.kpi, "the KPI counts the same sessions");
      assert.deepEqual(errors, []);
    });
  }
}
