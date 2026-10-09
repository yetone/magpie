// Run with Node's test runner and Playwright on the module path; see README.md.
// Routing: the context window's card folds to its head (0xBCD18E on X: it
// took most of the height, so the request list under it showed three
// rows, even on a portrait screen). Folded, it is one line with how full
// the window is beside the title, the list under it moves up, and it stays
// folded for the next request picked and after the page comes back;
// opened, it is all there again. In Chromium and WebKit, at a phone's
// width and a desk's.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const iso = (ms) => new Date(ms).toISOString();
const seat = { id: "codex@me", provider: "codex", name: "ChatGPT", who: "me@example.com", kind: "account", model: "gpt-6.1-sol", known: true, plan: "Pro" };

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
function req(id) {
  const t0 = now - (200 - id) * 1000;
  return {
    id, seq: id * 10, time: iso(t0), agent: "codex", session: "019a-session", model: "gpt-6.1-sol", provider: "codex",
    order: [seat], tries: [{ id: seat.id, model: seat.model, start: iso(t0), done: true, status: 200, ms: 4000 }],
    done: true, status: 200, ms: 4000, tokens: 2000, cost: 0.05, priced: true, usage: [{ in: 3000, cache_read: 87000 + id * 100, out: 400 }],
    prompt: prompt(id - 100),
  };
}
const routes = [req(101), req(102), req(103)];

function serve() {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang: "en", theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: 'window.bootPrefs = {lang:"en",theme:"light",web:true};' });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return; // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: routes[routes.length - 1].seq, totals: { requests: 0, rerouted: 0, errors: 0 }, routes });
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

const look = (page) => page.evaluate(() => {
  const card = document.querySelector(".rt-ctx .ctx-card"), fold = card.querySelector(".ctx-fold"), short = card.querySelector(".ctx-short");
  const shown = (e) => !!e && e.getClientRects().length > 0;
  return {
    shut: card.classList.contains("shut"),
    expanded: fold.getAttribute("aria-expanded"),
    height: card.offsetHeight,
    short: shown(short) ? short.textContent : null,
    grid: shown(card.querySelector(".ctx-waffle")),
    contents: shown(card.querySelector(".ctx-contents")),
    cells: card.querySelectorAll(".ctx-waffle > i").length,
    crumbs: card.querySelector(".ctx-crumbs").textContent,
    overflow: card.scrollWidth - card.clientWidth + (document.documentElement.scrollWidth - document.documentElement.clientWidth),
    after: card.getBoundingClientRect().bottom,
  };
});

// the reader scrolls to what they click with the wheel, as at a phone's
// width the card is under the request list
async function click(page, b) {
  const h = page.viewportSize().height;
  for (let i = 0; i < 20; i++) {
    const r = await b.boundingBox();
    if (r && r.y >= 0 && r.y + r.height <= h) break;
    await page.mouse.move(page.viewportSize().width / 2, h / 2);
    await page.mouse.wheel(0, r && r.y < 0 ? -200 : 200);
    await page.waitForTimeout(100);
  }
  await b.click();
  await page.waitForTimeout(300);
}
const fold = (page) => click(page, page.locator(".rt-ctx .ctx-fold"));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: Routing's context window folds to its head and stays folded`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const [width, height] of [[420, 760], [1100, 760]]) {
      await t.test(`${width}x${height}`, async () => {
        const context = await browser.newContext({ viewport: { width, height } });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve());
        await page.goto("http://magpie.test/?view=routing");
        await page.locator(".rt-ctx .ctx-waffle").waitFor();
        await page.waitForTimeout(800);

        const open = await look(page);
        assert.equal(open.shut, false, "it starts open");
        assert.equal(open.expanded, "true");
        assert(open.grid && open.contents, "open, the grid and contents are there");
        assert.equal(open.short, null, "open, the short figure isn't shown");

        await fold(page);
        const shut = await look(page);
        assert.equal(shut.shut, true, "a click folds it");
        assert.equal(shut.expanded, "false");
        assert(!shut.grid && !shut.contents, "folded, the grid and contents are gone");
        assert.equal(shut.short, "114K / 272K · 42%", "folded, how full it is is beside the title");
        assert(shut.height < 60, `folded, it is one line: ${shut.height}px`);
        assert(shut.after < open.after - 200, `what is under it moves up: ${open.after} -> ${shut.after}`);
        assert(shut.overflow <= 0, `nothing runs over at ${width}px: ${shut.overflow}`);

        // another request picked: still folded, telling of that one
        await click(page, page.locator(".rt-req").last());
        const other = await look(page);
        assert.equal(other.shut, true, "the next request's card is folded too");
        assert.notEqual(other.crumbs, shut.crumbs, "it tells of the request picked");

        // the page comes back: still folded
        await page.reload();
        await page.locator(".rt-ctx .ctx-card").waitFor();
        await page.waitForTimeout(500);
        assert.equal((await look(page)).shut, true, "it stays folded after the page comes back");

        // opened again, it is all there; its title stays where it was and
        // the page doesn't follow it (a click never moves the page: with
        // a folded padding of its own, it moved 4px)
        const b = page.locator(".rt-ctx .ctx-fold");
        await click(page, page.locator(".rt-ctx .ctx-short"));
        const before = await b.evaluate((e) => [e.getBoundingClientRect().top, document.querySelector("#view-routing").scrollTop]);
        await b.click();
        await page.waitForTimeout(800);
        const moved = await b.evaluate((e) => [e.getBoundingClientRect().top, document.querySelector("#view-routing").scrollTop]);
        assert.equal(moved[1], before[1], `opening it doesn't scroll the page: ${before} -> ${moved}`);
        assert(Math.abs(moved[0] - before[0]) <= 1, `its title stays under the pointer: ${before} -> ${moved}`);
        const again = await look(page);
        assert.equal(again.shut, false);
        assert.equal(again.expanded, "true");
        assert(again.grid && again.contents, "opened, the grid and contents are back");
        assert.equal(again.cells, 400);
        assert.equal(again.height, open.height, "and it is as tall as it was");
        assert.deepEqual(errors, []);
        await context.close();
      });
    }
  });
}
