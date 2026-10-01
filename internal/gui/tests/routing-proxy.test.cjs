// Run with Node's test runner and Playwright on the module path; see README.md.
// A group member whose proxy wasn't listening (#381: 127.0.0.1:1082 down,
// "proxyconnect tcp: … connection refused") hands the request to the next:
// the Routing page's story says the request never reached it and that it
// doesn't rest, in the page's language, rather than "an error another
// account wouldn't fix".
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const vendor = 'Grok (SuperGrok): Post "https://cli-chat-proxy.grok.com/v1/responses": proxyconnect tcp: dial tcp 127.0.0.1:1082: connect: connection refused';
const grok = { id: "grok", provider: "grok", name: "Grok (SuperGrok)", kind: "provider", model: "grok-4.7-build-fast" };
const cursor = { id: "cursor", provider: "cursor", name: "Cursor", kind: "provider", model: "grok-4.7-fast" };
const routes = [{
  id: 100, seq: 100, time: at(0), agent: "codex", model: "group/grok", provider: "cursor", order: [grok, cursor],
  tries: [
    { id: grok.id, model: grok.model, start: at(0), done: true, status: 502, ms: 3, fail: "proxy", error: vendor },
    { id: cursor.id, model: cursor.model, start: at(0), done: true, status: 200, ms: 800 },
  ],
  done: true, status: 200, ms: 900, fallback: vendor,
}, {
  id: 99, seq: 99, time: at(1), agent: "codex", model: "group/grok", provider: "grok", order: [grok, cursor],
  tries: [{ id: grok.id, model: grok.model, start: at(1), done: true, status: 200, ms: 500 }], done: true, status: 200, ms: 500,
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
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const want = {
  en: { story: "the proxy magpie goes through didn't take the connection, so the request never reached the vendor and goes on to the next", rest: "so it doesn't rest", tag: "502 · proxy not reachable" },
  zh: { story: "magpie 所走的代理没有接受连接，请求根本没到厂商，所以转给了下一个", rest: "所以不用休息", tag: "502 · 代理连不上" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a member whose proxy is down goes on to the next`, async (t) => {
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
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-proxy.png`), fullPage: true });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-day").nth(1).click();
      await page.locator(".rt-req").nth(routes.length - 1).waitFor();

      const row = page.locator(".rt-req").nth(0);
      const was = await row.evaluate((e) => e.getBoundingClientRect().top);
      await row.click();
      await page.waitForTimeout(400);
      assert(Math.abs((await row.evaluate((e) => e.getBoundingClientRect().top)) - was) <= 1, "picking the request moved the page");
      const steps = await page.locator(".rt-steps li").allTextContents();
      const story = steps.find((s) => s.includes(want[lang].story));
      assert(story, JSON.stringify(steps));
      assert(story.includes(want[lang].rest) && story.includes("Grok (SuperGrok)"), story);
      assert(!steps.some((s) => s.includes("wouldn't fix") || s.includes("也解决不了")), JSON.stringify(steps));
      const text = await page.locator("body").innerText();
      assert(text.includes(want[lang].tag), "no tag " + want[lang].tag);
      assert.deepEqual(errors, []);
    });
  }
}
