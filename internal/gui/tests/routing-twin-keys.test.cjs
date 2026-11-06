// Run with Node's test runner and Playwright on the module path; see README.md.
// iamleehom on Discord: a Routing chain had two providers each with keys
// named Key-01 and Key-02, all four serving gpt-6-astra, and the rows were
// alike (the same icon, key name and model), so which provider a row was
// of couldn't be told. A key or account named as one of another
// provider's on the stage says its provider after the name; a name only
// one provider has says nothing more, as before. Every language, both
// engines, a wide and a narrow width; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const ago = (s) => new Date(now.getTime() - s * 1e3).toISOString();
const M = "gpt-6-astra";
const key = (provider, name, k) => ({ id: `${provider}#${k}`, provider, name, icon: "openai", who: k, kind: "key", model: M });
const order = [
  key("relay-a", "Relay A", "Key-01"), key("relay-a", "Relay A", "Key-02"),
  key("relay-b", "Relay B", "Key-01"), key("relay-b", "Relay B", "Key-02"),
  key("solo", "Solo", "Key-09"),
];
const group = { id: "astra", name: "Astra", routing: "", members: ["relay-a/" + M, "relay-b/" + M, "solo/" + M] };
const first = { id: 1, seq: 1, time: ago(30), agent: "codex", model: "astra", provider: "relay-a", done: true, status: 200, ms: 900, tokens: 1200, group, order,
  tries: [{ id: order[0].id, model: M, start: ago(30), done: true, status: 200, ms: 900 }] };

function serve(lang) {
  return async (request) => {
    const url = new URL(request.request().url());
    const json = (data) => request.fulfill({ json: data });
    if (url.pathname === "/boot.js") return request.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return request.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: [first] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await request.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: two providers' keys of one name each say their provider`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 440]) {
        await t.test(`${lang} ${width}px`, async () => {
          const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
          t.after(() => context.close());
          const page = await context.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang));
          await page.goto("http://magpie.test/?view=routing");
          const rows = page.locator(".rt-accts > li").filter({ has: page.locator("b span.who") });
          await rows.nth(order.length - 1).waitFor();
          const seen = await rows.evaluateAll((ls) => ls.map((li) => {
            const b = li.querySelector("b"), sub = b.querySelector("span.who + span");
            return [b.querySelector("span.who").textContent, sub.textContent, sub.getBoundingClientRect().width];
          }));
          assert.deepEqual(seen.map(([n, s]) => [n, s]), [
            ["Key-01", "Relay A"], ["Key-02", "Relay A"], ["Key-01", "Relay B"], ["Key-02", "Relay B"], ["Key-09", ""],
          ]);
          if (width === 1100) for (const [n, s, w] of seen) if (s) assert.ok(w > 0, `${n} of ${s} is drawn`);
          // no row is alike another any more
          const texts = await rows.evaluateAll((ls) => ls.map((li) => li.querySelector("b").textContent));
          assert.equal(new Set(texts).size, texts.length, texts.join(" | "));
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "nothing runs off the page");
          assert.deepEqual(errors, []);
        });
      }
    }
  });
}
