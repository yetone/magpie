// Run with Node's test runner and Playwright on the module path; see README.md.
// #1859 (ouchxp): Routing picked while a request is being answered. The
// request's dot, let go at the account that answers, must sit where that
// account's wire ends, at its seat; it sat far off it, over the line under
// the stage, while the seat's own dot stayed plain.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const iso = (ms) => new Date(ms).toISOString();
const acct = { id: "acct-1", provider: "codex", name: "Codex", who: "user1@example.com", kind: "account", model: "gpt-6-astra", known: true, used: 10, plan: "Pro" };

function inFlight() {
  const t0 = Date.now() - 3000;
  return { id: 1, seq: 1, time: iso(t0), agent: "codex", model: acct.model, provider: acct.provider, order: [acct], tries: [{ id: acct.id, model: acct.model, start: iso(t0) }], done: false };
}

function serve(lang) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  const live = inFlight();
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) { await new Promise((r) => setTimeout(r, 1500)); }
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: url.searchParams.get("wait") ? [] : [live] }).catch(() => {});
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// where the held dot is, and where the wire to the answering seat ends, in
// the page's own pixels
const where = () => {
  const dot = document.querySelector(".rt-sky .pkt.held");
  const seat = [...document.querySelectorAll(".rt-accts li")].find((li) => li.classList.contains("on"));
  if (!dot || !seat) return null;
  const d = dot.getBoundingClientRect(), s = seat.getBoundingClientRect();
  return { dot: { x: d.left + d.width / 2, y: d.top + d.height / 2 }, seat: { l: s.left, t: s.top, b: s.bottom } };
};

const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];
const langs = ["en", "zh", "zh-TW", "ja", "de"];
const widths = [1100, 560];
for (const engine of engines) for (const lang of langs) for (const width of widths) {
  test(`${engine} ${lang} ${width}px: Routing picked mid-request holds the request's dot at the seat that answers it`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const context = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
    const page = await context.newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve(lang));
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-held-dot.png`) });
      }
      await browser.close();
    });
    // another view first, the request already under way
    await page.goto("http://magpie.test/?view=agents");
    await page.waitForTimeout(800);
    await page.locator('#nav button[data-view="routing"]').click();
    await page.waitForFunction(() => document.querySelector(".rt-sky .pkt.held"), null, { timeout: 6000 });
    await page.waitForTimeout(400);
    const atSeat = async (when) => {
      const w = await page.evaluate(where);
      assert(w, `${when}: the seat answering must be marked, with the request's dot held at it`);
      // beside it, a dot clear of the card, off its words (the sky is over
      // the cards: routing-sky-under)
      assert(w.dot.x < w.seat.l && w.seat.l - w.dot.x <= 10, `${when}: the held dot (x ${w.dot.x.toFixed(1)}) must sit at the seat's edge (x ${w.seat.l.toFixed(1)})`);
      assert(w.dot.y >= w.seat.t && w.dot.y <= w.seat.b, `${when}: the held dot (y ${w.dot.y.toFixed(1)}) must sit beside the seat (y ${w.seat.t.toFixed(1)}–${w.seat.b.toFixed(1)})`);
    };
    await atSeat("Routing picked");
    // the window resized while it waits: the stage goes the other layout,
    // and the dot goes with the seat
    await page.setViewportSize({ width: width > 560 ? 560 : 1100, height: 800 });
    await page.waitForTimeout(400);
    await atSeat("the window resized");
    assert.deepEqual(errors, []);
  });
}
