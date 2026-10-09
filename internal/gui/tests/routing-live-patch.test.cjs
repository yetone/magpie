// Run with Node's test runner and Playwright on the module path; see README.md.
// Routing, live: a session's next request changes only what it changes
// (Zhenzhen on Discord, omarchy: each new request flashed the whole
// context window and the request list under it, and the window's grid of
// cells grew and shrank so everything under it jumped). A request comes in
// (first without its prompt, as the gateway reads that beside it, then
// with it, then done), twice: the requests listed before stay the same
// nodes, the new one goes in beside them, the context window's card and
// grid are the ones that were there, patched, and the grid and its box
// keep their height on every frame between. In Chromium and WebKit, at a
// phone's width and a desk's; with the card open, and folded to its head
// (magpie.ctxShut), whose nodes, bar and height stay the same too.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const iso = (ms) => new Date(ms).toISOString();
const seat = { id: "codex@me", provider: "codex", name: "ChatGPT", who: "me@example.com", kind: "account", model: "gpt-6.1-sol", known: true, plan: "Pro" };

// what a codex prompt holds, a little more each request
const prompt = (n) => ({
  window: 272000, tokens: 90000 + n * 8000, counted: true, turns: n,
  parts: [
    { kind: "system", tokens: 9000, items: [{ name: "prompt", tokens: 9000 }] },
    { kind: "tools", tokens: 12000, items: [{ name: "exec_command", tokens: 7000 }, { name: "apply_patch", tokens: 5000 }] },
    { kind: "files", tokens: 30000 + n * 4000, items: [{ name: "/work/app/routing.js", tag: "read", tokens: 20000 + n * 3000 }, { name: "/work/app/context.js", tag: "read", tokens: 10000 + n * 1000 }] },
    { kind: "results", tokens: 20000 + n * 2000, items: [{ name: "exec_command", tag: "result", n: n + 2, tokens: 20000 + n * 2000 }] },
    { kind: "chat", tokens: 19000 + n * 2000, items: [{ name: "turn", tag: "turns", n, tokens: 19000 + n * 2000 }] },
  ],
});
// request id, as the trace has it at each step: under way without its
// prompt, under way with it, done
function req(id, step) {
  const t0 = now - (200 - id) * 1000;
  const done = step === "done";
  return {
    id, seq: id * 10 + ({ start: 0, read: 1, done: 2 })[step], time: iso(t0), agent: "codex", session: "019a-session", model: "gpt-6.1-sol", provider: "codex",
    order: [seat], tries: [{ id: seat.id, model: seat.model, start: iso(t0), done, ...(done ? { status: 200, ms: 4000 } : {}) }],
    done, ...(done ? { status: 200, ms: 4000, tokens: 2000, cost: 0.05, priced: true, usage: [{ in: 3000, cache_read: 87000 + id * 100, out: 400 }] } : {}),
    ...(step === "start" ? {} : { prompt: prompt(id - 100) }),
  };
}
const first = [req(101, "done"), req(102, "done"), req(103, "done")];

function serve(feed) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang: "en", theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: 'window.bootPrefs = {lang:"en",theme:"light",web:true};' });
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
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a live request patches the context window and the request list in place`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const [width, height] of [[420, 760], [1100, 760]]) {
      await t.test(`${width}x${height}, open`, async () => {
        const context = await browser.newContext({ viewport: { width, height } });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const feed = { next: null };
        await page.route("**/*", serve(feed));
        await page.goto("http://magpie.test/?view=routing");
        await page.locator(".rt-req").nth(first.length - 1).waitFor();
        await page.locator(".rt-ctx .ctx-waffle").waitFor();
        await page.waitForTimeout(1500); // the cells come in, the first play ends

        // what is there now, and every layout from here on
        await page.evaluate(() => {
          const box = document.querySelector(".rt-ctx"), grid = box.querySelector(".ctx-waffle");
          window.__was = { rows: [...document.querySelectorAll(".rt-req")], items: [...box.querySelectorAll(".ctx-row, .ctx-leg")], card: box.querySelector(".ctx-card"), grid, cells: [...grid.children], box: box.offsetHeight, gridH: grid.offsetHeight };
          window.__sizes = [];
          window.__ro = new ResizeObserver(() => {
            const g = document.querySelector(".rt-ctx .ctx-waffle");
            window.__sizes.push({ box: box.offsetHeight, grid: g ? g.offsetHeight : 0 });
          });
          window.__ro.observe(box);
          window.__ro.observe(grid);
          window.__scroll = [];
          document.querySelector("#view-routing").addEventListener("scroll", (e) => window.__scroll.push(e.target.scrollTop));
        });
        const send = async (r) => {
          for (let i = 0; i < 60 && !feed.next; i++) await page.waitForTimeout(50);
          const next = feed.next;
          assert(next, "the page asks for the next trace");
          feed.next = null;
          next(r);
          await page.waitForTimeout(400);
        };
        const said = [];
        for (const id of [104, 105]) {
          for (const step of ["start", "read", "done"]) {
            await send(req(id, step));
            said.push(await page.evaluate(() => [...document.querySelectorAll(".rt-ctx :is(.ctx-crumbs, .ctx-cache .ctx-big b)")].map((e) => e.textContent).join(" ")));
          }
          await page.waitForTimeout(600);
        }
        // under way, the card waits for the prompt in its place, then tells
        // of it, its cache not read yet; done, with what the vendor read
        assert.deepEqual(said, [
          "97% Codex › 019a-session › #103", "— Codex › 019a-session › #104", "97% Codex › 019a-session › #104",
          "97% Codex › 019a-session › #104", "— Codex › 019a-session › #105", "97% Codex › 019a-session › #105",
        ]);
        const got = await page.evaluate(() => {
          window.__ro.disconnect();
          const box = document.querySelector(".rt-ctx"), grid = box.querySelector(".ctx-waffle"), rows = [...document.querySelectorAll(".rt-req")];
          const w = window.__was;
          return {
            rows: rows.length,
            kept: w.rows.map((r) => rows.includes(r)),
            items: w.items.every((e) => e.isConnected && box.contains(e)),
            card: box.querySelector(".ctx-card") === w.card,
            grid: grid === w.grid,
            cells: w.cells.every((c, i) => grid.children[i] === c) && grid.children.length === w.cells.length,
            sizes: window.__sizes, box0: w.box, grid0: w.gridH, gridH: grid.offsetHeight,
            crumbs: box.querySelector(".ctx-crumbs").textContent,
            used: box.querySelector(".ctx-big b").textContent,
            scroll: window.__scroll,
          };
        });
        assert.equal(got.rows, first.length + 2, "both new requests are listed");
        assert.deepEqual(got.kept, first.map(() => true), "the requests listed before are the same nodes");
        assert(got.card, "the context window's card is the one that was there");
        assert(got.grid, "its grid is the one that was there");
        assert(got.cells, "its cells are the ones that were there");
        assert(got.items, "its legend and contents rows are the ones that were there");
        // and it tells of the newest request
        assert.match(got.crumbs, /#105$/);
        assert.equal(got.used, "130K");
        assert.equal(got.gridH, got.grid0, "the grid keeps its height");
        for (const s of got.sizes) {
          assert.equal(s.grid, got.grid0, `the grid kept its height on every frame: ${JSON.stringify(got.sizes)}`);
          assert(s.box >= got.box0 - 1, `the context window never collapsed: ${JSON.stringify([got.box0, got.sizes])}`);
        }
        assert.deepEqual(got.scroll, [], "the page doesn't scroll by itself");
        assert.deepEqual(errors, []);
        feed.next?.(req(999, "done"));
        await context.close();
      });
      await t.test(`${width}x${height}, folded`, async () => {
        const context = await browser.newContext({ viewport: { width, height } });
        const page = await context.newPage();
        await page.addInitScript(() => { try { localStorage.setItem("magpie.ctxShut", "1"); } catch {} });
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const feed = { next: null };
        await page.route("**/*", serve(feed));
        await page.goto("http://magpie.test/?view=routing");
        await page.locator(".rt-req").nth(first.length - 1).waitFor();
        await page.locator(".rt-ctx .ctx-card.shut").waitFor();
        await page.waitForTimeout(600);
        await page.evaluate(() => {
          const box = document.querySelector(".rt-ctx");
          window.__was = { rows: [...document.querySelectorAll(".rt-req")], line: box.querySelector(".ctx-card"), head: box.querySelector(".ctx-head"), bar: box.querySelector(".ctx-stack"), h: box.offsetHeight, list: document.querySelector(".rt-reqs").getBoundingClientRect().top };
          window.__sizes = [];
          window.__ro = new ResizeObserver(() => window.__sizes.push(box.offsetHeight));
          window.__ro.observe(box);
          window.__scroll = [];
          document.querySelector("#view-routing").addEventListener("scroll", (e) => window.__scroll.push(e.target.scrollTop));
        });
        const said = [];
        for (const id of [104, 105]) {
          for (const step of ["start", "read", "done"]) {
            for (let i = 0; i < 60 && !feed.next; i++) await page.waitForTimeout(50);
            assert(feed.next, "the page asks for the next trace");
            const next = feed.next;
            feed.next = null;
            next(req(id, step));
            await page.waitForTimeout(400);
            said.push(await page.evaluate(() => {
              const box = document.querySelector(".rt-ctx");
              return box.querySelector(".ctx-short").textContent + " " + box.querySelector(".ctx-crumbs").textContent.split(" › ").pop()
                + " " + [...box.querySelectorAll(".ctx-stack > i")].map((i) => i.style.width).join(",");
            }));
          }
        }
        // the folded head waits for a prompt in its place, then tells of it
        const bar = (f, r, c) => `3.31%,4.41%,0%,${f}%,${r}%,${c}%`;
        assert.deepEqual(said, [
          "114K / 272K · 42% #103 " + bar(15.44, 9.56, 9.19),
          ...Array(3).fill("122K / 272K · 45% #104 " + bar(16.91, 10.29, 9.93)),
          ...Array(2).fill("130K / 272K · 48% #105 " + bar(18.38, 11.03, 10.66)),
        ]);
        const got = await page.evaluate(() => {
          window.__ro.disconnect();
          const box = document.querySelector(".rt-ctx"), w = window.__was, rows = [...document.querySelectorAll(".rt-req")];
          return {
            kept: w.rows.every((r) => rows.includes(r)), line: box.querySelector(".ctx-card") === w.line && box.querySelector(".ctx-head") === w.head, bar: box.querySelector(".ctx-stack") === w.bar,
            card: !box.querySelector(".ctx-card").classList.contains("shut") || box.querySelector(".ctx-waffle").getClientRects().length > 0, h: box.offsetHeight, h0: w.h, sizes: window.__sizes, scroll: window.__scroll,
            list: document.querySelector(".rt-reqs").getBoundingClientRect().top, list0: w.list,
          };
        });
        assert(got.kept, "the requests listed before are the same nodes");
        assert(got.line && got.bar, "the folded card, its head and bar are the ones that were there");
        assert(!got.card, "it stays folded, its grid out of sight");
        for (const h of [got.h, ...got.sizes]) assert.equal(h, got.h0, `the folded card keeps its height: ${JSON.stringify([got.h0, got.sizes])}`);
        assert.equal(got.list, got.list0, "the request list stays where it was");
        assert.deepEqual(got.scroll, [], "the page doesn't scroll by itself");
        assert.deepEqual(errors, []);
        feed.next?.(req(999, "done"));
        await context.close();
      });
    }
  });
}
