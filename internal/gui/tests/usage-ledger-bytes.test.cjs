// Run with Node's test runner and Playwright on the module path; see README.md.
// The Usage page's Requests says what a request sent up to the vendor and got
// back (faithleysath, matrix0487 on Discord): a row's details name both, and
// what DeepSeek's image files kept out of it (#5407); a row from before they
// were kept says nothing of them, not 0. Under the Requests card the period's
// traffic reads "↑ up · ↓ down", with what the image files saved beside it,
// and a period with none says nothing. At 1180 and 420 wide the page never
// scrolls sideways. Chromium and WebKit, every language; no backend, the API
// is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();

const ROWS = [
  // the gateway's, with its bytes: 1.2 MB up, 34 KB down, 72 KB saved
  { t: new Date(now - 60e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color", provider: "deepseek", providerName: "DeepSeek", req: "deepseek-flash", model: "deepseek-flash", served: "deepseek-flash", in: 300, out: 40, ms: 2380, status: 200, rid: "req_new", ep: "/v1/chat/completions", cost: 0.01, priced: true, up_bytes: 1_234_567, plain_bytes: 1_234_567, down_bytes: 34_100, image_files_saved: 72_039 },
  // one from before the bytes were kept
  { t: new Date(now - 120e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color", provider: "deepseek", providerName: "DeepSeek", req: "deepseek-flash", model: "deepseek-flash", served: "deepseek-flash", in: 200, out: 20, ms: 1200, status: 200, rid: "req_old", ep: "/v1/chat/completions", cost: 0.01, priced: true },
  // an upload that cost more than it saved this once
  { t: new Date(now - 180e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color", provider: "deepseek", providerName: "DeepSeek", req: "deepseek-flash", model: "deepseek-flash", served: "deepseek-flash", in: 200, out: 20, ms: 1200, status: 200, rid: "req_upload", ep: "/v1/chat/completions", cost: 0.01, priced: true, up_bytes: 54_000, down_bytes: 900, image_files_saved: -40 },
];

function page(q, bytes) {
  return {
    period: q.get("period"), rows: bytes ? ROWS : ROWS.slice(1, 2), offset: 0, total: bytes ? 3 : 1, calls: bytes ? 3 : 1, errors: 0,
    input: 700, output: 80, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0.03, unpriced: 0,
    ...(bytes ? { up_bytes: 1_288_567, down_bytes: 35_000, image_files_saved: 71_999 } : {}),
    agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }],
  };
}

function server(lang, bytes) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: { rate: 7.2, at: new Date().toISOString() } });
    if (url.pathname === "/api/usage/requests") return json(page(url.searchParams, bytes));
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 3, errors: 0, input: 700, output: 80, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 0, cost: 0.03, bucket: "day", series: [], agents: [], models: [], path: "~/.config/magpie/usage.jsonl" });
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
  en: { up: "Sent upstream", down: "Received", saved: "Saved by image files", line: "↑ 1.3 MB · ↓ 35 KB · 72 KB saved by image files" },
  zh: { up: "发往上游", down: "收到", saved: "图片文件省下", line: "↑ 1.3 MB · ↓ 35 KB · 图片文件省下 72 KB" },
  "zh-TW": { up: "送往上游", down: "收到", saved: "圖片檔案省下", line: "↑ 1.3 MB · ↓ 35 KB · 圖片檔案省下 72 KB" },
  ja: { up: "上流へ送信", down: "受信", saved: "画像ファイルで節約", line: "↑ 1.3 MB · ↓ 35 KB · 画像ファイルで 72 KB 節約" },
  de: { up: "An den Anbieter gesendet", down: "Empfangen", saved: "Durch Bilddateien gespart", line: "↑ 1.3 MB · ↓ 35 KB · 72 KB durch Bilddateien gespart" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a request's bytes up and down", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      const w = L[lang];
      for (const width of [1180, 420]) {
        await t.test(lang + " " + width, async () => {
          const errors = [];
          const ctx = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
          const p = await ctx.newPage();
          p.setDefaultTimeout(5000);
          p.setDefaultNavigationTimeout(60000);
          p.on("pageerror", (e) => errors.push(e.message));
          await p.route("**/*", server(lang, true));
          await p.goto("http://magpie.test/");
          await p.locator('[data-view="usage"]').first().click();
          await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
          await p.locator("#ledWrap .led tbody tr.led-row").first().waitFor();

          // the period's traffic under the Requests card
          const io = p.locator("#ledKpi .led-io");
          assert.equal(await io.count(), 1);
          assert.equal((await io.textContent()).trim(), w.line);
          assert.equal(await io.getAttribute("title"), `${w.up}: 1,288,567 B\n${w.down}: 35,000 B`.replace(/,/g, lang === "de" ? "." : ","));

          const rows = p.locator(".led tbody tr.led-row");
          const pairs = (n) => p.locator(".led-detail").nth(n).evaluate((d) => Object.fromEntries([...d.querySelectorAll("dt")].map((dt) => [dt.textContent, dt.nextElementSibling.textContent])));
          await reader.click(p, rows.nth(0));
          const got = await pairs(0);
          assert.equal(got[w.up], "1.2 MB");
          assert.equal(got[w.down], "34 KB");
          assert.equal(got[w.saved], "72 KB");
          // a row from before: none of them, not 0
          await reader.click(p, rows.nth(1));
          const old = await pairs(1);
          for (const k of [w.up, w.down, w.saved]) assert(!(k in old), `an old row shows ${k}: ${old[k]}`);
          // an upload that cost more than it saved says nothing saved
          await reader.click(p, rows.nth(2));
          const upload = await pairs(2);
          assert.equal(upload[w.up], "54 KB");
          assert.equal(upload[w.down], "900 B");
          assert(!(w.saved in upload), "nothing saved is shown for a loss");

          const sideways = await p.evaluate(() => [document.documentElement.scrollWidth - innerWidth, ...[...document.querySelectorAll("#ledKpi .blk")].map((b) => b.scrollWidth - b.clientWidth)]);
          assert(sideways.every((n) => n <= 1), `scrolls sideways: ${sideways}`);
          assert.deepEqual(errors, []);
          await ctx.close();
        });
      }
      // a period of rows from before: the card says nothing of traffic
      await t.test(lang + " old", async () => {
        const ctx = await browser.newContext({ viewport: { width: 1180, height: 760 }, reducedMotion: "reduce" });
        const p = await ctx.newPage();
        p.setDefaultTimeout(5000);
        p.setDefaultNavigationTimeout(60000);
        await p.route("**/*", server(lang, false));
        await p.goto("http://magpie.test/");
        await p.locator('[data-view="usage"]').first().click();
        await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
        await p.locator("#ledWrap .led tbody tr.led-row").first().waitFor();
        await p.locator("#ledKpi .blk").first().waitFor();
        assert.equal(await p.locator("#ledKpi .led-io").count(), 0);
        await ctx.close();
      });
    }
  });
}
