// Run with Node's test runner and Playwright on the module path; see README.md.
// A Codex reset used by itself (the account's week used up, no one else to
// ask): the Routing page's story says whose reset was used and that the
// request was asked again, in the page's language, for a try that failed
// and was asked again and for Codex's own sign-in answering after it.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const key = { id: "codex-me", provider: "codex", name: "Codex", kind: "account", model: "gpt-5.5" };
const reset = { who: "me@example.com", text: "2 windows started again" };
const tries = [
  // the pool: out of its week, a reset used, asked again
  [{ id: key.id, model: key.model, start: at(0), done: true, status: 429, ms: 300, fail: "quota", reset },
   { id: key.id, model: key.model, start: at(0), done: true, status: 200, ms: 400 }],
  // Codex's own sign-in: the one try that answered after it
  [{ id: key.id, model: key.model, start: at(1), done: true, status: 200, ms: 500, reset }],
];
const routes = tries.map((tr, i) => ({
  id: 100 - i, seq: 100 - i, time: at(i), agent: "codex", model: "gpt-5.5", provider: "codex",
  order: [key], tries: tr, done: true, status: 200, ms: 700,
}));

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
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const want = {
  en: { again: "answered 429: its week is used up and nobody else could take the request, so one of me@example.com's Codex resets was used by itself and the request is asked again, before any of the reply reaches Codex.",
    first: "Its week was used up, so one of me@example.com's Codex resets was used by itself first." },
  zh: { again: "返回 429：本周额度已用完，且没有其他账号能接这个请求，于是自动使用了 me@example.com 的一张 Codex 重置卡并重新发送请求，此时 Codex 尚未收到任何回复。",
    first: "本周额度已用完，所以先自动使用了 me@example.com 的一张 Codex 重置卡。" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a Codex reset used by itself is in the story`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1100, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-auto-reset.png`), fullPage: true });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-day").nth(1).click();
      await page.locator(".rt-req").nth(routes.length - 1).waitFor();
      const steps = async () => page.locator(".rt-steps li").evaluateAll((ls) => ls.map((l) => l.textContent));

      await page.locator(".rt-req").nth(0).click();
      await page.waitForTimeout(300);
      let got = await steps();
      assert(got.some((s) => s.includes(want[lang].again)), JSON.stringify(got));
      assert(!got.some((s) => s.includes(want[lang].first)), JSON.stringify(got));

      await page.locator(".rt-req").nth(1).click();
      await page.waitForTimeout(300);
      got = await steps();
      assert(got.some((s) => s.startsWith(want[lang].first)), JSON.stringify(got));
      assert.deepEqual(errors, []);
    });
  }
}
