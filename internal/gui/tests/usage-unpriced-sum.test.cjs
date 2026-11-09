// Run with Node's test runner and Playwright on the module path; see README.md.
// Usage's sum says how many calls it couldn't price (#1856,
// cnlove7777777-art: Kimi K3 Auto and GLM-5.3 Max had "no price", and the
// sum read as if they cost nothing). Overview's and Requests' heads say "2
// calls unpriced" beside the sum, Overview's calls tile counts them too, a
// period with none priced says the count alone instead of nothing (before,
// Requests showed nothing at all), and Sessions,
// which prices by model, says how many models. The count stays at every
// width from a phone's to a wide window, whole, with the sum whole beside
// it and the page never sideways. Every language, Chromium and WebKit; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets"));
const today = new Date().toISOString().slice(0, 10);
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();

const want = {
  en: { calls: "2 calls unpriced", tile: "2 calls unpriced", models: "1 model unpriced" },
  zh: { calls: "2 次调用未计价", tile: "2 次调用未计价", models: "1 个模型未计价" },
  "zh-TW": { calls: "2 次呼叫未計價", tile: "2 次呼叫未計價", models: "1 個模型未計價" },
  ja: { calls: "2 件の呼び出しは価格不明", tile: "2 件の呼び出しは価格不明", models: "1 個のモデルは価格不明" },
  de: { calls: "2 Aufrufe ohne Preis", tile: "2 Aufrufe ohne Preis", models: "1 Modell ohne Preis" },
};

// some: one call priced, two of Kimi K3 Auto not; none: only those two
function overview(p, some) {
  const cost = some ? 0.0012 : 0;
  return {
    period: p, calls: some ? 3 : 2, errors: 0, input: 8600, output: 49, cacheRead: 0, reasoning: 0, unpriced: 2, cost, bucket: "hour",
    series: [{ t: new Date().toISOString(), input: 8600, output: 49, calls: 3 }],
    agents: [{ id: "codex", name: "Codex", calls: 3, errors: 0, input: 8600, output: 49, cost }],
    models: [{ id: "relay/kimi-k3-auto", calls: 2, errors: 0, input: 6000, output: 30, cost: 0 }], path: "",
  };
}
const row = (i, model, cost, priced) => ({ route_id: 900 + i, t: at(i + 1), agent: "codex", agentName: "Codex", icon: "codex-color", provider: "relay", providerName: "relay",
  host: "relay.test", ep: "/v1/responses", req: model, model, served: model, in: 3000, out: 15, cache_read: 0, cache_write: 0, ms: 900, status: 200, rid: "r" + i, cost, priced });
function requests(p, some) {
  const rows = [row(0, "kimi-k3-auto", 0, false), row(1, "kimi-k3-auto", 0, false), ...(some ? [row(2, "gpt-5", 0.0012, true)] : [])];
  return { period: p, rows, offset: 0, total: rows.length, calls: rows.length, errors: 0, input: 9000, output: 45, cache_read: 0, cache_write: 0, cost: some ? 0.0012 : 0, unpriced: 2, by: {}, bucket: "hour", series: [], agents: [] };
}
const usage = [
  { agent: "codex", cwd: "/work/app", model: "gpt-5", input: 4000, output: 400, cache_read: 0, cache_write: 0, cost: 0.02, priced: true },
  { agent: "codex", cwd: "/work/app", model: "kimi-k3-auto", input: 1000, output: 100, cache_read: 0, cache_write: 0, cost: 0, priced: false },
];
const session = {
  agent: "codex", name: "Codex", icon: "openai", id: "c-1", cwd: "/work/app", title: "port the parser", start: at(30), last: at(5),
  models: usage.map(({ agent, cwd, ...m }) => m), input: 5000, output: 500, cache_read: 0, cache_write: 0, cost: 0.02, unpriced: 1,
};

function serve(lang, some) {
  const settings = { theme: "light", lang, quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage") return json(overview(url.searchParams.get("period"), some));
    if (url.pathname === "/api/usage/requests") return json(requests(url.searchParams.get("period"), some));
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/sessions/progress") return json({ indexing: false });
    if (url.pathname === "/api/sessions") return json({ sessions: [session], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: today, to: today, days: [{ date: today, usage, active: [] }], agents: { codex: "Codex" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// the head at one width: the sum (when there is one) and the count both
// whole and in the window, and the page not sideways; "" when so
const look = (sum) => {
  const cost = document.querySelector("#usageCost"), b = cost.querySelector("b"), note = cost.querySelector("span");
  const box = cost.getBoundingClientRect(), n = note?.getBoundingClientRect();
  const got = { text: note?.textContent, sum: b?.textContent, shown: !!n?.width, cutNote: note && note.scrollWidth > note.clientWidth + 1, wide: document.documentElement.scrollWidth, inner: innerWidth };
  const whole = got.shown && !got.cutNote && n.right <= innerWidth + 0.5 && got.wide <= got.inner &&
    (sum ? got.sum === sum && b.getBoundingClientRect().right <= box.right + 0.5 && b.getBoundingClientRect().left >= box.left - 0.5 : !b);
  return whole ? "" : JSON.stringify(got);
};

async function sweep(page, sum, text) {
  const cut = [];
  for (const w of [360, 420, 520, 640, 651, 760, 900, 1100, 1240]) {
    await page.setViewportSize({ width: w, height: 700 });
    // the head is fitted a frame or two after a resize
    const ok = await page.waitForFunction(`!(${look})(${JSON.stringify(sum)})`, null, { polling: "raf", timeout: 3000 }).then(() => true, () => false);
    if (!ok) cut.push(`${w}px: ${await page.evaluate(`(${look})(${JSON.stringify(sum)})`)}`);
    assert.equal((await page.locator("#usageCost span").textContent()).trim(), text, `at ${w}px`);
  }
  assert.deepEqual(cut, [], "the count or the sum is cut, hidden, or the page scrolls sideways");
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: Usage's sum says how many it couldn't price`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const errors = [];
      const open = async (tab, some) => {
        const page = await (await browser.newContext({ viewport: { width: 1100, height: 700 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, some));
        await page.addInitScript((tab) => { localStorage.setItem("magpie.usageTab", tab); localStorage.setItem("magpie.sessRange", "all"); }, tab);
        await page.goto("http://magpie.test/?view=usage");
        await page.waitForFunction(() => document.querySelector("#usageCost span:not(.skeleton)"));
        return page;
      };
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await browser.close();
      });
      const w = want[lang];

      // Overview: the sum and how many calls it leaves out; the calls tile too
      let page = await open("usage", true);
      await page.waitForFunction(() => document.querySelector("#usageCost b")?.textContent.includes("0.001"));
      assert.match(await page.locator("#usageCost").getAttribute("title"), /2/, "the tooltip says how many");
      const tile = await page.locator("#stats .tile, #stats > *").evaluateAll((ts, n) => ts.some((x) => x.textContent.includes(n)), w.tile);
      assert(tile, `the calls tile says "${w.tile}"`);
      await sweep(page, "≈$0.001+", w.calls);
      if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-unpriced-overview.png`) });

      // nothing priced: no sum, and the count alone rather than nothing
      page = await open("usage", false);
      await sweep(page, null, w.calls);

      // Requests: the same count beside its sum; and with nothing priced
      page = await open("requests", true);
      await page.waitForFunction(() => document.querySelector("#usageCost b")?.textContent.includes("0.001"));
      await sweep(page, "≈$0.001+", w.calls);
      page = await open("requests", false);
      await sweep(page, null, w.calls);

      // Sessions prices by model: how many models
      page = await open("sessions", true);
      await page.locator("#sessList .row.sess").first().waitFor();
      await page.waitForFunction(() => document.querySelector("#usageCost b"));
      await sweep(page, "≈$0.020+", w.models);
      assert.deepEqual(errors, []);
    });
  }
}
