// Run with Node's test runner and Playwright on the module path; see README.md.
// A session's name, once the Routing page has it, stays when a later poll of
// /api/gateway/session-titles answers without it (#5365, Flandern1211: a
// ZCode card went back to its folder when a slow read came back empty). In
// both engines, every language, at 1100 and 420. No backend: the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function fixture() {
  const now = Date.now(), at = (s) => new Date(now - s * 1000).toISOString();
  const seat = { id: "relay", provider: "relay", name: "Relay", who: "team", kind: "key", model: "m1" };
  return [{ id: 1, seq: 1, time: at(2), agent: "claude", session: "s-zc", folder: "work/zc",
    model: "relay/m1", provider: "relay", order: [seat], tries: [{ id: seat.id, model: "m1", start: at(2), done: true, status: 200, ms: 500 }],
    done: true, status: 200, ms: 500, tokens: 1000 }];
}

function serve(lang, polls) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "claude", name: "Claude Code", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.has("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: fixture() });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/gateway/session-titles") {
      const body = route.request().postDataJSON() || {};
      if (!(body.sessions || []).length) return json({});
      // the first poll names the session; every one after answers without it
      polls.n++;
      return json({ titles: polls.n === 1 ? { "claude:s-zc": "Port the parser" } : {} });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: routing keeps a session's name when a later poll answers without it`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const errors = [], polls = { n: 0 };
        const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, polls));
        try { await page.addInitScript(() => localStorage.setItem("magpie.routingBySession", "1")); } catch {}
        await page.goto("http://magpie.test/?view=routing");
        const name = page.locator(".rt-session .nm").first();
        await page.waitForFunction(() => document.querySelector(".rt-session .nm")?.textContent.includes("Port the parser"));

        // two more polls (the page asks again on focus, as it does every
        // 15s), each answering without the name: the third is made only
        // once the second's answer was taken in and drawn
        const deadline = Date.now() + 10000;
        while (polls.n < 3 && Date.now() < deadline) {
          await page.evaluate(() => window.dispatchEvent(new Event("focus")));
          await page.waitForTimeout(50);
        }
        assert(polls.n >= 3, `the page asked again: ${polls.n} polls`);
        assert.equal(await name.textContent(), "Claude Code · Port the parser");
        assert.deepEqual(errors, []);
      });
    }
  }
}
