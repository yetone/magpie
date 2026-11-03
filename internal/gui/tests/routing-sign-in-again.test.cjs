// Run with Node's test runner and Playwright on the module path; see README.md.
// adorable_piglet on Discord: the Routing page's Codex card said "2 需要重新登录"
// (sign-in required) on an account the editor showed in use, with nowhere
// to sign it in again. Now an account refused for its sign-in and not
// answered since has Sign in again on its card, which signs it in again
// on the Providers page as the editor does; one that has answered since
// says its refusals were before that, with no button. Both engines, wide
// and narrow, every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const seat = (who) => ({ id: "codex@" + who, provider: "codex", name: "Codex", who, kind: "account", agent: "codex", model: "gpt-6.1-sol" });
const tallies = {
  // refused at 10:45, last answered at 10:41
  '["codex","stale@example.com"]': { ...seat("stale@example.com"), tried: 14, ok: 8, fails: { auth: 2, other: 4 }, last: now - 600000, authAt: now - 360000 },
  // refused, then answered
  '["codex","fine@example.com"]': { ...seat("fine@example.com"), tried: 10, ok: 8, fails: { auth: 2 }, last: now - 60000, authAt: now - 360000 },
};
// one request the gateway still keeps: the fine account answering it
const iso = (ms) => new Date(ms).toISOString();
const routes = [{ id: 100, seq: 100, time: iso(now - 61000), agent: "codex", model: "gpt-6.1-sol", provider: "codex", done: true, status: 200, ms: 300,
  order: [seat("fine@example.com")], tries: [{ id: "codex@fine@example.com", model: "gpt-6.1-sol", start: iso(now - 61000), done: true, ms: 250, status: 200 }] }];
const words = {
  en: ["2 sign-in required, 4 failed", "Sign in again", "2 sign-in refused (answered since)"],
  zh: ["2 需要重新登录, 4 失败", "重新登录", "2 登录曾被拒（之后已应答）"],
  "zh-TW": ["2 需要重新登入, 4 失敗", "重新登入", "2 登入曾被拒（之後已應答）"],
  ja: ["2 サインインが必要です, 4 失敗", "再度サインイン", "2 サインイン拒否（その後は応答あり）"],
  de: ["2 Anmeldung erforderlich, 4 fehlgeschlagen", "Erneut anmelden", "2 Anmeldung abgelehnt (seitdem beantwortet)"],
};

function serve(lang, signins) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light", quotaLeft: false } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 100, totals: { requests: 24, rerouted: 0, errors: 8 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [{ day: new Date(now).toISOString().slice(0, 10), requests: 24 }], routes: [], tallies });
    if (url.pathname === "/api/signin" && r.request().method() === "POST") {
      signins.push(r.request().postDataJSON());
      return json({ id: "s1", agent: "codex", state: "waiting", linkOnly: true });
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: an account refused for its sign-in is signed in again from its card`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const width of [1100, 420]) for (const lang of Object.keys(words)) {
      await t.test(`${lang} at ${width}px`, async () => {
        const ctx = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
        try {
          const page = await ctx.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [], signins = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, signins));
          await page.goto("http://magpie.test/?view=routing");
          const [needs, again, past] = words[lang];
          const stale = page.locator(".rt-act", { hasText: "stale@example.com" }).first();
          await stale.locator(".tally .bad", { hasText: needs }).waitFor();
          // answered since: its refusals are past, and nothing to do
          const fine = page.locator(".rt-act", { hasText: "fine@example.com" }).first();
          assert.equal(await fine.locator(".tally .bad").textContent(), past);
          assert.equal(await fine.locator("button").count(), 0);
          // not answered since: Sign in again, where the card says it
          const b = stale.locator("button.rt-signin");
          assert.equal(await b.textContent(), again);
          const box = await b.boundingBox();
          assert.ok(box && box.x >= 0 && box.x + box.width <= width, `the button is on the screen at ${width}px: ${JSON.stringify(box)}`);
          await b.click();
          await page.locator("#view-providers").waitFor({ state: "visible" });
          for (let i = 0; i < 50 && !signins.length; i++) await page.waitForTimeout(100);
          // the account the card is of (a page served remotely signs Codex
          // in by its device code, as the editor's button does)
          assert.deepEqual(signins.map((b) => [b.agent, b.user]), [["codex", "stale@example.com"]]);
          assert.deepEqual(errors, []);
        } finally {
          await ctx.close();
        }
      });
    }
  });
}
