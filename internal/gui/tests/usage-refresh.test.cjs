// Run with Node's test runner and Playwright on the module path; see README.md.
// The Usage page reads its numbers again while it is looked at, as often as
// the reader says: every 5 s to begin with, or every 10 s, 30 s or a minute, or
// never, from the picker beside the cost; the button beside it reads them now,
// on the tab shown (Overview or Requests), and says when they were last read.
// On the Overview it is the allowances' Refresh too, asking for them as
// opening the page does (#486); there is no second Refresh beside them.
// The choice is remembered. A clock stands in for time here, so nothing waits;
// no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const ROWS = [{ t: new Date(now - 60e3).toISOString(), agent: "codex", agentName: "Codex", icon: "codex-color", provider: "relay", providerName: "Relay", host: "team", req: "sol", model: "gpt-6-sol", in: 100, out: 10, ms: 900, status: 200, cost: 0.01, priced: true }];

function server(lang, calls) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: { rate: 7.2, at: new Date().toISOString() } });
    if (url.pathname === "/api/usage/requests") {
      calls.requests++;
      return json({ period: "30d", rows: ROWS, offset: 0, total: 1, calls: 1, errors: 0, input: 100, output: 10, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0.01, unpriced: 0, bucket: "day", series: [], by: { provider: [], agent: [], model: [] }, agents: [], providers: [] });
    }
    if (url.pathname === "/api/usage") {
      calls.overview++;
      return json({ calls: 1, errors: 0, input: 100, output: 10, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 0, cost: 0.01 + calls.overview * 0.001, bucket: "day", series: [{ label: "Mon", input: 100, output: 10, calls: 1, cost: 0.01 }], agents: [{ name: "Codex", calls: 1, cost: 0.01 }], models: [{ name: "gpt-6-sol", calls: 1, cost: 0.01 }], path: "~/.config/magpie/usage.jsonl" });
    }
    if (url.pathname === "/api/usage/quotas") { calls.quotas++; if (url.search === "?asked=1") calls.asked++; return json([]); }
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const L = {
  en: { label: "5 s", off: "Off", min: "1 min", options: ["Off", "5 s", "10 s", "30 s", "1 min"], now: "Refresh now" },
  zh: { label: "5 秒", off: "关闭", min: "1 分钟", options: ["关闭", "5 秒", "10 秒", "30 秒", "1 分钟"], now: "立即刷新" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: the Usage page's refresh period`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const w = L[lang];
      const errors = [], calls = { requests: 0, overview: 0, quotas: 0, asked: 0 };
      const context = await browser.newContext({ viewport: { width: 1180, height: 700 }, reducedMotion: "reduce" });
      const open = async () => {
        const p = await context.newPage();
        p.setDefaultTimeout(5000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.clock.install({ time: now });
        await p.route("**/*", server(lang, calls));
        await p.goto("http://magpie.test/");
        return p;
      };
      // time passes, and the answers to what it asked come in
      const pass = async (p, ms) => { await p.clock.fastForward(ms); await p.waitForTimeout(150); };

      const p = await open();
      await p.locator('[data-view="usage"]').first().click();
      await p.locator("#usageTab .opt").nth(1).click(); // Requests
      await p.locator("#ledWrap .led tbody tr").first().waitFor();
      assert.equal((await p.locator("#usageEvery").textContent()).trim(), w.label, "every 5 s to begin with");

      // every 5 s: another read after a while, and none before
      let n = calls.requests;
      await pass(p, 2000);
      assert.equal(calls.requests, n, "not yet");
      await pass(p, 4000);
      assert(calls.requests > n, "read again after 5 s");

      // the picker offers the five; off reads no more
      await p.locator("#usageEvery").click();
      assert.deepEqual(await p.locator(".proto-menu .pm-item").allTextContents().then((a) => a.map((s) => s.trim())), w.options);
      await p.locator(".proto-menu .pm-item", { hasText: new RegExp("^\\s*" + w.off + "\\s*$") }).click();
      assert.equal((await p.locator("#usageEvery").textContent()).trim(), w.off);
      n = calls.requests;
      await pass(p, 120e3);
      assert.equal(calls.requests, n, "off reads no more");

      // a minute: not at half of it, once by the end
      await p.locator("#usageEvery").click();
      await p.locator(".proto-menu .pm-item", { hasText: w.min }).click();
      n = calls.requests;
      await pass(p, 30e3);
      assert.equal(calls.requests, n, "not at half a minute");
      await pass(p, 35e3);
      assert(calls.requests > n, "once by the end of it");

      // the button reads at once, turns while it does, and says when
      n = calls.requests;
      await p.locator("#usageReload").click();
      await p.waitForTimeout(200);
      assert(calls.requests > n, "read on the click");
      assert(!(await p.locator("#usageReload").evaluate((b) => b.classList.contains("busy"))), "done turning");
      assert((await p.locator("#usageReload").getAttribute("title")).startsWith(w.now), await p.locator("#usageReload").getAttribute("title"));
      assert(/\d{1,2}:\d{2}:\d{2}/.test(await p.locator("#usageReload").getAttribute("title")), "when they were read");

      // on Overview it reads the summary, and the allowances are read now too
      await p.locator("#usageTab .opt").nth(0).click();
      await p.locator("#stats .kpi").first().waitFor();
      const o = calls.overview, q = calls.quotas, a = calls.asked;
      await p.locator("#usageReload").click();
      await p.waitForTimeout(200);
      assert(calls.overview > o, "the summary again");
      assert(calls.quotas > q, "and the allowances");
      // it is the allowances' Refresh too (#486): asked, as that one was, and
      // there is no second Refresh beside them
      assert(calls.asked > a, "the allowances asked for, a Claude account's by its /usage");
      assert.equal(await p.locator("#quotaRefresh").count(), 0, "one refresh on the page");
      const tip = await p.locator("#usageReload").getAttribute("title");
      assert(tip.startsWith(w.now) && tip.includes("/usage"), tip);

      // remembered: another window opens on the minute
      await p.locator('[data-view="usage"]').first().waitFor();
      const again = await open();
      await again.locator('[data-view="usage"]').first().click();
      await again.locator("#usageEvery").waitFor();
      assert.equal((await again.locator("#usageEvery").textContent()).trim(), w.min);
      assert.deepEqual(errors, []);
    });
  }
}
