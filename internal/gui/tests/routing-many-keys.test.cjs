// Run with Node's test runner and Playwright on the module path; see README.md.
// The Routing page stays light with hundreds of keys in a group (#3164:
// several hundred gpt-5.6-sol keys in one group, "进到这个分组的路由页就基本
// 没法流畅操作了"). Each request names every key it weighed, and the page
// read every one of them again and again: a sort of a route's keys looked
// each key up in its provider's whole list for every comparison, every
// row of the stage went through its route's whole list each second, the
// trace's times were parsed again for each, and drawing the wires read
// each row's box after the wire before it was drawn, so an open fold laid
// the stage out once a row. Here 500 keys of one provider and 300 of
// another serve a group while requests come, folded and opened, and what
// the page does is counted: no key lookup through a whole list, times
// parsed a bounded number of times, and no box read after a wire drawn
// in the same pass. And no wire is one cubic thousands of pixels tall: WebKit
// dashes a cubic at a cost that grows faster than its length, and the
// flowing wire to a fold hundreds of rows from magpie took ~75 ms of each
// frame. And only the keys near sight are in the list: each update laid
// out, read and moved all 800, longer than a frame in WebKit. The rest are
// gaps as tall as they are, their wires still drawn to where they would
// be, and scrolled to, the last key is there with its wire ending at it.
// English, Chinese and German; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const iso = (ms) => new Date(ms).toISOString();
const M = "gpt-5.6-sol";
const pools = [["solpool", "SolPool", 500], ["solpool2", "SolPool2", 300]];
const key = (p, i) => `${p}#k${String(i).padStart(3, "0")}`;
const order = pools.flatMap(([p, name, n]) => Array.from({ length: n }, (_, i) => ({ id: key(p, i), provider: p, name, who: `sk-proj-…${String(i).padStart(4, "0")}`, kind: "key", model: M })));
const group = { id: "sol", name: "Sol", routing: "", members: pools.map(([p]) => `${p}/${M}`) };
const providers = pools.map(([p, name, n]) => ({ id: p, name, keyList: Array.from({ length: n }, (_, i) => ({ id: `k${String(i).padStart(3, "0")}` })) }));

// a request to the group, answered by one key after another refused it,
// or still under way at it
function req(id, live) {
  const t0 = Date.now() - 500, a = order[(id * 7) % order.length], b = order[(id * 7 + 1) % order.length];
  const tries = [
    { id: b.id, model: M, start: iso(t0), done: true, status: 429, fail: "rate", ms: 100 },
    { id: a.id, model: M, start: iso(t0 + 100), done: !live, ms: 300, ...(live ? {} : { status: 200 }) },
  ];
  return { id, seq: id, time: iso(t0), agent: "codex", model: "sol", provider: a.provider, group, order: order.map((w) => ({ ...w })), tries,
    done: !live, status: live ? 0 : 200, ms: live ? 0 : 400, tokens: live ? 0 : 1200 };
}

function serve(lang, feed) {
  const first = Array.from({ length: 30 }, (_, i) => req(1 + i, false));
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      const trace = (routes) => json({ mine: true, now: new Date().toISOString(), seq: routes.length ? routes[routes.length - 1].seq : Number(url.searchParams.get("after")), totals: { requests: 0, rerouted: 0, errors: 0 }, routes });
      if (!url.searchParams.get("wait")) return trace(first);
      while (!feed.q.length && !feed.closed) await new Promise((r) => setTimeout(r, 20));
      return trace(feed.q.splice(0)).catch(() => {});
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers, excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// what the page does, counted: lookups through a list, times parsed, and
// the times a box read laid the stage out again after a wire was drawn
const init = () => {
  const n = window.__n = { find: 0, dates: 0, thrash: 0, boxes: 0 };
  const find = Array.prototype.findIndex;
  Array.prototype.findIndex = function (...a) { if (this.length > 100) n.find++; return find.apply(this, a); };
  const D = Date;
  window.Date = new Proxy(D, { construct(T, a) { if (typeof a[0] === "string") n.dates++; return Reflect.construct(T, a); } });
  // a wire drawn leaves the stage to lay out again: the next box read
  // lays it out then and there
  let drew = false;
  const set = Element.prototype.setAttribute;
  Element.prototype.setAttribute = function (k, v) { if (k === "d" && this instanceof SVGPathElement) drew = true; return set.call(this, k, v); };
  const box = Element.prototype.getBoundingClientRect;
  Element.prototype.getBoundingClientRect = function () { n.boxes++; if (drew) n.thrash++; drew = false; return box.call(this); };
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "de"]) {
    test(`${engine} ${lang}: hundreds of keys in a group are read once, not each against all`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1200, height: 800 } });
      await context.addInitScript(init);
      const page = await context.newPage();
      page.setDefaultTimeout(15000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const feed = { q: [], closed: false };
      await page.route("**/*", serve(lang, feed));
      t.after(async () => { feed.closed = true; await browser.close(); });
      await page.goto("http://magpie.test/?view=routing");
      await page.locator("#view-routing .rt-fold").first().waitFor();
      await page.waitForTimeout(1500);

      let id = 100;
      const pump = setInterval(() => { feed.q.push(req(id, true), req(id - 1, false)); id++; }, 300);
      t.after(() => clearInterval(pump));
      const look = async () => {
        const before = await page.evaluate(() => ({ ...window.__n }));
        await page.waitForTimeout(3000);
        const after = await page.evaluate(() => ({ ...window.__n }));
        return Object.fromEntries(Object.keys(after).map((k) => [k, after[k] - before[k]]));
      };
      const folded = await look();
      // both folds opened, each key a row of its own on the stage
      while (await page.evaluate(() => { const f = document.querySelector("#view-routing .rt-fold:not(.open)"); f?.click(); return !!f; }));
      await page.waitForFunction(() => document.querySelectorAll("#view-routing .rt-wires path[d]").length >= 800);
      const opened = await look();
      clearInterval(pump);
      // the tallest wire, and the tallest cubic any wire is drawn with
      const tall = await page.evaluate(() => {
        let wire = 0, cubic = 0;
        for (const p of document.querySelectorAll("#view-routing .rt-wires path[d]")) {
          const n = (p.getAttribute("d").match(/-?[\d.]+(?:e-?\d+)?|[A-Za-z]/g) || []);
          let cmd = "", ys = [], y = 0;
          for (let i = 0; i < n.length;) {
            if (/[A-Za-z]/.test(n[i])) { cmd = n[i++]; continue; }
            const k = { M: 2, L: 2, Q: 4, C: 6 }[cmd] || 2, a = n.slice(i, i + k).map(Number);
            i += k;
            const pts = [y, ...a.filter((_, j) => j % 2)];
            if (cmd === "C") cubic = Math.max(cubic, Math.max(...pts) - Math.min(...pts));
            y = pts[pts.length - 1];
            ys.push(...pts);
          }
          wire = Math.max(wire, Math.max(...ys) - Math.min(...ys));
        }
        return { wire: Math.round(wire), cubic: Math.round(cubic) };
      });
      // a key far below the fold of the window is not in the list, and one
      // in the list not in sight is not drawn and is as tall as the keys
      // drawn: drawn, WebKit painted all 800 again on each frame a magpie
      // flew, ~40 frames a second
      const KEYS = "#view-routing .rt-accts > li:not(.rt-fold):not(.rt-sub):not(.rt-gap):not(.idle)";
      const rowsNow = () => page.evaluate((KEYS) => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(() => {
        const all = [...document.querySelectorAll(KEYS)], keys = all.filter((li) => !li.classList.contains("hit"));
        const drawn = (li) => li.firstElementChild.checkVisibility({ contentVisibilityAuto: true });
        const mid = (li) => { const r = li.getBoundingClientRect(); return Math.abs((r.top + r.bottom) / 2 - innerHeight / 2); };
        const seen = keys.reduce((a, li) => (mid(li) < mid(a) ? li : a));
        const h = (li) => Math.round(li.getBoundingClientRect().height);
        // the wire to each key in sight ends at the middle of its left edge
        const st = document.querySelector("#view-routing .rt-stage").getBoundingClientRect();
        const ends = [...document.querySelectorAll("#view-routing .rt-wires path[d]")].map((p) => { const n = p.getAttribute("d").match(/-?[\d.]+(?:e-?\d+)?/g).map(Number); return [n[n.length - 2] + st.left, n[n.length - 1] + st.top]; });
        const inSight = keys.filter((li) => { const r = li.getBoundingClientRect(); return r.bottom > 0 && r.top < innerHeight; });
        const unwired = inSight.filter((li) => { const r = li.getBoundingClientRect(); return !ends.some(([x, y]) => Math.abs(x - r.left) < 1.5 && Math.abs(y - (r.top + r.bottom) / 2) < 1.5); }).map((li) => li.title);
        done({ shown: mid(seen) < innerHeight / 2 && drawn(seen), notDrawn: keys.filter((li) => !drawn(li)).length, listed: keys.length, inSight: inSight.length, unwired, wires: ends.length,
          last: all.some((li) => li.title === "solpool2#k299" && drawn(li)), heights: [...new Set(keys.map(h))] });
      }))), KEYS);
      const rows = await rowsNow();
      t.diagnostic(`folded ${JSON.stringify(folded)}, open ${JSON.stringify(opened)}, wires ${JSON.stringify(tall)}, rows ${JSON.stringify(rows)}`);
      assert.equal(rows.shown, true, "a key in sight is drawn");
      assert.equal(rows.last, false, "the last of 800 keys, out of sight, is not drawn");
      assert(rows.listed < 120, `${rows.listed} of 800 keys in the list`);
      assert.equal(rows.heights.length, 1, `keys drawn and not are one height: ${rows.heights}`);
      assert(rows.wires >= 800, `${rows.wires} wires: each key has one, in the list or not`);
      assert.deepEqual(rows.unwired, [], "a wire ends at each key in sight");
      // scrolled to the foot, the last key is there, drawn, with its wire
      // by the wheel, as a reader would: on until the last key is in the
      // list, then to it. The wheel turns over the keys: at the page's foot
      // the requests list sits below them, and a wheel over it scrolls it
      // and stops there (overscroll-behavior: contain), as it should
      const overKeys = () => page.evaluate(() => { const r = document.querySelector("#view-routing .rt-accts").getBoundingClientRect(); return [r.left + Math.min(40, r.width / 2), (Math.max(60, r.top) + Math.min(innerHeight - 20, r.bottom)) / 2]; }).then(([x, y]) => page.mouse.move(x, y));
      for (let k = 0; k < 80; k++) {
        await overKeys();
        const at = await page.evaluate(() => [...document.querySelectorAll("#view-routing .rt-accts > li")].find((li) => li.title === "solpool2#k299")?.getBoundingClientRect().top);
        if (at !== undefined && Math.abs(at - 400) < 150) break;
        await page.mouse.wheel(0, at === undefined ? 3000 : Math.max(-3000, Math.min(3000, at - 400)));
        await page.waitForTimeout(at === undefined ? 50 : 300);
      }
      await page.waitForTimeout(300);
      const foot = await rowsNow();
      t.diagnostic(`at the foot ${JSON.stringify({ ...foot, unwired: foot.unwired.length })}`);
      assert.equal(foot.last, true, "scrolled to, the last key is drawn");
      assert(foot.inSight > 5, `${foot.inSight} keys in sight at the foot`);
      assert.deepEqual(foot.unwired, [], "a wire ends at each key in sight at the foot");
      // the second fold, hundreds of keys down, closed and opened again
      // where it is clicked stays there, the list made again around it
      const fold2 = () => page.evaluate(() => { const f = document.querySelectorAll("#view-routing .rt-fold")[1]; const r = f?.getBoundingClientRect(); return r && { top: r.top, x: r.left + 20, y: (r.top + r.bottom) / 2 }; });
      for (let k = 0; k < 80; k++) {
        const f = await fold2();
        if (f && Math.abs(f.top - 300) < 150) break;
        await overKeys();
        await page.mouse.wheel(0, !f ? -3000 : Math.max(-3000, Math.min(3000, f.top - 300)));
        await page.waitForTimeout(!f ? 50 : 300);
      }
      await page.waitForTimeout(300);
      for (const was of ["open", "closed"]) {
        const f = await fold2();
        await page.mouse.click(f.x, f.y);
        await page.waitForTimeout(800);
        const after = await fold2();
        assert(after && Math.abs(after.top - f.top) < 2, `the second fold, ${was} and clicked, moved from ${f.top} to ${after?.top}`);
      }
      for (const [name, n] of [["folded", folded], ["open", opened]]) {
        // a key's place is looked up in an index of its provider's list:
        // before, a sort of 800 keys looked through the 500 for each of
        // ~8000 comparisons, ~50k lookups a second
        assert(n.find < 50, `${name}: ${n.find} lookups through a list of hundreds in 3s`);
        // each time the trace says is parsed once: ~1M a second before
        assert(n.dates < 20000, `${name}: ${n.dates} times parsed in 3s`);
        // every box is read before a wire is drawn: before, each of the
        // 800 rows' boxes was read after the wire above it was drawn, so
        // the stage was laid out again for each (~7000 a second). A few
        // dozen a second are the stage drawn anew as requests come.
        assert(n.thrash < 400, `${name}: the stage laid out again ${n.thrash} times for a wire drawn, in 3s`);
      }
      assert(opened.boxes > 100, `the open stage was laid out (${opened.boxes} boxes read)`);
      // a wire to a fold far from magpie runs thousands of pixels, in
      // pieces of its curve: before, it was one cubic as tall as itself
      assert(tall.wire > 5000, `the tallest wire is ${tall.wire}px`);
      assert(tall.cubic <= 2000, `a wire is drawn with a cubic ${tall.cubic}px tall`);
      assert.deepEqual(errors, []);
    });
  }
}
