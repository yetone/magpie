// Run with Node's test runner and Playwright on the module path; see README.md.
// A request its agent canceled (#2398: a person stopped the reply, or sent
// the next message while it came) saw no error. Its Routing row is told
// apart from a failure, with no red mark, and from an answer, with no
// green dot: the row is "gone", its dot muted. The errors stat doesn't
// count it.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const key = { id: "claude", provider: "anthropic", name: "Claude", kind: "provider", model: "claude-opus-5-5" };
const canceled = {
  id: 100, seq: 100, time: at(0), agent: "claude", model: "anthropic/claude-opus-5-5", provider: "anthropic", error: "the agent canceled the request",
  order: [key], tries: [{ id: key.id, model: key.model, start: at(0), done: true, status: 499, gone: true, ms: 4000, ttft: 900 }],
  done: true, status: 499, canceled: true, ms: 4000, ttft: 900, tokens: 9000, out: 120,
};
const answered = (id, i) => ({
  id, seq: id, time: at(i), agent: "claude", model: "anthropic/claude-opus-5-5", provider: "anthropic",
  order: [key], tries: [{ id: key.id, model: key.model, start: at(i), done: true, status: 200, ms: 3000, ttft: 300 }],
  done: true, status: 200, ms: 3000, ttft: 300, tokens: 3000, out: 100,
});
const routes = [canceled, answered(98, 2), answered(97, 3), answered(96, 4)];

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

const langs = ["en", "zh", "zh-TW", "ja", "de"];
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of langs) {
    test(`${engine} ${lang}: a request its agent canceled is neither failed nor answered`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1100, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(15000); // webkit launches are slow here, and slower again later in a run
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      t.after(async () => { await browser.close(); });
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-day").first().waitFor(); // the view is drawn
      await page.locator(".rt-day:visible").nth(1).click(); // Live, then the day with the requests
      const row = page.locator(".rt-req").first();
      await row.waitFor();
      const cls = await row.getAttribute("class");
      assert(cls.split(" ").includes("gone"), `row class ${cls}`);
      assert(!/\b(bad|ok|moved)\b/.test(cls), `row class ${cls}`);
      const dot = row.locator(".to i").first();
      const [bg, muted, green, red, mask] = await dot.evaluate((i) => {
        const probe = (v) => { const s = document.createElement("span"); s.style.color = `var(${v})`; document.body.append(s); const c = getComputedStyle(s).color; s.remove(); return c; };
        const cs = getComputedStyle(i);
        return [cs.backgroundColor, probe("--muted"), probe("--green"), probe("--red"), cs.maskImage || cs.webkitMaskImage || "none"];
      });
      assert.equal(bg, muted, `the dot is ${bg}: green ${green}, red ${red}`);
      assert.equal(mask, "none", "a canceled row carries a failure's x mark");
      // no speed is told for a reply its agent stopped
      assert.doesNotMatch(await row.textContent(), /tok\/s/);
      assert.deepEqual(errors, []);
    });
  }
}
