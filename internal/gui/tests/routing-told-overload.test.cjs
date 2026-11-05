// Run with Node's test runner and Playwright on the module path; see README.md.
// danzi002 on Discord: a Codex turn over ChatGPT accounts, the first
// with its allowance left answering 529 server_is_overloaded, the last
// out of its allowance. Codex was told the overload, and the request's
// row read "529 · 额度用尽" (quota used up): the status the overload's,
// the kind the last try's. The route says the kind of the failure the
// agent was told (fail), and the row says it: the servers overloaded,
// not a quota used up.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const said = `Codex: {"message":"Our servers are currently overloaded. Please try again later.","type":"service_unavailable_error","param":null,"code":"server_is_overloaded"}`;
const a = { id: "codex@a@example.com", provider: "codex", name: "Codex", kind: "account", who: "a@example.com", model: "gpt-5.5" };
const b = { id: "codex@b@example.com", provider: "codex", name: "Codex", kind: "account", who: "b@example.com", model: "gpt-5.5" };
const routes = [{
  id: 201, seq: 201, time: at(0), agent: "codex", model: "gpt-5.5", provider: "codex", order: [a, b],
  tries: [
    { id: a.id, model: a.model, start: at(0), done: true, status: 529, ms: 900, fail: "other", error: said,
      rest: { status: 529, why: "other", by: "cooldown", model: a.model, until: new Date(Date.parse(at(0)) + 30e3).toISOString() } },
    { id: b.id, model: b.model, start: at(0), done: true, status: 429, ms: 200, fail: "quota", earlier: true,
      error: "Codex: The usage limit has been reached" },
  ],
  done: true, status: 529, error: said, fail: "overloaded", ms: 1200,
}];

function serve(lang) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") {
      const d = url.searchParams.get("day");
      return json({ cut: false, days: [{ day, requests: routes.length }], routes: d ? routes : [] });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// busy: the overload's word; quota: the used-up allowance's, which the
// row must not say
const want = {
  en: { busy: "servers overloaded", quota: "quota used up" },
  zh: { busy: "服务器过载", quota: "额度用尽" },
  "zh-TW": { busy: "伺服器過載", quota: "額度用盡" },
  ja: { busy: "サーバー過負荷", quota: "クォータ切れ" },
  de: { busy: "Server überlastet", quota: "Kontingent aufgebraucht" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(want)) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: a request told an earlier try's overload reads overloaded, not quota used up`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-told-overload.png`), fullPage: true });
          }
          await browser.close();
        });
        await page.goto("http://magpie.test/?view=routing");
        await page.locator(".rt-days .rt-day").nth(1).click();
        const row = page.locator(".rt-req").first();
        await row.waitFor();
        const w = want[lang];
        const text = await row.textContent();
        assert(text.includes("529") && text.includes(w.busy), text);
        assert(!text.includes(w.quota), text);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "the page scrolls sideways");
        assert.deepEqual(errors, []);
      });
    }
  }
}
