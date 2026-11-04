// Run with Node's test runner and Playwright on the module path; see README.md.
// A conversation kept on a relay for what its vendor cached of it, whose
// relay answered an overloaded 503, is asked of that relay once more before
// it moves (jiabubu on Discord). The Routing page's story says why it was
// asked again though another was left, in the page's language: the text for
// the last one left said "nobody else is left to ask", which was untrue here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const vendor = "Overloaded";
const relay = { id: "relay", provider: "relay", name: "Relay", kind: "provider", model: "claude-sonnet-4-6" };
const other = { id: "other", provider: "other", name: "Other", kind: "provider", model: "claude-sonnet-4-6" };
const routes = [{
  id: 100, seq: 100, time: at(0), agent: "claude", model: "group/g", provider: "relay", order: [relay, other],
  affinity: { mode: "", turn: 3, last: "relay", lastTurn: 2, at: at(1), cacheRead: 42000, cacheWrite: 1800, kept: true, why: "cache" },
  tries: [
    { id: relay.id, model: relay.model, start: at(0), done: true, status: 503, ms: 40, fail: "other", error: vendor, again: 1000, kept: true },
    { id: relay.id, model: relay.model, start: at(0), done: true, status: 200, ms: 500 },
  ],
  done: true, status: 200, ms: 1600,
}, {
  id: 99, seq: 99, time: at(1), agent: "claude", model: "relay/claude-sonnet-4-6", provider: "relay", order: [relay],
  tries: [{ id: relay.id, model: relay.model, start: at(1), done: true, status: 200, ms: 500 }], done: true, status: 200, ms: 500,
}];

function serve(lang) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/settings.json", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
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
  en: { story: "for what its vendor cached of it, so it is asked once more in", left: "nobody else is left to ask" },
  zh: { story: "会话因其供应商缓存的内容而保持在 Relay", left: "且无其他可问" },
  "zh-TW": { story: "會話因其供應商快取的內容而保持在 Relay", left: "且無其他可問" },
  ja: { story: "ベンダーがキャッシュした内容のため会話は Relay", left: "他に問い合わせる相手もいません" },
  de: { story: "Das Gespräch bleibt bei Relay", left: "es bleibt niemand sonst zum Fragen" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(want)) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: a kept conversation's retry says why it stays`, async (t) => {
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
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-kept-again.png`), fullPage: true });
          }
          await browser.close();
        });
        await page.goto("http://magpie.test/?view=routing");
        await page.locator(".rt-days .rt-day").nth(1).click();
        await page.locator(".rt-req").nth(routes.length - 1).waitFor();
        const row = page.locator(".rt-req").nth(0);
        await row.click();
        await page.waitForTimeout(400);
        const steps = await page.locator(".rt-steps li").allTextContents();
        const story = steps.find((s) => s.includes(want[lang].story));
        assert(story, JSON.stringify(steps));
        assert(story.includes("503"), story);
        assert(!steps.some((s) => s.includes(want[lang].left)), JSON.stringify(steps));
        assert.deepEqual(errors, []);
      });
    }
  }
}
