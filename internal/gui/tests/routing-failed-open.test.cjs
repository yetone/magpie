// Run with Node's test runner and Playwright on the module path; see README.md.
// A request the gateway weighed no seats for comes with "order": null (Go's
// nil slice), and clicking it in the Routing page's list did nothing: pick
// read r.order as a list and threw before the story was drawn (Discord,
// mythfish on v0.1.810: "workbuddy-ai/deepseek-v4.1-flash • 400 · 失败 6.5 秒",
// WorkBuddy AI: Invalid request parameters). Live and in a kept day, the
// failed request opens and its story gives the vendor's words.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const vendor = "WorkBuddy AI: Invalid request parameters";
const key = { id: "workbuddy-ai", provider: "workbuddy-ai", name: "WorkBuddy AI", kind: "provider", model: "deepseek-v4.1-flash" };
const base = (i) => ({ id: 100 - i, seq: 100 - i, time: at(i), agent: "codex", model: "workbuddy-ai/deepseek-v4.1-flash", provider: "workbuddy-ai", done: true });
// newest first: one that answered, then the 400 with no order, as the report's
const routes = [
  { ...base(0), order: [key], tries: [{ id: key.id, model: key.model, start: at(0), done: true, status: 200, ms: 900 }], status: 200, ms: 900 },
  { ...base(1), order: null, tries: [{ id: key.id, model: key.model, start: at(1), done: true, status: 400, ms: 6500, error: vendor }], status: 400, ms: 6500, error: vendor },
];

function serve(lang, live) {
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
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: live ? structuredClone(routes) : [] });
    }
    if (url.pathname === "/api/gateway/history") {
      const d = url.searchParams.get("day");
      return json({ cut: false, days: live ? [] : [{ day, requests: routes.length }], routes: d && !live ? structuredClone(routes) : [] });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], presets: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const want = {
  en: { head: "How the request at", said: "It said: " + vendor },
  zh: { head: "的请求是怎么路由的", said: "原话：" + vendor },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const live of [true, false]) {
      test(`${engine} ${lang} ${live ? "live" : "kept day"}: a failed request with no order opens`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const context = await browser.newContext({ viewport: { width: 1100, height: 760 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, live));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${live ? "live" : "day"}-failed-open.png`), fullPage: true });
          }
          await browser.close();
        });
        await page.goto("http://magpie.test/?view=routing");
        if (!live) await page.locator(".rt-day").nth(1).click();
        await page.locator(".rt-req").nth(routes.length - 1).waitFor();

        const row = page.locator(".rt-req").nth(1);
        assert.match(await row.textContent(), /400/);
        const was = await row.evaluate((e) => e.getBoundingClientRect().top);
        await row.click();
        await page.waitForTimeout(400);
        assert(Math.abs((await row.evaluate((e) => e.getBoundingClientRect().top)) - was) <= 1, "picking the request moved the page");
        assert.equal(await row.getAttribute("aria-pressed"), "true");
        assert(!(await page.locator(".rt-log").evaluate((e) => e.hidden)), "the story is hidden");
        assert((await page.locator(".rt-log-head").textContent()).includes(want[lang].head));
        const got = await page.locator(".rt-steps li").evaluateAll((ls) => ls.map((l) => [l.className, l.textContent]));
        assert.deepEqual(got.filter(([c]) => c === "aside said").map(([, s]) => s), [want[lang].said], JSON.stringify(got));

        // and back to the one that answered
        await page.locator(".rt-req").nth(0).click();
        await page.waitForTimeout(300);
        assert.equal(await page.locator(".rt-req").nth(0).getAttribute("aria-pressed"), "true");
        assert.deepEqual(errors, []);
      });
    }
  }
}
