// Run with Node's test runner and Playwright on the module path; see README.md.
// Routing: the context window's card folded to its head (routing-ctx-fold
// tests the fold itself) is one line of a fixed height with a thin bar of
// what fills the window, a part each, beside how full it is (PILIPALA on
// X: on a wide window the card took the screen, and the requests and the
// magpies flying were out of sight under it; Jeremy.Zhou on Discord: the
// page is too crowded). Folded, the stage and the request list are in view
// without scrolling at 1440x900 and 1920x1080; at a narrow 440px the head
// is all the card takes (the stage and the story stacked above it are what
// is left before the list). Unfolding by its title doesn't move the page. A
// request picked keeps the fold; one opened from the Usage page's Context
// tab shows its card unfolded, that request only, and the reader's choice
// is untouched. In Chromium and WebKit, in English, Chinese and German.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const iso = (ms) => new Date(ms).toISOString();
const seat = { id: "codex@me", provider: "codex", name: "ChatGPT", who: "me@example.com", kind: "account", model: "gpt-6.1-sol", known: true, plan: "Pro" };

// a codex prompt, a little more each request
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
const routes = Array.from({ length: 12 }, (_, i) => req(101 + i));
const newest = routes[routes.length - 1];

const words = { en: "Context window", zh: "上下文窗口", de: "Kontextfenster" };

function serve(lang) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: newest.seq, totals: { requests: 0, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

async function open(browser, lang, width, height) {
  const context = await browser.newContext({ viewport: { width, height } });
  const page = await context.newPage();
  // folded is the reader's own choice, kept in magpie.ctxShut; set once, so a reload keeps what the page stores
  await page.addInitScript(() => { try { if (!sessionStorage.getItem("seeded")) { sessionStorage.setItem("seeded", "1"); localStorage.setItem("magpie.ctxShut", "1"); } } catch {} });
  page.setDefaultTimeout(5000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang));
  await page.goto("http://magpie.test/?view=routing");
  await page.locator(".rt-req").nth(routes.length - 1).waitFor();
  await page.locator(".rt-ctx > *").first().waitFor();
  await page.waitForTimeout(500);
  return { context, page, errors };
}
// the reader scrolls to what they click with the wheel (as routing-ctx-fold
// does): a card opened afresh can sit under the window's bottom
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
const scrollTop = (page) => page.evaluate(() => document.querySelector("#view-routing").scrollTop);
const stored = (page) => page.evaluate(() => localStorage.getItem("magpie.ctxShut"));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: the context window on Routing folds to one line`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());

    for (const [width, height] of [[1920, 1080], [1440, 900], [440, 900]]) {
      for (const lang of Object.keys(words)) {
        await t.test(`${lang} ${width}x${height}: folded to one line, the stage and the requests in view`, async () => {
          const { context, page, errors } = await open(browser, lang, width, height);
          const box = page.locator(".rt-ctx");
          const fold = box.locator(".ctx-fold");
          assert.equal(await box.locator(".ctx-card.shut").count(), 1, "folded, as the reader keeps it");
          assert.equal(await fold.getAttribute("aria-expanded"), "false");
          assert.equal(await fold.innerText(), words[lang]);
          assert.equal(await box.locator(".ctx-short").innerText(), "186K / 272K · 68%");
          assert.match(await box.locator(".ctx-crumbs").textContent(), /#112$/);
          // a bar of what fills the window, a part each (this prompt has no memory)
          assert.deepEqual(await box.locator(".ctx-stack > i").evaluateAll((is) => is.map((i) => i.className + " " + i.style.width)),
            ["k-system 3.31%", "k-tools 4.41%", "k-memory 0%", "k-files 28.68%", "k-results 16.18%", "k-chat 15.81%"]);
          const at = await page.evaluate(() => {
            const r = (s) => document.querySelector(s).getBoundingClientRect();
            const card = r(".rt-ctx .ctx-card"), head = r(".rt-ctx .ctx-head"), bar = r(".rt-ctx .ctx-stack"), c = document.querySelector(".rt-ctx .ctx-card");
            return { vh: innerHeight, stage: r(".rt-stage").bottom, head: r(".rt-req-head").top + 24, row: r(".rt-req").bottom, cardH: card.height, headH: head.height,
              bar: bar.width, barIn: bar.right <= head.right + 0.5, over: c.scrollWidth - c.clientWidth, wide: document.documentElement.scrollWidth <= innerWidth };
          });
          assert.equal(await scrollTop(page), 0);
          assert(at.stage <= at.vh, `the magpies' stage is in view: ${JSON.stringify(at)}`);
          if (width >= 1440) {
            assert(at.head <= at.vh, `the request list's heading is in view: ${JSON.stringify(at)}`);
            assert(at.row <= at.vh, `the first request is in view: ${JSON.stringify(at)}`);
          }
          assert(at.cardH < 60 && at.headH === 20, `one line: ${JSON.stringify(at)}`);
          assert(at.bar >= 24 && at.barIn, `the bar is there and in the line: ${JSON.stringify(at)}`);
          assert(at.over <= 0 && at.wide, `nothing runs over: ${JSON.stringify(at)}`);
          assert.deepEqual(errors, []);
          await context.close();
        });
      }
    }

    await t.test("unfolded by its title without moving the page", async () => {
      const { context, page, errors } = await open(browser, "en", 1440, 900);
      // the reader scrolls down a little first, so a click that moved the page would show
      await page.mouse.move(700, 500);
      await page.mouse.wheel(0, 120);
      await page.waitForTimeout(400);
      const y = await scrollTop(page);
      assert(y > 0, "the page scrolled");
      const fold = page.locator(".rt-ctx .ctx-fold");
      const foldTop = await fold.evaluate((e) => e.getBoundingClientRect().top);
      await fold.click();
      await page.locator(".rt-ctx .ctx-card:not(.shut) .ctx-waffle").waitFor();
      assert.equal(await page.locator(".rt-ctx .ctx-waffle > i").count(), 400);
      assert.equal(await fold.getAttribute("aria-expanded"), "true");
      assert.equal(await page.locator(".rt-ctx .ctx-stack").evaluate((e) => e.getClientRects().length), 0, "open, the bar is the card's own legend");
      await page.waitForTimeout(800);
      assert.equal(await scrollTop(page), y, "unfolding doesn't move the page");
      const after = await fold.evaluate((e) => e.getBoundingClientRect().top);
      assert(Math.abs(after - foldTop) <= 1, `the title stays under the pointer: ${foldTop} → ${after}`);
      assert.equal(await stored(page), "0");
      assert.deepEqual(errors, []);
      await context.close();
    });

    await t.test("a request picked keeps the fold; one opened for its context window shows it, for itself only", async () => {
      const { context, page, errors } = await open(browser, "en", 1440, 900);
      const shut = () => page.locator(".rt-ctx .ctx-card").evaluate((c) => c.classList.contains("shut"));
      const crumb = () => page.locator(".rt-ctx .ctx-crumbs").evaluate((c) => c.textContent.split(" › ").pop());
      const y = await scrollTop(page);
      await page.locator(".rt-req").nth(3).click();
      await page.waitForTimeout(300);
      assert.equal(await shut(), true, "a request picked stays folded");
      assert.equal(await crumb(), "#109");
      assert.equal(await scrollTop(page), y, "the pick doesn't move the page");

      // the Usage page's Context tab's Latest request, or a point of its line
      await page.evaluate((r) => window.openRoute(r.id, r.time, { context: true }), routes[2]);
      await page.locator(".rt-ctx .ctx-card:not(.shut) .ctx-waffle").waitFor();
      assert.equal(await crumb(), "#103");
      assert.equal(await page.locator(".rt-ctx .ctx-fold").getAttribute("aria-expanded"), "true");
      assert.equal(await stored(page), "1", "the reader's own choice is untouched");

      await page.locator(".rt-req").nth(5).click();
      await page.waitForTimeout(300);
      assert.equal(await shut(), true, "the next request is folded again");
      assert.equal(await crumb(), "#107");
      assert.equal(await page.locator(".rt-ctx .ctx-fold").getAttribute("aria-expanded"), "false");

      // opened for it again, then folded by its title: folded it stays, for it and the next
      await page.evaluate((r) => window.openRoute(r.id, r.time, { context: true }), routes[2]);
      await page.locator(".rt-ctx .ctx-card:not(.shut)").waitFor();
      await click(page, page.locator(".rt-ctx .ctx-fold"));
      assert.equal(await shut(), true, "the title folds it");
      assert.equal(await stored(page), "1");
      await click(page, page.locator(".rt-req").nth(5));
      await page.evaluate((r) => window.openRoute(r.id, r.time), routes[2]);
      await page.waitForTimeout(300);
      assert.equal(await crumb(), "#103");
      assert.equal(await shut(), true, "a request opened from the list or a link stays folded");
      assert.deepEqual(errors, []);
      await context.close();
    });
  });
}
