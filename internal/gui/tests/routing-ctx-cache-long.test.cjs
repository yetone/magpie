// Run with Node's test runner and Playwright on the module path; see README.md.
// Routing, live: a request under way in a long session shows its session's
// last cache, dimmed, never "—" (shenggxhz on X: Codex in T3 Code read
// "— 命中 · 供应商回答后可见" and took it for no cache at all). Two ways
// it showed the dash:
//   - the card forgot the last cache whenever its session's line started
//     at another request; Routing keeps the latest 60 requests, so in a
//     session longer than that the line's first moved on with each new
//     request, and every request under way showed "—";
//   - the page opened while a request was under way had no last cache to
//     show, though the session's answered requests were in the list.
// Every GUI language, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const now = Date.now();
const iso = (ms) => new Date(ms).toISOString();
const seat = { id: "codex@me", provider: "codex", name: "ChatGPT", who: "me@example.com", kind: "account", model: "gpt-6.1-sol", known: true, plan: "Pro" };
const prompt = (n) => ({
  window: 500000, tokens: 90000 + n * 1000, counted: true, turns: 12,
  parts: [
    { kind: "system", tokens: 9000, items: [{ name: "prompt", tokens: 9000 }] },
    { kind: "results", tokens: 40000 + n * 500, items: [{ name: "exec_command", tag: "result", n: 3, tokens: 40000 + n * 500 }] },
    { kind: "chat", tokens: 41000 + n * 500, items: [{ name: "turn", tag: "turns", n: 12, tokens: 41000 + n * 500 }] },
  ],
});
function req(id, step) {
  const t0 = now - (400 - id) * 1000, done = step === "done";
  return {
    id, seq: id * 10 + ({ start: 0, read: 1, done: 2 })[step], time: iso(t0), agent: "codex", session: "019a-long", model: "gpt-6.1-sol", provider: "codex",
    order: [seat], tries: [{ id: seat.id, model: seat.model, start: iso(t0), done, ...(done ? { status: 200, ms: 4000 } : {}) }],
    done, ...(done ? { status: 200, ms: 4000, tokens: 2000, priced: true, usage: [{ in: 9000, cache_read: 81000 + id * 100, out: 400 }] } : {}),
    ...(step === "start" ? {} : { prompt: prompt(id - 100) }),
  };
}

function serve(lang, first, feed) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
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

async function open(browser, lang, first) {
  const context = await browser.newContext({ viewport: { width: 1100, height: 900 } });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const feed = { next: null };
  await page.route("**/*", serve(lang, first, feed));
  await page.goto("http://magpie.test/?view=routing");
  await page.locator(".rt-req").first().waitFor();
  await page.locator(".rt-ctx .ctx-card").waitFor();
  const send = async (r) => {
    for (let i = 0; i < 60 && !feed.next; i++) await page.waitForTimeout(50);
    assert(feed.next, "the page asks for the next trace");
    const next = feed.next;
    feed.next = null;
    next(r);
    await page.waitForTimeout(400);
  };
  const cache = () => page.evaluate(() => {
    const c = document.querySelector(".rt-ctx .ctx-cache");
    return c && { cls: c.className, figure: c.querySelector(".ctx-big b").textContent, sub: c.querySelector(".ctx-sub").textContent };
  });
  const tr = (s) => page.evaluate(([l, s]) => (l === "en" ? s : I18N[l][s]), [lang, s]);
  return { context, errors, send, cache, last: await tr("The last request's · this one is on its way") };
}

const langs = ["en", "zh", "zh-TW", "ja", "de"];
const pctOf = (id) => ((100 * (81000 + id * 100)) / (90000 + id * 100)).toFixed(0) + "%";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a request under way in a long session shows the last cache, never a dash`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of langs) {
      await t.test(`${lang}: more requests than Routing keeps`, async () => {
        // 60 answered, as many as Routing keeps: each new one drops the
        // session's first from the list
        const first = Array.from({ length: 60 }, (_, i) => req(101 + i, "done"));
        const p = await open(browser, lang, first);
        try {
          for (const id of [161, 162, 163]) {
            await p.send(req(id, "start"));
            await p.send(req(id, "read"));
            const c = await p.cache();
            assert(c, `${id} under way: the cache is shown`);
            assert.equal(c.figure, pctOf(id - 1), `${id} under way: the last request's figure, not a dash`);
            assert.match(c.cls, /\bwas\b/, `${id} under way: dimmed as the last one's`);
            assert.equal(c.sub, p.last);
            await p.send(req(id, "done"));
            assert.equal((await p.cache()).figure, pctOf(id), `${id} done: its own figure`);
          }
          assert.deepEqual(p.errors, []);
        } finally { await p.context.close(); }
      });
      await t.test(`${lang}: opened while a request is under way`, async () => {
        const first = [req(101, "done"), req(102, "done"), req(103, "read")];
        const p = await open(browser, lang, first);
        try {
          await p.send(req(103, "read"));
          const c = await p.cache();
          assert(c, "the cache is shown");
          assert.equal(c.figure, pctOf(102), "the session's last answered request's figure");
          assert.match(c.cls, /\bwas\b/);
          assert.equal(c.sub, p.last);
          assert.deepEqual(p.errors, []);
        } finally { await p.context.close(); }
      });
    }
  });
}
