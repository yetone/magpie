// Run with Node's test runner and Playwright on the module path; see README.md.
// Usage's sum at the head's right is never cut (#1599, sean908): in the
// window, between 640px and the width the tabs, the periods with Custom
// named and the refresh need, the sum was the one thing to give way, and
// $0.001 read "$0.". At every width from a phone's to a wide window the
// sum is whole and on the page, and the page never scrolls sideways.
// English, Chinese, Japanese and German, Chromium and WebKit; no backend,
// the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets"));

function serve(lang) {
  const settings = { theme: "light", lang, quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage") {
      const p = url.searchParams.get("period");
      return json({
        period: p, calls: 3, errors: 2, input: 8600, output: 49, cacheRead: 512, reasoning: 35, unpriced: 0, cost: 0.0012, bucket: "hour",
        series: [{ t: new Date().toISOString(), input: 8600, output: 49, calls: 3 }],
        agents: [{ id: "codex", name: "Codex", calls: 3, errors: 2, input: 8600, output: 49, cost: 0.0012 }],
        models: [{ id: "codex/gpt-5.5", calls: 3, errors: 2, input: 8600, output: 49, cost: 0.0012 }], path: "",
      });
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    test(`${engine} ${lang}: Usage's sum is whole at every width`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      let page;
      t.after(async () => {
        if (process.env.ARTIFACT_DIR && page) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-usage-sum-whole.png`) });
        }
        await browser.close();
      });
      page = await (await browser.newContext({ viewport: { width: 651, height: 600 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(8000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=usage");
      await page.waitForFunction(() => document.querySelector("#usageCost b")?.textContent.includes("0.001"));
      const cut = [];
      for (let w = 380; w <= 1240; w += 10) {
        await page.setViewportSize({ width: w, height: 600 });
        const look = () => {
          const cost = document.querySelector("#usageCost"), b = cost.querySelector("b");
          if (!b) return JSON.stringify({ text: cost.textContent }); // redrawn: not a sum yet
          const c = cost.getBoundingClientRect(), s = b.getBoundingClientRect();
          const got = { head: cost.parentElement.className, text: b.textContent, sum: [s.left, s.right], box: [c.left, c.right], wide: document.documentElement.scrollWidth, inner: innerWidth };
          const whole = got.text === "$0.001" && got.sum[1] <= got.box[1] + 0.5 && got.sum[0] >= got.box[0] - 0.5 && got.sum[1] <= got.inner && got.wide <= got.inner;
          return whole ? "" : JSON.stringify(got);
        };
        // the head is fitted a frame or two after a resize: given a few
        // seconds to come whole, where a cut sum never does. A busy machine
        // can hold the page's frames past that wait (#2142: "1220px: ",
        // whole once looked at again), so what counts is how it is then.
        const whole = await page.waitForFunction(`!(${look})()`, null, { polling: "raf", timeout: 3000 }).then(() => true, () => false);
        const now = whole ? "" : await page.evaluate(look);
        if (now) cut.push(`${w}px: ${now}`);
      }
      assert.deepEqual(cut, [], "the sum is cut, or the page scrolls sideways");
      assert.deepEqual(errors, []);
    });
  }
}
