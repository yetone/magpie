// Run with Node's test runner and Playwright on the module path; see README.md.
// Routing, live: nothing in the context window moves or flashes as a
// session's requests stream in (dumplings on Discord: each new request made
// the card jitter and flash, and the prompt cache read "—命中", the dash
// against the word, over an empty bar). Three causes, each held here on
// every frame while requests come in (under way without their prompt, with
// it, done):
//   - the story above the card is told in fewer lines while a request is
//     under way, and its head gets View usage and Replay once it is done,
//     so the card went up and back down: it stays where it is;
//   - the Live/Counted pill is narrower than Counted, so the model beside
//     it moved: the pill keeps its width;
//   - the cache went to "—" and an empty bar while under way: the last
//     request's figure stays, dimmed and said to be the last one's, until
//     the new one's comes.
// A cache the vendor didn't report is a dash set apart from its word, the
// reason said, and no bar, in its same place. Every GUI language, Chromium
// and WebKit, a phone's width and a desk's.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const iso = (ms) => new Date(ms).toISOString();
const seat = { id: "codex@me", provider: "codex", name: "ChatGPT", who: "me@example.com", kind: "account", model: "gpt-6.1-sol", known: true, plan: "Pro" };
const prompt = (n, counted = true) => ({
  window: 272000, tokens: 90000 + n * 8000, counted, turns: n,
  parts: [
    { kind: "system", tokens: 9000, items: [{ name: "prompt", tokens: 9000 }] },
    { kind: "tools", tokens: 12000, items: [{ name: "exec_command", tokens: 7000 }] },
    { kind: "files", tokens: 30000 + n * 4000, items: [{ name: "/work/app/routing.js", tag: "read", tokens: 30000 + n * 4000 }] },
    { kind: "results", tokens: 20000 + n * 2000, items: [{ name: "exec_command", tag: "result", n: n + 2, tokens: 20000 + n * 2000 }] },
    { kind: "chat", tokens: 19000 + n * 2000, items: [{ name: "turn", tag: "turns", n, tokens: 19000 + n * 2000 }] },
  ],
});
// a request as the trace has it at each step: under way without its
// prompt, under way with it, done (est: done, its tokens not counted, no
// cache reported)
function req(id, step, session = "019a-session") {
  const t0 = now - (200 - id) * 1000, done = step === "done" || step === "est";
  return {
    id, seq: id * 10 + ({ start: 0, read: 1, done: 2, est: 2 })[step], time: iso(t0), agent: "codex", session, model: "gpt-6.1-sol", provider: "codex",
    order: [seat], tries: [{ id: seat.id, model: seat.model, start: iso(t0), done, ...(done ? { status: 200, ms: 4000 } : {}) }],
    done, ...(step === "done" ? { status: 200, ms: 4000, tokens: 2000, cost: 0.05, priced: true, usage: [{ in: 3000 + id * 7, cache_read: 87000 + id * 100, out: 400 }] } : {}),
    ...(step === "est" ? { status: 200, ms: 4000 } : {}),
    ...(step === "start" ? {} : { prompt: prompt(id - 100, step !== "est") }),
  };
}
const first = [req(101, "done"), req(102, "done"), req(103, "done")];

function serve(lang, feed, details) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true}; localStorage.setItem("magpie.routingContext", "1"); localStorage.setItem("magpie.routingDetails", "${details ? "1" : "0"}");` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      const trace = (routes) => json({ mine: true, now: new Date().toISOString(), seq: routes.length ? routes[routes.length - 1].seq : Number(url.searchParams.get("after")), totals: { requests: 0, rerouted: 0, errors: 0 }, routes });
      if (!url.searchParams.get("wait")) return trace(first);
      const r = await new Promise((res) => { feed.next = res; });
      return trace([r]);
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

const sizes = [["en", 420], ["en", 1100], ["zh", 420], ["zh", 1100], ["zh-TW", 900], ["ja", 900], ["de", 420], ["en", 420, true], ["en", 1354, true], ["zh", 1100, true]];
// the parts of the card watched on every frame
const WATCH = {
  card: ".rt-ctx-detail .ctx-body", model: ".rt-brief-path code", state: ".rt-ctx-toggle .ctx-state", used: ".rt-ctx .ctx-stat .ctx-big b",
  cache: ".rt-ctx .ctx-cache", figure: ".rt-ctx .ctx-cache .ctx-big b", bar: ".rt-ctx .ctx-cache .ctx-bar", grid: ".rt-ctx .ctx-waffle",
  legend: ".rt-ctx .ctx-legend", foot: ".rt-ctx .ctx-foot",
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: the context window holds still as requests stream in`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const [lang, width, details = false] of sizes) {
      await t.test(`${lang} ${width}px, details ${details ? "open" : "closed"}`, async () => {
        const context = await browser.newContext({ viewport: { width, height: 900 } });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const feed = { next: null };
        await page.route("**/*", serve(lang, feed, details));
        await page.goto("http://magpie.test/?view=routing");
        await page.locator(".rt-req").nth(first.length - 1).waitFor();
        await page.locator(".rt-ctx .ctx-cache").waitFor();
        await page.waitForTimeout(1500); // the cells come in, the first play ends
        // the width changes a little after the card is drawn, as when the
        // list below takes a scrollbar's width off the page: the story's
        // height is measured again then, not at the next live request
        await page.setViewportSize({ width: width - 10, height: 900 });
        await page.waitForTimeout(300);
        const tr = (s) => page.evaluate(([l, s]) => (l === "en" ? s : I18N[l][s]), [lang, s]);
        const w = { last: await tr("The last request's · this one is on its way"), none: await tr("The vendor didn't say"), soon: await tr("Read once the vendor answers") };
        for (const [k, v] of Object.entries(w)) assert.ok(v, `${k} has its ${lang}`);
        const send = async (r) => {
          for (let i = 0; i < 60 && !feed.next; i++) await page.waitForTimeout(50);
          const next = feed.next;
          assert(next, "the page asks for the next trace");
          feed.next = null;
          next(r);
          await page.waitForTimeout(400);
        };
        // An open story gains its waiting explanation the first time it
        // runs. Once both states have appeared, later requests keep that
        // peak height, rather than moving the context on every request.
        if (details) {
          await send(req(104, "read"));
          await send(req(104, "done"));
        }

        // every frame from here on: where each part is, what the cache says
        await page.evaluate((watch) => {
          window.__frames = [];
          window.__on = true;
          const tick = () => {
            const f = {};
            for (const [k, s] of Object.entries(watch)) {
              const e = document.querySelector(s), r = e && e.getBoundingClientRect();
              f[k] = r ? [r.x, r.y, r.width, r.height] : null;
            }
            f.figure$ = document.querySelector(".rt-ctx .ctx-cache .ctx-big b")?.textContent;
            f.fill = document.querySelector(".rt-ctx .ctx-cache .ctx-bar i")?.getBoundingClientRect().width;
            f.scroll = document.querySelector("#view-routing").scrollTop;
            window.__frames.push(f);
            if (window.__on) requestAnimationFrame(tick);
          };
          requestAnimationFrame(tick);
        }, WATCH);
        const cacheNow = () => page.evaluate(() => {
          const c = document.querySelector(".rt-ctx .ctx-cache"), b = c.querySelector(".ctx-big b"), s = c.querySelector(".ctx-big span");
          return {
            cls: c.className, figure: b.textContent, sub: c.querySelector(".ctx-sub").textContent,
            gap: s.getBoundingClientRect().left - b.getBoundingClientRect().right,
            bar: getComputedStyle(c.querySelector(".ctx-bar")).visibility,
          };
        });
        const seen = [];
        for (const id of [105, 106]) {
          for (const step of ["start", "read", "done"]) {
            await send(req(id, step));
            seen.push([id, step, await cacheNow()]);
            if (process.env.ARTIFACT_DIR && id === 105 && step !== "start") {
              await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
              await page.locator(".rt-ctx-detail .ctx-body").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-ctx-${step === "read" ? "live" : "done"}.png`) });
            }
          }
        }
        const frames = await page.evaluate(() => { window.__on = false; return window.__frames; });
        assert(frames.length > 20, `frames were watched: ${frames.length}`);
        for (const k of Object.keys(WATCH)) {
          for (const i of [0, 1, 2, 3]) {
            const vs = frames.map((f) => f[k]?.[i]);
            assert(vs.every((v) => v !== undefined), `${k} is there on every frame`);
            const lo = Math.min(...vs), hi = Math.max(...vs);
            assert(hi - lo <= 0.5, `${k} held still (${["x", "y", "width", "height"][i]} from ${lo} to ${hi}, frames ${JSON.stringify(vs.map((v, j) => j + ":" + v).filter((x, j) => j === 0 || vs[j] !== vs[j - 1]))})`);
          }
        }
        // the cache never flashes to a dash; its bar never empties
        assert.deepEqual([...new Set(frames.map((f) => f.figure$))].filter((x) => !/^\d+%$/.test(x)), [], "the cache figure is a figure on every frame");
        assert(frames.every((f) => f.fill > 0), "the cache bar never empties");
        assert.deepEqual([...new Set(frames.map((f) => f.scroll))], [frames[0].scroll], "the page doesn't scroll by itself");
        // under way with its prompt: the last request's, dimmed and said so;
        // done: its own
        for (const [id, step, c] of seen) {
          // just begun, without its prompt, the card is still the last
          // request's, as it was (routing-live-patch)
          if (step !== "read") {
            assert(!/\bwas\b/.test(c.cls), `${id} ${step} shows its request's own cache`);
            assert.notEqual(c.sub, w.last);
          } else {
            assert.match(c.cls, /\bwas\b/, `${id} ${step}: the last request's cache, dimmed`);
            assert.equal(c.sub, w.last);
          }
          assert(c.gap >= 3, `the figure and its word are apart: ${c.gap}px`);
          assert.equal(c.bar, "visible");
        }

        // a request done whose cache the vendor didn't report: a dash set
        // apart from its word, why, no bar, and the card still in its place
        const before = await page.evaluate((s) => [...document.querySelectorAll(s)].map((e) => e.getBoundingClientRect().y), Object.values(WATCH).join(","));
        await send(req(107, "est"));
        const est = await cacheNow();
        assert.equal(est.figure, "—");
        assert.match(est.cls, /\bnone\b/);
        assert.equal(est.sub, w.none);
        assert(est.gap >= 3, `the dash and its word are apart: ${est.gap}px`);
        assert.equal(est.bar, "hidden", "no empty bar under the dash");
        const after = await page.evaluate((s) => [...document.querySelectorAll(s)].map((e) => e.getBoundingClientRect().y), Object.values(WATCH).join(","));
        assert.deepEqual(after.map(Math.round), before.map(Math.round), "nothing moved for the estimated request");
        if (process.env.ARTIFACT_DIR) await page.locator(".rt-ctx-detail .ctx-body").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-ctx-none.png`) });
        // another session's first request, under way: no last request of
        // its own to show, so a dash till it is read, in the same place
        const top = await page.locator(".rt-ctx .ctx-cache").evaluate((e) => e.getBoundingClientRect().y);
        await send(req(108, "read", "019b-other"));
        const soon = await cacheNow();
        assert.equal(soon.figure, "—");
        assert.match(soon.cls, /\bnone\b/);
        assert.equal(soon.sub, w.soon);
        assert(soon.gap >= 3, `the dash and its word are apart: ${soon.gap}px`);
        assert.equal(soon.bar, "hidden");
        assert(Math.abs(await page.locator(".rt-ctx .ctx-cache").evaluate((e) => e.getBoundingClientRect().y) - top) <= 0.5, "the cache kept its place");
        if (process.env.ARTIFACT_DIR) await page.locator(".rt-ctx-detail .ctx-body").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-ctx-soon.png`) });
        assert.deepEqual(errors, []);
        feed.next?.(req(999, "done"));
        await context.close();
      });
    }
  });
}
