// Run with Node's test runner and Playwright on the module path; see README.md.
// The Routing page in a narrow window (Jiabubu on Discord): the Requests and
// the Accounts and keys go one under the other, and each account or key then
// took a whole line, so a page of many accounts ran on far down. Under the
// requests the accounts sit several to a line, each provider's heading across
// them; beside the requests (1150px and wider) the column still holds one.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const now = new Date();
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const renew = new Date(now.getTime() + 3 * 864e5).toISOString();
const acct = (n, who, plan) => ({ id: "codex:" + n, provider: "codex", name: "Codex", who, plan, kind: "account", model: "gpt-5.6-terra", known: true, used: 10 + n, renews: [renew], icon: "codex-color", routing: "order" });
const codex = [acct(1, "ann@example.com", "plus"), acct(2, "bob@example.com", "pro"), acct(3, "cat@example.com", "plus"), acct(4, "dan@example.com", "team")];
const ds = { id: "deepseek", provider: "deepseek", name: "DeepSeek", kind: "provider", model: "deepseek-flash", icon: "deepseek-color", routing: "order" };
const mk = (i, w, model) => ({
  id: 100 - i, seq: 100 - i, time: at(i), agent: "codex", model, provider: w.provider,
  order: w.provider === "codex" ? codex : [w], done: true, status: 200, ms: 4000, tokens: 9000,
  tries: [{ id: w.id, model: w.model, start: at(i), done: true, status: 200, ms: 4000 }],
});
const routes = [...codex.map((w, i) => mk(i, w, "gpt-5.6-terra")), mk(4, ds, "deepseek-flash")];

function serve(lang) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/t", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  const assets = path.resolve(__dirname, "../assets");
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 100, totals: { requests: routes.length, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: narrow, the accounts sit several to a line`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage({ viewport: { width: 700, height: 1200 } });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      t.after(() => browser.close());
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-act", { hasText: "dan@example.com" }).waitFor();

      // the account rows' boxes: how many share the first one's line
      const rows = () => page.locator(".rt-acts > .rt-act:not(.rt-keys)").evaluateAll((es) => es.map((e) => {
        const b = e.getBoundingClientRect();
        return { top: Math.round(b.top), left: b.left, right: b.right, text: e.textContent };
      }));
      const settle = () => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(() => setTimeout(r, 100)))));
      for (const width of [700, 560, 420]) {
        await page.setViewportSize({ width, height: 1200 });
        await settle();
        const reqs = await page.locator(".rt-reqs").boundingBox(), acts = await page.locator(".rt-acts").boundingBox();
        assert(acts.y >= reqs.y + reqs.height - 1, `${width}px: the accounts under the requests`);
        const rs = (await rows()).filter((r) => r.text.includes("@example.com"));
        assert.equal(rs.length, 4, `${width}px: each Codex account drawn`);
        const shared = rs.filter((r) => Math.abs(r.top - rs[0].top) <= 1);
        assert(shared.length >= 2, `${width}px: two accounts share a line, not one each: ${JSON.stringify(rs.map((r) => [r.top, Math.round(r.left)]))}`);
        assert(shared[1].left >= shared[0].right - 1, `${width}px: side by side`);
        // the open "4 accounts" row heads them, across its line, as a provider's heading does
        const sum = await page.locator(".rt-acts > .rt-keys.open").evaluate((e) => e.getBoundingClientRect().width / e.parentElement.clientWidth);
        assert(sum > 0.95, `${width}px: the accounts' summary row across the list: ${sum}`);
        // each provider's heading still across its own line
        const heads = await page.locator(".rt-acts > .rt-prov").evaluateAll((es) => es.map((e) => e.getBoundingClientRect().width / e.parentElement.clientWidth));
        assert(heads.length >= 2 && heads.every((f) => f > 0.95), `${width}px: headings across the list: ${heads}`);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `${width}px: no horizontal overflow`);
        const out = await page.locator(".rt-act .nm b").evaluateAll((es) => es.some((e) => e.getBoundingClientRect().right > e.closest(".rt-act").getBoundingClientRect().right + 1));
        assert.equal(out, false, `${width}px: names stay inside their account`);
      }
      // beside the requests the column holds one to a line, as before
      await page.setViewportSize({ width: 1440, height: 1200 });
      await settle();
      const wide = (await rows()).filter((r) => r.text.includes("@example.com"));
      assert.equal(new Set(wide.map((r) => r.top)).size, 4, "wide: one account to a line");
      assert.deepEqual(errors, []);
    });
  }
}
