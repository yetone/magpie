// Run with Node's test runner and Playwright on the module path; see README.md.
// The Routing page's requests at every window width (normalcoder_ on X,
// v0.1.4053):
// - a request still answering got its numbers (60 B, ≈ tokens, ago) after
//   the list's columns were measured, so they sat past the list's right
//   edge, cut off, in a column measured with none;
// - made wider, the Accounts and keys moved beside the requests and took a
//   third of the page from them, so a wide window showed each request on
//   more lines than a narrower one.
// Each row stays inside the list at every width, from wide to narrow and
// back, by request and by session; no metric is shown at one width and gone
// at a wider one, and a row is never taller in a wider window. Chromium and
// WebKit, the desktop window and the browser page, en and zh.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const acct = (n, who) => ({ id: "antigravity:" + n, provider: "antigravity", name: "Antigravity", who, plan: "Google AI Pro", kind: "account", model: "gemini-3.8-flash", known: true, used: 10 + n, icon: "generic", routing: "speed" });
const seats = [acct(1, "racooseoordssoiacl@example.com"), acct(2, "cecitimcmmarnicdosserenaos@example.com")];
const usage = [{ provider: "antigravity", model: "gemini-3.8-flash", in: 1000, out: 400, cache_read: 3000, cache_write: 1000 }];
const done = (i) => ({ id: 100 - i, seq: 100 - i, time: at(i), agent: "codex", session: "chat-" + (i % 2), model: "antigravity/gemini-3.8-flash", provider: "antigravity",
  effort: "medium", order: seats, done: true, status: 200, ms: 12345, ttft: 2345, tokens: 135400, out: 400, usage, priced: true, cost: 0.0123,
  tries: [{ id: seats[0].id, model: "gemini-3.8-flash", effort: "medium", start: at(i), done: true, status: 200, ms: 12345, ttft: 2345 }] });
// the reporter's: answering, its first bytes not come yet when the list is
// first drawn
const live = (extra) => ({ id: 200, seq: 200, time: now.toISOString(), agent: "codex", session: "chat-live", model: "antigravity/gemini-3.8-flash", provider: "antigravity",
  effort: "medium", order: seats, done: false, tries: [{ id: seats[0].id, model: "gemini-3.8-flash", effort: "medium", start: now.toISOString() }], ...extra });

function serve(lang, web, feed) {
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (d) => r.fulfill({ json: d });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/t", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      const routes = url.searchParams.has("wait") ? await new Promise((resolve) => { feed.next = resolve; }) : feed.routes;
      return json({ mine: true, now: new Date().toISOString(), seq: feed.seq, totals: { requests: feed.routes.length, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

// each row: what of it is past its own box or the list's (cut off), the
// metrics wholly in sight, and its height
const measure = (page) => page.evaluate(() => {
  const list = document.querySelector(".rt-reqs"), lb = list.getBoundingClientRect();
  return [...list.querySelectorAll(".rt-req, button.rt-session")].map((row) => {
    const rb = row.getBoundingClientRect(), edge = Math.min(rb.right, lb.right - parseFloat(getComputedStyle(list).paddingRight));
    const out = [...row.querySelectorAll(".at, .asked, .to, .meta > *, .nm, .summary, .summary > *, .cost")].filter((e) => {
      const b = e.getBoundingClientRect();
      return b.width && (b.right > edge + 1 || b.left < rb.left - 1);
    }).map((e) => `${e.className} "${e.textContent.slice(0, 16)}" ${Math.round(e.getBoundingClientRect().right - edge)}px past`);
    const shown = [...row.querySelectorAll(".rt-metric")].filter((e) => {
      const b = e.getBoundingClientRect();
      return b.width && b.left >= lb.left - 1 && b.right <= lb.right + 1;
    }).map((e) => e.className.replace("rt-metric ", ""));
    return { key: row.classList.contains("rt-session") ? "session " + row.textContent.slice(0, 12) : "request " + row.querySelector(".at").textContent, out, shown, height: Math.round(rb.height) };
  });
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: every request row fits its list at every width, and a wider window shows no less`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      // the reporter's list, the request in flight alone in it, and one
      // with finished requests too
      for (const [web, lone] of [[false, true], [false, false], [true, false]]) {
        const feed = { seq: 100, routes: [live({}), ...(lone ? [] : Array.from({ length: 6 }, (_, i) => done(i)))] };
        const page = await browser.newPage({ viewport: { width: 1000, height: 1000 }, reducedMotion: "reduce" });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, web, feed));
        await page.goto("http://magpie.test/?view=routing");
        await page.locator(".rt-req").nth(feed.routes.length - 1).waitFor();
        // its first bytes come in: the numbers a request in flight shows
        for (let i = 0; i < 100 && !feed.next; i++) await page.waitForTimeout(20);
        assert(feed.next, "live poll started");
        feed.seq = 101;
        feed.next([live({ seq: 101, bytes: 60, estTok: 1234, contentAt: Date.now() - 3000 })]);
        await page.locator(".rt-req .recv").first().waitFor();
        const settle = () => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(() => setTimeout(r, 150)))));
        const widths = [];
        for (let w = 2000; w >= 520; w -= 60) widths.push(w);
        for (const by of ["request", "session"]) {
          if (by === "session") {
            await page.locator(".rt-group-by button").nth(1).click();
            await page.locator("button.rt-session").first().waitFor();
          }
          // narrowed, then widened again: the same width shows the same
          const seen = new Map();
          for (const w of [...widths, ...widths.slice().reverse()]) {
            await page.setViewportSize({ width: w, height: 1000 });
            await settle();
            const rows = await measure(page);
            // the page's own width: the window's sidebar comes in at its
            // own width, and takes from the page
            rows.page = await page.locator("#rtMore").evaluate((e) => Math.round(e.getBoundingClientRect().width));
            const where = `${web ? "web" : "desktop"}${lone ? " alone" : ""} by ${by} at ${w}px`;
            for (const row of rows) assert.deepEqual(row.out, [], `${where}: ${row.key} inside the list`);
            if (seen.has(w)) assert.deepEqual(rows.map((r) => r.shown), seen.get(w).map((r) => r.shown), `${where}: the same metrics narrowed to it and widened back`);
            seen.set(w, rows);
          }
          // wider never shows less: every metric in sight at one width is in
          // sight at each wider one, and no row takes more lines
          const asc = [...seen.keys()].sort((a, b) => seen.get(a).page - seen.get(b).page || a - b);
          for (let i = 1; i < asc.length; i++) {
            const narrow = seen.get(asc[i - 1]), wide = seen.get(asc[i]);
            if (wide.page === narrow.page) continue;
            assert.equal(wide.length, narrow.length);
            narrow.forEach((row, j) => {
              const at = `${web ? "web" : "desktop"} by ${by}, ${row.key}, a page of ${narrow.page}px → ${wide.page}px (window ${asc[i - 1]} → ${asc[i]})`;
              for (const m of row.shown) assert(wide[j].shown.includes(m), `${at}: ${m} still shown (${wide[j].shown})`);
              assert(wide[j].height <= row.height + 1, `${at}: no taller (${row.height} → ${wide[j].height})`);
            });
          }
          // every metric picked, in the live row's numbers too, is shown at
          // the narrowest width
          const least = seen.get(asc[0]);
          if (!lone) {
            const doneRow = least.find((r) => r.key.startsWith("request") && r.shown.includes("duration"));
            assert(doneRow, "a finished request's row");
            assert.deepEqual(doneRow.shown, ["duration", "ttft", "tokens", "cache-hit", "speed", "cost-metric"]);
          }
          assert(least.some((r) => r.shown.filter((m) => m === "recv").length === 3), `the request in flight's numbers: ${JSON.stringify(least.map((r) => r.shown))}`);
        }
        assert.deepEqual(errors, [], "no page error");
        feed.next?.([]);
        await page.close();
      }
    });
  }
}
