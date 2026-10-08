// Run with Node's test runner and Playwright on the module path; see README.md.
// The Sessions tab's list is every session the filters keep, drawn a page at a
// time: a filter click or a keystroke in Search draws the first page and no
// more, "Show N more" appends the next, and the note under the list says how
// many of how many are drawn. Without the pages a history of thousands was
// rebuilt whole on every keystroke (~300 ms a keystroke on a fast Mac, and
// worse on Windows: yetone, #1019). The data stays complete, so the count
// beside the list and the list still agree. English and Chinese, Chromium and
// WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const to = new Date(2026, 8, 28);
const iso = (d) => [d.getFullYear(), d.getMonth() + 1, d.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const N = 2600; // past two pages of 200, so a third page is left over
const DAYS = 30;

// N sessions, newest first, one every few minutes; every 25th names another
// model, so a filter has something to narrow to
const sessions = Array.from({ length: N }, (_, i) => ({
  agent: "claude", name: "Claude Code", icon: "claude", id: "s" + i,
  cwd: i % 2 ? "/work/alpha" : "/work/beta", title: i === 0 ? "Fix the session list" : "Session " + i,
  start: new Date(to.getTime() - (i + 30) * 60e3).toISOString(),
  last: new Date(to.getTime() - (i + 1) * 60e3).toISOString(),
  input: 1000 + i, output: 100, cache_read: 0, cache_write: 0, cost: 0.01, priced: true,
  models: [{ model: i % 25 === 0 ? "claude-opus-4" : "claude-sonnet-5", input: 1000 + i, output: 100, cost: 0.01, priced: true }],
  resume: "claude --resume s" + i,
}));
const days = Array.from({ length: DAYS }, (_, i) => {
  const d = new Date(to); d.setDate(d.getDate() - (DAYS - 1 - i));
  return { date: iso(d), usage: [{ agent: "claude", cwd: "/work/alpha", model: "claude-opus-4", input: 30000, output: 4000, cache_read: 0, cache_write: 0, cost: 1.5, priced: true }], active: [] };
});

function serve(lang) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/t.json", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/sessions/progress") return json({ indexing: false });
    if (url.pathname === "/api/sessions") return json({ sessions, dirs: ["/test/sessions"] });
    if (url.pathname === "/api/sessions/stats") return json({ from: days[0].date, to: iso(to), days, agents: { claude: "Claude Code" } });
    if (url.pathname === "/api/sessions/overview") return json({
      count: N, median: 120000, p90: 880000, days: days.map(() => 1),
      top: { tokens: [], cost: [], active: [] }, messages: days.map(() => 30), output: days.map(() => 0),
      shape: {
        messages: { edges: [1, 6, 16, 31, 61, 121], counts: [3, 10, 14, 9, 4, 2], total: N },
        minutes: { edges: [1, 6, 16, 31, 61, 121], counts: [20, 12, 6, 3, 1, 0], total: N },
        autonomy: { edges: [0, 1, 3, 6, 11, 21], counts: [5, 7, 10, 12, 6, 2], total: N },
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

const words = {
  en: {
    more: /^Show 200 more$/,
    // The note counts the list the filters kept, not every session on the
    // computer: a page of them reads "showing 200 of 2600", and a list drawn
    // whole reads its own length — "1 session" for one, "N sessions" for
    // more. The number picks the word, so the regex does too: `sessions?`
    // would let "1 sessions" through.
    showing: (drawn, total) => new RegExp(`showing ${drawn} of ${total}`),
    whole: (n) => new RegExp(`· ${n} ${n === 1 ? "session" : "sessions"} ·`),
    none: "No session matches.",
  },
  zh: {
    more: /^显示其余 200 个$/,
    showing: (drawn, total) => new RegExp(`显示 ${total} 个中的 ${drawn} 个`),
    // Chinese has no plural: one word either way
    whole: (n) => new RegExp(`· ${n} 个会话 ·`),
    none: "没有匹配的会话。",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: the Sessions list is drawn in pages over a long history`, async (t) => {
      const w = words[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 760 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(10000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      // the Sessions tab, over all ranges, as it was left
      await page.addInitScript(() => { localStorage.setItem("magpie.usageTab", "sessions"); localStorage.setItem("magpie.sessRange", "all"); });
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=usage");
      await page.locator("#sessGrid:not([hidden])").waitFor();
      const rows = page.locator("#sessList .row.sess");
      const more = page.locator("#sessList .sess-page-more");

      // the first page and no more: 200 rows of the 2600 the list holds
      assert.equal(await rows.count(), 200, "the first page");
      assert.equal(await more.count(), 1, "a way to the next page");
      assert.match(await more.innerText(), w.more);
      assert.match(await page.locator("#sessNote").innerText(), w.showing(200, N));

      // The next page appends 200 more, and the note follows. The button is
      // under 200 rows, so it is pressed through the DOM: what this test is
      // about is the pages, not bringing a control into sight (click-scroll
      // and reader.cjs cover that).
      await more.evaluate((b) => b.click());
      assert.equal(await rows.count(), 400, "the second page, on top of the first");
      assert.match(await page.locator("#sessNote").innerText(), w.showing(400, N));
      // and the rows already drawn are the same elements, not rebuilt
      const first = await rows.first().getAttribute("data-key");
      await more.evaluate((b) => b.click());
      assert.equal(await rows.count(), 600, "the third page");
      assert.equal(await rows.first().getAttribute("data-key"), first, "the rows above stayed where they were");
      assert.match(await page.locator("#sessNote").innerText(), w.showing(600, N));

      // a keystroke in Search is a new list: it starts at the first page again
      // and, over 2600 sessions, does not take the time the whole list would.
      // 1111 of them name "Session 1" ("1", "1x", "1xx", "1xxx"), so the note
      // counts that list — 200 of 1111, not of the 2600 on the computer
      const q = page.locator("#sessQ");
      const at = Date.now();
      await q.fill("Session 1");
      const took = Date.now() - at;
      assert(await rows.count() <= 200, "a search draws a page, not the history");
      assert.equal(await rows.count(), 200, "a page of the matches");
      assert.match(await page.locator("#sessNote").innerText(), w.showing(200, 1111));
      assert(took < 250, `a keystroke over ${N} sessions took ${took}ms`);

      // a filter that keeps nothing says so, and the note counts nothing
      await q.fill("nothing matches this at all");
      await page.locator("#sessList .empty-state").waitFor();
      assert.equal(await rows.count(), 0);
      assert.match(await page.locator("#sessNote").innerText(), w.whole(0));
      await q.press("Escape");
      await page.locator("#sessList .row.sess").first().waitFor();
      assert.equal(await rows.count(), 200, "clearing the search draws the first page again");
      assert.match(await page.locator("#sessNote").innerText(), w.showing(200, N));

      // A search that matches fewer than a page holds them all: the note says
      // "1 session", not "1 of 2600" — there is no "Show more" for the other
      // 2599, and saying so would send the reader looking for one. The whole
      // list is drawn, so it is the length and not "1 of 1" (yetone, #1019).
      await q.fill("Fix the session list");
      await page.locator("#sessList .row.sess").first().waitFor();
      assert.equal(await rows.count(), 1, "the one match");
      assert.equal(await more.count(), 0, "a list shorter than a page has no way to more");
      assert.match(await page.locator("#sessNote").innerText(), w.whole(1));
      assert.doesNotMatch(await page.locator("#sessNote").innerText(), new RegExp(String(N)), "the note doesn't count the whole history");

      assert.deepEqual(errors, []);
    });
  }
}
