// Run with Node's test runner and Playwright on the module path; see README.md.
// In the Routing page's Requests list a row's reasoning ("· high") sits in
// the flow of its row: never drawn over the time taken, first token and
// tokens beside it, at a wide window and a narrow one (#273: at a window
// of about 1000px, two columns, "7.1 秒" and "high" read as "7·1higȟ").
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const at = (i) => new Date(now.getTime() - (i + 1) * 9e3).toISOString();
const seat = { id: "claude", provider: "claude", name: "Claude", who: "ann@example.com", kind: "account", agent: "claude", model: "claude-opus-4-5" };
// newest first: one under way, then answered ones with all their numbers,
// one that took two tries, and one whose effort was picked for the turn
const routes = [0, 1, 2, 3, 4, 5, 6].map((i) => {
  const done = i > 0;
  const tries = [{ id: seat.id, model: "claude-opus-4-5", start: at(i), effort: "high", ...(i === 6 ? { picked: true } : {}), ...(done ? { done: true, status: 200, ms: 7100 + i * 3100 } : {}) }];
  if (i === 5) tries.unshift({ id: seat.id, model: "claude-opus-4-5", start: at(i), effort: "high", done: true, status: 429, fail: "rate_limited", ms: 300 });
  return {
    id: 100 - i, seq: 100 - i, time: at(i), agent: "claude", model: "claude/claude-opus-4-5-20251101", provider: "claude", effort: "high",
    order: [seat], tries, ...(done ? { done: true, status: 200, ms: 7100 + i * 3100, ttft: 4600, tokens: 141400 + i * 1000 } : {}),
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
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 100, totals: { requests: routes.length, rerouted: 1, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// in the page: every run of text in a row as drawn, its box cut to what
// the boxes around it that clip let show, and the effort's own box
function measure(rows) {
  const clipped = (row, el, box) => {
    let { left, right, top, bottom } = box;
    for (let e = el; e && e !== row.parentElement; e = e.parentElement) {
      if (getComputedStyle(e).overflowX === "visible") continue;
      const c = e.getBoundingClientRect();
      left = Math.max(left, c.left); right = Math.min(right, c.right); top = Math.max(top, c.top); bottom = Math.min(bottom, c.bottom);
    }
    return { left, right, top, bottom };
  };
  return rows.map((row) => {
    const texts = [];
    const walk = document.createTreeWalker(row, NodeFilter.SHOW_TEXT);
    for (let n = walk.nextNode(); n; n = walk.nextNode()) {
      if (!n.textContent.trim()) continue;
      const r = document.createRange();
      r.selectNodeContents(n);
      const b = clipped(row, n.parentElement, r.getBoundingClientRect());
      if (b.right - b.left > 0.5 && b.bottom - b.top > 0.5) texts.push({ text: n.textContent, cls: n.parentElement.closest(".ef") ? "ef" : n.parentElement.className, ...b });
    }
    const ef = row.querySelector(".ef"), e = ef.getBoundingClientRect(), shown = clipped(row, ef, e), rb = row.getBoundingClientRect();
    // the effort's "· " comes from ::before, inside its box: the box stands for it
    texts.push({ text: "· " + ef.textContent, cls: "ef box", ...shown });
    return {
      texts, row: { left: rb.left, right: rb.right },
      ef: { text: ef.textContent, full: ef.scrollWidth <= ef.clientWidth + 0.5 && shown.right - shown.left >= e.width - 0.5, left: e.left, right: e.right },
    };
  });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const width of [1440, 1000, 480]) {
      test(`${engine} ${lang} ${width}px: a request's reasoning sits beside its numbers, not over them`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.locator(".rt-reqs").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-effort-row.png`) });
          }
          await browser.close();
        });
        await page.goto("http://magpie.test/?view=routing");
        await page.locator(".rt-req").nth(routes.length - 1).waitFor();
        await page.waitForTimeout(200);

        const rows = await page.locator(".rt-req").evaluateAll(measure);
        assert.equal(rows.length, routes.length);
        for (const [i, r] of rows.entries()) {
          assert.equal(r.ef.text, "high");
          assert(r.ef.full, `row ${i}: the effort is cut ${JSON.stringify(r.ef)}`);
          for (const x of r.texts) assert(x.left >= r.row.left - 0.5 && x.right <= r.row.right + 0.5, `row ${i}: "${x.text}" is outside its row`);
          for (let a = 0; a < r.texts.length; a++) for (let b = a + 1; b < r.texts.length; b++) {
            const p = r.texts[a], q = r.texts[b];
            if (p.cls === "ef box" && q.cls === "ef" || q.cls === "ef box" && /^ef/.test(p.cls)) continue; // the effort and its own box
            const w = Math.min(p.right, q.right) - Math.max(p.left, q.left), h = Math.min(p.bottom, q.bottom) - Math.max(p.top, q.top);
            assert(!(w > 0.5 && h > 0.5), `row ${i}: "${p.text}" (${p.cls}) is drawn over "${q.text}" (${q.cls}) by ${w.toFixed(1)}px`);
          }
        }
        // the answered rows keep all their numbers
        const meta = await page.locator(".rt-req").nth(1).locator(".meta > span").first().textContent();
        assert.match(meta, lang === "zh" ? /秒 · 首字 4\.6 秒 · 142\.4k token$/ : /s · TTFT 4\.6 s · 142\.4k tokens$/);
        assert.equal(await page.locator(".rt-req").nth(1).locator(".meta .cost").textContent(), "—");
        // no stripe down a row's side
        assert.equal(await page.locator(".rt-req").first().evaluate((e) => getComputedStyle(e).borderLeftColor === getComputedStyle(e).borderTopColor), true);
        assert.deepEqual(errors, []);
      });
    }
  }
}
