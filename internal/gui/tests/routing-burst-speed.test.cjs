// Run with Node's test runner and Playwright on the module path; see README.md.
// A reply that came in one burst at its end tells no speed in the Routing
// page's story (#731, Arcadia822: a Gemini turn of one write tool call, 8264
// tokens whose first content came 1 ms before the end, read 8,264,000
// tok/s); its first token still shows, and an ordinary reply keeps its
// speed.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const key = { id: "antigravity", provider: "antigravity", name: "Antigravity", kind: "provider", model: "gemini-3.8-flash" };
// newest first: the report's burst, a 500 ms burst, then ordinary replies
// (100 tok/s and 50 tok/s; a few rows, so the stage above holds still)
// and last, a reply whose first words came at 5.1 s and the rest held back
// and sent at once (John on Discord: Kimi Code, 1,367 tok/s): its flow says
// its content took 2 ms to come
// Then John's rows after that fix (Kimi Code again: 2,237 and 2,012 tok/s),
// a burst let go over 236 ms and 196 ms: 550 tokens, 22 of them reasoning,
// in 16 s with the first content at 5.2 s, its text early (5.4 s) and
// with the burst (15.764 s); 395 tokens in 19 s. And a steady 50 tok/s
// answer after the same reasoning, which keeps its speed.
// [out, ms, ttft, flow, reasoning, firstText]
const timing = [[8264, 24360, 24359], [8264, 1500, 1000], [1200, 15000, 3000], [100, 3000, 1000], [100, 3000, 1000], [100, 3000, 1000], [2187, 6700, 5100, 2],
  [550, 16000, 5200, 236, 22, 5400], [550, 16000, 5200, 236, 22, 15764], [395, 19000, 5200, 196], [552, 16000, 5200, 10600, 22, 5400]];
const routes = timing.map(([out, ms, ttft, flow, reasoning, firstText], i) => ({
  id: 100 - i, seq: 100 - i, time: at(i), agent: "codex", model: "antigravity/gemini-3.8-flash", provider: "antigravity",
  order: [key], tries: [{ id: key.id, model: key.model, start: at(i), done: true, status: 200, ms, ttft, flow, firstText }],
  done: true, status: 200, ms, ttft, flow, firstText, reasoning, tokens: out + 4087, out,
}));

function serve(lang) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") {
      const d = url.searchParams.get("day");
      return json({ cut: false, days: [{ day, requests: routes.length }], routes: d ? routes : [] });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const want = {
  en: { first: /first token in 24 s/, speed: /tok\/s/, normal: /· 100 tok\/s/, slow: /· 50 tok\/s/ },
  zh: { first: /24 秒 后首响/, speed: /token\/秒/, normal: /· 100 token\/秒/, slow: /· 50 token\/秒/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a reply that came in one burst tells no speed`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1100, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      t.after(() => browser.close());
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-days .rt-day").nth(1).click();
      await page.locator(".rt-req").nth(timing.length - 1).waitFor();

      const story = async (i) => {
        const row = page.locator(".rt-req").nth(i);
        // Taller metric rows need the list scrolled first, with real input
        // so the page's script-scroll protection keeps the same position.
        const rb = await row.boundingBox(), lb = await page.locator(".rt-reqs").boundingBox();
        if (rb.y + rb.height > lb.y + lb.height) {
          await page.locator(".rt-reqs").hover();
          await page.mouse.wheel(0, rb.y + rb.height - lb.y - lb.height + 6);
          await page.waitForTimeout(200);
        }
        const was = await row.evaluate((e) => e.getBoundingClientRect().top);
        const scrolled = await page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]);
        await row.click();
        await page.waitForTimeout(300);
        assert(Math.abs((await row.evaluate((e) => e.getBoundingClientRect().top)) - was) <= 1, "picking the request moved the page");
        assert.deepEqual(await page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]), scrolled);
        return page.locator(".rt-steps").textContent();
      };
      // the report: its first token, and no speed
      const burst = await story(0);
      assert.match(burst, want[lang].first);
      assert.doesNotMatch(burst, want[lang].speed);
      assert.doesNotMatch(burst, /8264000|8,264,000/);
      // half a second for 8264 tokens is a burst all the same
      assert.doesNotMatch(await story(1), want[lang].speed);
      // ordinary replies keep theirs
      assert.match(await story(2), want[lang].normal);
      assert.match(await story(3), want[lang].slow);
      // a burst after its first words: no speed, not 1,367 tok/s
      const held = await story(6);
      assert.doesNotMatch(held, want[lang].speed);
      assert.doesNotMatch(held, /1367|1,367/);
      // a burst let go over 236 ms after its text, or with it, and over
      // 196 ms: no speed, not 2,237 or 2,012 tok/s
      for (const [i, n] of [[7, /2237|2,237/], [8, /2237|2,237/], [9, /2015|2,015|2012|2,012/]]) {
        const s = await story(i);
        assert.doesNotMatch(s, want[lang].speed, `row ${i}: ${s}`);
        assert.doesNotMatch(s, n);
      }
      // a steady answer after the same reasoning keeps its 50 tok/s
      assert.match(await story(10), want[lang].slow);
      assert.deepEqual(errors, []);
    });
  }
}
