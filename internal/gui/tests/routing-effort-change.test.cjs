// Run with Node's test runner and Playwright on the module path; see README.md.
// In the Routing page's Requests list a request whose reasoning was changed
// on the way ("· medium → low", "· medium → xhigh") keeps it in its own
// column: never drawn over the time taken, first token and tokens beside it,
// at any width of the window (#435, azir12345: 文字重叠 — "medium → low" and
// "15 秒 · 首字 14 秒 · 6.9k token" were drawn on top of each other, while
// rows at "· low" were fine: the rows share their columns, the numbers' one
// is as wide as the longest numbers in the list, and the reasoning, which
// would not shrink, ran out of what was left). Where there is no room, where
// it went gives way first, then the level asked for, each cut with an
// ellipsis inside its own box; the level sent stays whole. Chromium and
// WebKit, English and Chinese, and with the styles as Safari 15 reads them.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const at = (i) => new Date(now.getTime() - (i + 1) * 9e3).toISOString();
const seat = { id: "codex:a", provider: "codex", name: "Codex", who: "ann@example.com", kind: "account", agent: "codex", model: "gpt-6.1-sol" };
// newest first, as in the report: one under way at a changed effort, a
// title call at a changed effort, a long one changed up, plain "low" ones,
// and further down one whose numbers run long (three tries, minutes, a
// million tokens): the numbers' column is as wide as its widest, and where
// it went is left the rest, which can be next to nothing
const rows = [
  { asked: "medium", sent: "xhigh", done: false },
  { asked: "medium", sent: "low", kind: "title", ms: 15000, ttft: 14000, tokens: 6900 },
  { asked: "medium", sent: "xhigh", ms: 157000, ttft: 24000, tokens: 139700 },
  { asked: "low", sent: "low", ms: 2500, ttft: 1200, tokens: 5800 },
  { asked: "medium", sent: "xhigh", picked: true, ms: 39000, ttft: 3100, tokens: 17700 },
  { asked: "medium", sent: "high", tries: 3, ms: 457000, ttft: 64000, tokens: 1234567 },
];
const routes = rows.map((x, i) => {
  const done = x.done !== false;
  const tries = [{ id: seat.id, model: seat.model, start: at(i), effort: x.sent, ...(x.picked ? { picked: true } : {}), ...(done ? { done: true, status: 200, ms: x.ms } : {}) }];
  for (let n = 1; n < (x.tries || 1); n++) tries.unshift({ id: seat.id, model: seat.model, start: at(i), effort: x.sent, done: true, status: 429, fail: "rate_limited", ms: 300 });
  return {
    id: 100 - i, seq: 100 - i, time: at(i), agent: "codex", model: "gpt-6.1-sol", provider: "codex", effort: x.asked, ...(x.kind ? { kind: x.kind } : {}),
    order: [seat], tries, ...(done ? { done: true, status: 200, ms: x.ms, ttft: x.ttft, tokens: x.tokens } : {}),
  };
});
const efText = (x) => x.asked !== x.sent ? `${x.asked} → ${x.sent}` : x.sent;

// Safari 15 (macOS 12's WebKit) has neither subgrid nor container queries:
// each row is a grid of its own, sized by its own numbers, and the
// narrower layouts follow the window. The styles are served as it reads them
const safari15 = (css) => css
  .replace(/@supports not \(grid-template-columns: subgrid\)/g, "@supports (display: grid)")
  .replace(/grid-template-columns: subgrid/g, "grid-template-columns: x-subgrid")
  .replace(/@container[^{]*\{/g, "@media (max-width: 0px) {");

function serve(lang, old) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "dark" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"dark",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 100, totals: { requests: routes.length, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file);
    await route.fulfill({ body: old && contentType === "text/css" ? safari15(body.toString()) : body, contentType });
  };
}

// in the page: every run of text in a row as drawn, its box cut to what the
// boxes around it that clip let show, and the effort's and its cell's boxes
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
    let k = 0;
    for (let n = walk.nextNode(); n; n = walk.nextNode(), k++) {
      if (!n.textContent.trim()) continue;
      const r = document.createRange();
      r.selectNodeContents(n);
      for (const rect of r.getClientRects()) {
        const b = clipped(row, n.parentElement, rect);
        if (b.right - b.left > 0.5 && b.bottom - b.top > 0.5) texts.push({ k, text: n.textContent, cls: n.parentElement.closest(".ef") ? "ef" : n.parentElement.className, ...b });
      }
    }
    const ef = row.querySelector(".ef"), to = row.querySelector(".to").getBoundingClientRect(), rb = row.getBoundingClientRect();
    const shown = clipped(row, ef, ef.getBoundingClientRect());
    // the effort's "· " comes from ::before, inside its box: the box stands for it
    texts.push({ text: "· " + ef.textContent, cls: "ef box", ...shown });
    return { texts, ef: { text: ef.textContent, ...shown }, to: { left: to.left, right: to.right }, row: { left: rb.left, right: rb.right } };
  });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) for (const old of [false, true]) {
    test(`${engine} ${lang}${old ? " (Safari 15's styles)" : ""}: a changed reasoning never sits over the numbers beside it`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const width of [1440, 1340, 1200, 1000, 900, 760, 680, 600, 520, 480, 400]) {
        const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce", colorScheme: "dark" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, old));
        await page.goto("http://magpie.test/?view=routing");
        await page.locator(".rt-req").nth(routes.length - 1).waitFor();
        await page.waitForTimeout(150);
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.locator(".rt-reqs").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}${old ? "-old" : ""}-${width}-effort-change.png`) });
        }
        const got = await page.locator(".rt-req").evaluateAll(measure);
        assert.equal(got.length, routes.length);
        for (const [i, r] of got.entries()) {
          const where = `${width}px row ${i}`;
          assert.equal(r.ef.text, efText(rows[i]), where);
          // the effort is drawn within where it went, and so within its row
          assert(r.ef.left >= r.to.left - 0.5 && r.ef.right <= r.to.right + 0.5, `${where}: the effort runs out of its cell ${JSON.stringify({ ef: r.ef, to: r.to })}`);
          for (const x of r.texts) assert(x.left >= r.row.left - 0.5 && x.right <= r.row.right + 0.5, `${where}: "${x.text}" is outside its row`);
          for (let a = 0; a < r.texts.length; a++) for (let b = a + 1; b < r.texts.length; b++) {
            const p = r.texts[a], q = r.texts[b];
            if (p.k !== undefined && p.k === q.k) continue; // one run's lines
            if (p.cls.startsWith("ef") && q.cls.startsWith("ef")) continue; // the effort and its own box
            const w = Math.min(p.right, q.right) - Math.max(p.left, q.left), h = Math.min(p.bottom, q.bottom) - Math.max(p.top, q.top);
            assert(!(w > 0.5 && h > 0.5), `${where}: "${p.text}" (${p.cls}) is drawn over "${q.text}" (${q.cls}) by ${w.toFixed(1)}px`);
          }
        }
        // the numbers stay whole
        const meta = await page.locator(".rt-req").nth(1).locator(".meta > span").first().textContent();
        assert.match(meta, lang === "zh" ? /^15 秒 · 首字 14 秒 · 6\.9k token$/ : /^15 s · TTFT 14 s · 6\.9k tokens$/, `${width}px`);
        // where it went and the level asked for give way first: the level
        // sent is whole in every row, at every width
        const cut = await page.locator(".rt-req .ef").evaluateAll((es) => es.map((e) => {
          const now = e.querySelector(".now") || e;
          return now && now.scrollWidth <= now.clientWidth + 0.5 && e.getBoundingClientRect().right >= now.getBoundingClientRect().right - 0.5 ? "" : e.textContent;
        }).filter(Boolean));
        assert.deepEqual(cut, [], `${width}px: the level sent is cut`);
        // and at a width with room, a changed one is whole too
        if (width >= 1200) assert(await page.locator(".rt-req").nth(1).locator(".ef").evaluate((e) => e.scrollWidth <= e.clientWidth + 0.5), `${width}px: "medium → low" is cut`);
        // no stripe down a row's side
        assert.equal(await page.locator(".rt-req").first().evaluate((e) => getComputedStyle(e).borderLeftColor === getComputedStyle(e).borderTopColor), true);
        assert.deepEqual(errors, []);
        await context.close();
      }
    });
  }
}
