// Run with Node's test runner and Playwright on the module path; see README.md.
// A reply that broke off after it began (#733: a stream that sent its 200
// and some text, then failed) is told as failed everywhere the Routing
// page says how a request went: its row is bad, not a green dot that
// answered; the "errors your agent saw" stat picks it, as the counter
// counts it; and its story says it began answering and broke off. The
// tryOk rule (status < 400 && no fail) was only put through part of the
// page, so the row and the stat still judged by the status alone.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const key = { id: "codex", provider: "openai", name: "Codex", kind: "provider", model: "gpt-6-luna" };
// newest first: the request that broke off, then ordinary answers (a few
// rows, so the stage above holds still)
const broke = {
  id: 100, seq: 100, time: at(0), agent: "codex", model: "openai/gpt-6-luna", provider: "openai", error: "Unable to reach the model provider",
  order: [key], tries: [{ id: key.id, model: key.model, start: at(0), done: true, status: 200, fail: "other", ms: 24360, ttft: 24359 }],
  done: true, status: 200, ms: 24360, ttft: 24359, tokens: 12000, out: 200,
};
const answered = (id, i) => ({
  id, seq: id, time: at(i), agent: "codex", model: "openai/gpt-6-luna", provider: "openai",
  order: [key], tries: [{ id: key.id, model: key.model, start: at(i), done: true, status: 200, ms: 3000, ttft: 300 }],
  done: true, status: 200, ms: 3000, ttft: 300, tokens: 3000, out: 100,
});
const noted = {
  id: 101, seq: 101, time: at(0), agent: "codex", model: "openai/gpt-6-luna", provider: "openai", error: "Codex titles are off in magpie's Settings, so magpie answered it itself",
  order: [key], tries: [{ id: key.id, model: key.model, start: at(0), done: true, status: 200, ms: 5, ttft: 5 }],
  done: true, status: 200, ms: 5, ttft: 5, tokens: 30, out: 20,
};
const routes = [noted, broke, answered(98, 2), answered(97, 3), answered(96, 4)];

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
  en: { broke: /began answering, then broke off/ },
  zh: { broke: /已开始回答，随后中断/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a reply that broke off is told as failed everywhere`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1100, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(15000); // webkit launches are slow here, and slower again later in a run
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      t.after(async () => { await browser.close(); });
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-day").first().waitFor(); // the view is drawn
      await page.locator(".rt-day:visible").nth(1).click(); // Live, then the day with the requests
      const first = page.locator(".rt-req").nth(1); // the broken-off one, under the newer noted 200
      await first.waitFor();
      // the row is bad, not a green dot that answered (the old rule said "ok": status 200)
      const cls = await first.getAttribute("class");
      assert(cls.includes("bad"), `row class ${cls}`);
      assert(!cls.includes("ok") && !cls.includes("moved"), `row class ${cls}`);
      // the stat's click picks it, over the noted 200 that answers and is newer
      await page.locator(".rt-errs").click();
      await page.waitForTimeout(300);
      assert.equal(await first.getAttribute("aria-pressed"), "true", "the errors stat picks a 200 with a note of its own, not the broken-off request");
      assert.equal(await page.locator(".rt-req").first().getAttribute("aria-pressed"), "false", "the newer noted 200 was picked");
      // its story says it began answering and broke off
      const story = await page.locator(".rt-steps").textContent();
      assert.match(story, want[lang].broke, story);
      assert.deepEqual(errors, []);
    });
  }
}

// A 200 whose error is magpie's own note — Codex's titles turned off in
// Settings, a title reply with no title in it — answered: its try has no
// fail. The rule reads the tries, not the route's error, so the row stays
// a green dot rather than turning every titled thread's first request red
// (yetone, reviewing #772).
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a 200 with a note of its own is still an answer`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1100, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(15000); // webkit launches are slow here, and slower again later in a run
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      t.after(async () => { await browser.close(); });
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-day").first().waitFor(); // the view is drawn
      await page.locator(".rt-day:visible").nth(1).click();
      const first = page.locator(".rt-req").first(); // the noted 200, newer than the broken-off one
      await first.waitFor();
      const cls = await first.getAttribute("class");
      assert(cls.includes("ok"), `row class ${cls}`);
      assert(!cls.includes("bad"), `row class ${cls}`);
      assert.deepEqual(errors, []);
    });
  }
}
