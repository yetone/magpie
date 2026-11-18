// Run with Node's test runner and Playwright on the module path; see README.md.
// A ring named for a group and its span (tongzhenbao on X): agy-cli 0.1.91
// named its windows "Gemini · 7 days", "Gemini · 5 hours", "Claude & GPT ·
// 7 days" with no pool, and the menu bar panel drew each on one line, cut
// to "Gemini …", "Gemini …", "Claude …", two rings no one could tell apart.
// Such a ring reads as a pool's does, its first word over its span
// (Gemini / 7d), and so MiniMax's "Video · 5 hours". agy-cli 0.1.92 sends
// its pools as pools, and its models' windows when agy listed them: the
// Usage page's "Every model" lists them; with no model named it shows no
// "Every model (0)". English and Chinese, Chromium and WebKit; the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (h) => new Date(Date.now() + h * 36e5).toISOString();
const acct = (provider, name, user, windows) => ({ provider, name, icon: "", user, plan: "Pro", windows });
// agy-cli 0.1.91: a group's window named with its span, no pool
const old = acct("agy-cli", "AGY-CLI", "old@example.com", [
  { name: "Gemini · 7 days", used: 100, resetsAt: at(100), aside: true },
  { name: "Gemini · 5 hours", used: 40, resetsAt: at(2), aside: true },
  { name: "Claude & GPT · 7 days", used: 100, resetsAt: at(90), aside: true },
]);
// agy-cli 0.1.92 with agy's models listed: the models' windows, then the pools'
const model = (name, family, pool, used) => ({ name, family, pool, used, resetsAt: at(100), aside: true });
const pool = (pool, name, used, h) => ({ name, pool, used, resetsAt: at(h), aside: true });
const listed = acct("agy-cli", "AGY-CLI", "new@example.com", [
  model("Claude Opus 5.5", "Claude", "Claude & GPT", 66), model("Gemini 3.8 Flash", "Gemini", "Gemini", 20),
  model("GPT-OSS 120B", "GPT-OSS", "Claude & GPT", 66),
  pool("Gemini", "7 days", 20, 100), pool("Gemini", "5 hours", 10, 2), pool("Claude & GPT", "7 days", 66, 90),
]);
// the same before agy listed its models: pools alone
const bare = acct("agy-cli", "AGY-CLI", "bare@example.com", [
  pool("Gemini", "7 days", 20, 100), pool("Gemini", "5 hours", 10, 2), pool("Claude & GPT", "7 days", 66, 90),
]);
const minimax = acct("minimax", "MiniMax", "mm@example.com", [
  { name: "5 hours", used: 30, resetsAt: at(2) },
  { name: "Video · 1 days", used: 50, resetsAt: at(20), aside: true },
]);
const quotas = [old, listed, bare, minimax];

function serve(lang, panel) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${!panel}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = { en: { every: "Every model (3)" }, zh: { every: "全部模型（3）" } };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a ring named for a group and its span reads both`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const pages = [];
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          for (const [name, p] of pages) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-quota-ring-names-${name}.png`) });
        }
        await browser.close();
      });
      const errors = [];
      const open = async (name, url, viewport) => {
        const page = await (await browser.newContext({ viewport, reducedMotion: "reduce" })).newPage();
        pages.push([name, page]);
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, url.includes("mode=panel")));
        await page.goto(url);
        return page;
      };

      // the menu bar panel: each ring its group over its span, whole
      const panel = await open("panel", "http://magpie.test/?mode=panel", { width: 440, height: 900 });
      await panel.locator('#ptabs [data-ptab="usage"]').click();
      for (const more of await panel.locator(".pq-group .pq-more").all()) await more.click();
      const card = (user) => panel.locator(".pq-card", { hasText: user });
      await card("old@example.com").locator(".pq-ring").first().waitFor();
      const labels = (user) => card(user).locator(".pq-rn").evaluateAll((es) => es.map((e) => [...e.children].map((c) => c.textContent)));
      assert.deepEqual(await labels("old@example.com"), [["Gemini", "7d"], ["Gemini", "5h"], ["Claude", "7d"]]);
      assert.deepEqual(await labels("new@example.com"), [["Gemini", "5h"], ["Gemini", "7d"], ["Claude", "7d"]]);
      assert.deepEqual(await labels("bare@example.com"), [["Gemini", "5h"], ["Gemini", "7d"], ["Claude", "7d"]]);
      assert.deepEqual((await labels("mm@example.com"))[1], ["Video", "1d"]);
      for (const user of ["old@example.com", "new@example.com", "mm@example.com"]) {
        const fit = await card(user).locator(".pq-rn > span").evaluateAll((es) => es.every((e) => e.scrollWidth <= e.clientWidth));
        assert(fit, `${user}: the ring labels fit`);
      }
      // the reset line names the ring as the ring does
      const say = await card("old@example.com").locator(".pq-say").textContent();
      assert(say.includes("Gemini 5h"), "the second deadline is named: " + say);
      // the tooltip keeps the whole name
      assert((await card("old@example.com").locator(".pq-ring").first().getAttribute("title")).startsWith("Gemini · 7 days"));

      // the Usage page: the models' windows under Every model; no pool's models, no toggle
      const page = await open("usage", "http://magpie.test/?view=usage", { width: 900, height: 900 });
      const sub = page.locator(".subscription-card", { hasText: "AGY-CLI" });
      await sub.locator(".quota-windows").first().waitFor();
      const account = (user) => sub.locator(".subscription-account", { hasText: user });
      const toggle = account("new@example.com").locator("button.quota-every");
      assert.equal((await toggle.textContent()).trim(), words[lang].every);
      await toggle.click();
      await page.waitForTimeout(200);
      const names = await sub.locator(".quota-windows").evaluateAll((bs) => bs.map((b) => [...b.querySelectorAll(".quota-labels > span:first-child")].map((e) => e.textContent)));
      assert(names.some((n) => JSON.stringify(n) === JSON.stringify(["Claude Opus 5.5", "Gemini 3.8 Flash", "GPT-OSS 120B"])), "the models listed: " + JSON.stringify(names));
      assert.equal(await sub.locator("button.quota-every").count(), 1, "no Every model (0) beside the one with models");
      assert.equal(names.filter((n) => n.length === 3).length, 3, "every account's three windows shown: " + JSON.stringify(names));
      assert.deepEqual(errors, []);
    });
  }
}
