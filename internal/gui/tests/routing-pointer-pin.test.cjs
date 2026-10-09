// Run with Node's test runner and Playwright on the module path; see README.md.
// Routing: the groups stay under the pointer as requests come in (cwfox67 on
// X: picking a manual group's model meant fighting the page, which shook as
// it updated). With the groups low in the view, as they are at the page's
// foot, the requests list above them kept its place and grew, so a group's
// card slid down from under the pointer with each request. With the pointer
// on a manual group's card and requests streaming in, the card stays where
// it is on the screen. In Chromium and WebKit, at a phone's width and a
// desk's.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const fs = require("node:fs/promises");
const path = require("node:path");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now(), iso = (ms) => new Date(ms).toISOString();
const seat = { id: "codex@me", provider: "codex", name: "ChatGPT", who: "me@example.com", kind: "account", model: "gpt-6.1-sol", known: true, plan: "Pro" };
const prompt = (n) => ({ window: 272000, tokens: 90000 + n * 8000, counted: true, turns: n, parts: [
  { kind: "system", tokens: 9000, items: [{ name: "prompt", tokens: 9000 }] },
  { kind: "files", tokens: 30000 + n * 4000, items: Array.from({length: 1 + (n % 5)}, (_, i) => ({ name: "/work/f" + i + ".js", tag: "read", tokens: 3000 })) },
  { kind: "chat", tokens: 19000 + n * 2000, items: [{ name: "turn", tag: "turns", n, tokens: 19000 + n * 2000 }] } ] });
function req(id, step) {
  const t0 = now - (400 - id) * 1000, done = step === "done";
  return { id, seq: id * 10 + ({ start: 0, read: 1, done: 2 })[step], time: iso(t0), agent: "codex", session: "019a-session", model: "gpt-6.1-sol", provider: "codex",
    order: [seat], tries: [{ id: seat.id, model: seat.model, start: iso(t0), done, ...(done ? { status: 200, ms: 4000 } : {}) }],
    done, ...(done ? { status: 200, ms: 4000, tokens: 2000, cost: 0.05, priced: true, usage: [{ in: 3000, cache_read: 87000, out: 400 }] } : {}),
    ...(step === "start" ? {} : { prompt: prompt(id - 100) }) };
}
const first = [101, 102, 103].map((i) => req(i, "done"));
const models = [{ id: "a/sol", name: "sol", providerName: "A", icon: "generic" }, { id: "b/flash", name: "flash", providerName: "B", icon: "generic" }];
const base = { members: ["a/sol", "b/flash"], ready: true, memberInfo: [{ id: "a/sol", ready: true }, { id: "b/flash", ready: true }], offers: [], shared: [] };
const groups = { models, pools: [], groups: [{ ...base, id: "hand", name: "Hand", routing: "manual", pick: "a/sol", picked: "a/sol" }, { ...base, id: "auto", name: "Auto", routing: "order" }] };
function serve(feed) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang: "en", theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url()); const json = (d) => route.fulfill({ json: d });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: 'window.bootPrefs = {lang:"en",theme:"light",web:true};' });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      const trace = (routes) => json({ mine: true, now: new Date().toISOString(), seq: routes.length ? routes[routes.length - 1].seq : Number(url.searchParams.get("after")), totals: { requests: 0, rerouted: 0, errors: 0 }, routes });
      if (!url.searchParams.get("wait")) return trace(first);
      return trace([await new Promise((res) => { feed.next = res; })]);
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const ct = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType: ct });
  };
}
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a group under the pointer stays put as requests come in`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const [w, h] of [[420, 760], [1100, 760]]) {
      await t.test(`${w}x${h}`, async () => {
        const context = await browser.newContext({ viewport: { width: w, height: h } });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const feed = {};
        await page.route("**/*", serve(feed));
        await page.goto("http://magpie.test/?view=routing");
        await page.locator(".rt-group").first().waitFor();
        await page.waitForTimeout(1500);
        // the reader wheels down to the groups, then puts the pointer on the
        // manual group's card
        await page.mouse.move(8, h / 2);
        for (let i = 0; i < 80; i++) {
          const top = await page.evaluate(() => document.querySelector(".rt-group").getBoundingClientRect().top);
          if (top < 300) break;
          await page.mouse.wheel(0, Math.min(200, top - 250));
          await page.waitForTimeout(60);
        }
        await page.waitForTimeout(300);
        const b = await page.locator(".rt-group").first().boundingBox();
        await page.mouse.move(b.x + 40, b.y + 10);
        await page.waitForTimeout(100);
        const at = () => page.evaluate(() => [document.querySelector(".rt-group").getBoundingClientRect().top, document.querySelector(".rt-reqs").offsetHeight, document.querySelector("#rt").offsetHeight]);
        const was = await at(), seen = [];
        for (let id = 104; id < 112; id++) for (const s of ["start", "read", "done"]) {
          for (let i = 0; i < 60 && !feed.next; i++) await page.waitForTimeout(50);
          const next = feed.next;
          assert(next, "the page asks for the next trace");
          feed.next = null;
          next(req(id, s));
          await page.waitForTimeout(250);
          seen.push(await at());
        }
        const last = seen[seen.length - 1];
        assert(last[1] + last[2] - was[1] - was[2] > 50 || w < 760, `what is above the groups really changed: ${JSON.stringify([was, last])}`);
        for (const s of seen) assert(Math.abs(s[0] - was[0]) <= 1.5, `the card stayed under the pointer: ${was[0]} → ${seen.map((x) => Math.round(x[0])).join(" ")}`);
        assert.deepEqual(errors, []);
        feed.next?.(req(999, "done"));
        await context.close();
      });
    }
  });
}
